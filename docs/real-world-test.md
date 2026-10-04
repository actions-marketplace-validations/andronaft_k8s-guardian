# Real-world test: 30 popular Helm charts

To see how k8s-guardian behaves on real manifests (not on hand-written
examples), it was run against 30 widely used Helm charts: 652 Kubernetes
objects in total. The run found five bugs in k8s-guardian itself. All of
them are fixed, and each has a regression test.

Reproduce it with [`hack/charts/run.sh`](../hack/charts/run.sh)
(needs `git`, `helm`, `python3` with PyYAML).

## Setup

* Charts are rendered from their Git repositories (default branch, 2 October
  2026) with `helm template` and **default values**, for Kubernetes 1.33
  (Longhorn requires 1.34). Charts that need a value to render at all got
  the minimum: a cluster name, the collector mode, Loki's single-binary
  mode.
* Subcharts from remote repositories are left out, except
  kube-state-metrics, node-exporter, Grafana and Bitnami's `common`, which
  were taken from their sources.
* Rendering the default branch means some charts are development builds:
  Kyverno and cert-manager have version `0.0.0`, and Kyverno's images use
  `:latest`, so its KG010 findings would not appear for a release.

## Bugs found in k8s-guardian

| # | Problem | Seen in | Fix |
|---|---------|---------|-----|
| 1 | KG016 flagged a numeric `targetPort` when the container declares no ports. `containerPort` is informational, so the Service works. | Harbor (database, Redis), Vault (agent injector) | A numeric `targetPort` is only compared when the workload declares ports. Named ports are still always checked. |
| 2 | KG016 flagged Services whose pods are created by an operator or controller, not by the chart. | kube-prometheus-stack (Prometheus, Alertmanager), Longhorn | Only a near miss is reported: a workload carrying some, but not all, of the selector's labels (the typo case). |
| 3 | Liveness and readiness probes were required on run-once Pods such as Helm test hooks. | 28 findings in 8 charts | Pods with `restartPolicy: Never` or `OnFailure` are treated like Jobs. |
| 4 | `--fix` re-encoded every document in a file. Reformatting touched up to 85,000 lines (Kyverno), and in Loki it **changed the content of a ConfigMap** (a leading empty line in a block scalar was dropped). | all charts | Documents that a fix does not change are written back byte for byte. Changed documents keep block scalars intact. |
| 5 | `--fix` produced manifests that do not run. It set `privileged: false` on Longhorn's manager (the API server rejects the result: Bidirectional mount propagation requires privileged), removed `hostNetwork` from node-exporter, and set `runAsNonRoot`, `readOnlyRootFilesystem` and `drop: [ALL]` on images that need root or a writable filesystem. | Longhorn, Falco, node-exporter, and any image running as root | `--fix` now applies only fixes that don't change how a workload runs. The others need `--fix --unsafe-fixes` and a review. Privileged containers and host namespaces are reported but never changed. KG005 and KG007 skip privileged containers, which have every privilege anyway. |

## Results

524 findings: 216 errors, 183 warnings, 125 info.

| Rule | Findings | What it means in these charts |
|------|---------:|-------------------------------|
| KG001 / KG002 resources | 81 / 84 | Most charts ship `resources: {}` and expect you to set them. Real, but a values-file decision. |
| KG014 automount token | 77 | Many controllers do need the API token; info only. |
| KG015 replicas | 48 | Default single replica; often fine for leader-elected controllers. |
| KG006 read-only root FS | 41 | |
| KG007 drop capabilities | 34 | |
| KG013 seccomp | 32 | The only class `--fix` changes by default, together with KG005. |
| KG003 run as non-root | 27 | Nothing in the manifest prevents root; the image may still use a non-root `USER`. |
| KG005 privilege escalation | 27 | |
| KG008 / KG009 probes | 24 / 18 | |
| KG010 image tags | 17 | Mostly Kyverno development images (see Setup). |
| KG012 hostPath | 7 | Deliberate: log shippers, storage, node monitoring. |
| KG004 privileged / KG011 host namespaces | 4 / 3 | Deliberate: Falco, Longhorn, node-exporter. Use `k8s-guardian.io/ignore` on those resources. |

Bitnami's Redis, PostgreSQL and NGINX charts and KEDA come out clean apart
from info findings, which shows what secure defaults look like.

Every finding was checked against the rendered YAML. None of the remaining
ones is wrong about the manifest. Whether it matters is a policy decision,
which is what the severities, `--skip` and the ignore annotation are for.

## Rules added after this run

KG018 (`rbac-wildcard`) was added later and checked on the same charts before release. It reports 7 roles, one finding each:

* Argo CD: `argocd-application-controller` (`"*"` verbs and resources) and `argocd-server` (`"*"` resources);
* KEDA: `keda-operator`;
* Kyverno: `kyverno:migrate-resources`;
* Longhorn: `longhorn-role` and the `longhorn` Role;
* Velero: `velero-server`.

