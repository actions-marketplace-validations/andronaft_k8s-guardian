# Enforcing rules in the cluster: `export`

Use the same rules in CI and at admission time.

```bash
k8s-guardian export vap > guardrails.yaml          # built-in + .k8s-guardian/rules
k8s-guardian export vap --builtin KG001,KG010 --rules policies/ --action Warn
kubectl apply -f guardrails.yaml

k8s-guardian export kyverno > kyverno-policies.yaml   # if you already run Kyverno (>= 1.11, CEL)
```

## ValidatingAdmissionPolicy

`export vap` generates native **ValidatingAdmissionPolicies** (Kubernetes ≥ 1.30, CEL). The API server then enforces your guardrails, so no Kyverno, OPA or webhook needs to run.

* Severities map to actions: `error` becomes Deny, `warning` becomes Warn, `info` becomes Audit.
* `kube-system` is excluded by default.

```text
$ kubectl apply -f deploy.yaml
The deployments "api" is invalid: ValidatingAdmissionPolicy 'k8s-guardian-org001-require-contact-email' denied request:
ORG001 require-contact-email: add metadata.annotations['example.com/contact'] with a valid email
```

CI and the cluster make **the same decision**. An e2e suite starts a real kube-apiserver, applies the exported policies and compares each admission decision with the CLI engine. It covers Pods, Deployments and CronJobs, and every custom-rule operator (see [`test/e2e`](../test/e2e)).

## Kyverno

`error` becomes Enforce, and everything else becomes Audit (policy reports). An e2e test runs the exported ClusterPolicies with the real Kyverno CLI and checks that it reports **exactly** the same rules as the CLI engine.

## What can be exported

* **Built-in rules:** KG001–KG007 and KG010–KG012.
* **Custom rules** are exported too, including the paths from `rules:` in the [config file](configuration.md). Resource-scoped custom rules need `match.kinds` to be exported.
* **Rules that need other resources or the cluster stay in `check`.** These include Service/workload wiring, HPA-aware replica counts, RBAC and quotas.
* **Argo Rollouts are not part of the exported policies.**