All of these really grant wildcards. Whether that is acceptable for a controller like Argo CD is a policy decision; the rule is a warning, and the ignore annotation or `skip` handles deliberate cases. The other numbers on this page are unchanged by the later releases.

## Checks on `--fix`

For every chart, in both modes (`--fix` and `--fix --unsafe-fixes`):

* a second run changes nothing (idempotent);
* a run that fixes nothing returns the input byte for byte;
* no object is added or dropped, and ConfigMaps, Secrets and CRDs are unchanged;
* `kubectl apply --dry-run=server` against kube-apiserver 1.37 reports no
  error that the original manifest did not have. Errors such as missing
  service accounts in an empty test cluster show up for the original too.

A server dry run proves the API accepts the result, not that the workload
still starts. That is why the fixes that can stop it from starting are
opt-in.

| Chart | Source | Objects | Errors | Warnings | Info |
|-------|--------|--------:|-------:|---------:|-----:|
| ingress-nginx | [kubernetes/ingress-nginx](https://github.com/kubernetes/ingress-nginx) | 18 | 5 | 1 | 4 |
| grafana | [grafana-community/helm-charts](https://github.com/grafana-community/helm-charts) | 10 | 4 | 0 | 3 |
| prometheus | [prometheus-community/helm-charts](https://github.com/prometheus-community/helm-charts) | 15 | 9 | 10 | 4 |
| kube-state-metrics | [prometheus-community/helm-charts](https://github.com/prometheus-community/helm-charts) | 5 | 2 | 0 | 2 |
| node-exporter | [prometheus-community/helm-charts](https://github.com/prometheus-community/helm-charts) | 3 | 3 | 4 | 0 |
| kube-prometheus-stack | [prometheus-community/helm-charts](https://github.com/prometheus-community/helm-charts) | 126 | 19 | 8 | 9 |
| metrics-server | [kubernetes-sigs/metrics-server](https://github.com/kubernetes-sigs/metrics-server) | 10 | 1 | 0 | 2 |
| external-dns | [kubernetes-sigs/external-dns](https://github.com/kubernetes-sigs/external-dns) | 5 | 2 | 0 | 2 |
| cluster-autoscaler | [kubernetes/autoscaler](https://github.com/kubernetes/autoscaler) | 8 | 3 | 5 | 2 |
| aws-load-balancer-controller | [aws/eks-charts](https://github.com/aws/eks-charts) | 11 | 2 | 2 | 1 |
| traefik | [traefik/traefik-helm-chart](https://github.com/traefik/traefik-helm-chart) | 6 | 2 | 0 | 2 |
| external-secrets | [external-secrets/external-secrets](https://github.com/external-secrets/external-secrets) | 19 | 6 | 4 | 6 |
| kyverno | [kyverno/kyverno](https://github.com/kyverno/kyverno) | 73 | 14 | 4 | 13 |
| vault | [hashicorp/vault-helm](https://github.com/hashicorp/vault-helm) | 13 | 7 | 10 | 5 |
| keda | [kedacore/charts](https://github.com/kedacore/charts) | 29 | 0 | 0 | 6 |
| sealed-secrets | [bitnami-labs/sealed-secrets](https://github.com/bitnami-labs/sealed-secrets) | 10 | 2 | 0 | 2 |
| podinfo | [stefanprodan/podinfo](https://github.com/stefanprodan/podinfo) | 5 | 12 | 16 | 5 |
| jenkins | [jenkinsci/helm-charts](https://github.com/jenkinsci/helm-charts) | 14 | 10 | 14 | 3 |
| argo-cd | [argoproj/argo-helm](https://github.com/argoproj/argo-helm) | 59 | 20 | 9 | 15 |
| redis | [bitnami/charts](https://github.com/bitnami/charts) | 14 | 0 | 0 | 1 |
| postgresql | [bitnami/charts](https://github.com/bitnami/charts) | 7 | 0 | 0 | 1 |
| nginx | [bitnami/charts](https://github.com/bitnami/charts) | 6 | 0 | 0 | 1 |
| falco | [falcosecurity/charts](https://github.com/falcosecurity/charts) | 6 | 12 | 12 | 1 |
| velero | [vmware-tanzu/helm-charts](https://github.com/vmware-tanzu/helm-charts) | 14 | 6 | 8 | 3 |
| fluent-bit | [fluent/helm-charts](https://github.com/fluent/helm-charts) | 7 | 7 | 9 | 2 |
| harbor | [goharbor/harbor-helm](https://github.com/goharbor/harbor-helm) | 31 | 16 | 9 | 7 |
| opentelemetry-collector | [open-telemetry/opentelemetry-helm-charts](https://github.com/open-telemetry/opentelemetry-helm-charts) | 4 | 3 | 4 | 2 |
| longhorn | [longhorn/longhorn](https://github.com/longhorn/longhorn) | 64 | 29 | 39 | 8 |
| cert-manager | [cert-manager/cert-manager](https://github.com/cert-manager/cert-manager) | 44 | 8 | 3 | 7 |
| loki | [grafana/loki](https://github.com/grafana/loki) | 16 | 12 | 12 | 6 |
