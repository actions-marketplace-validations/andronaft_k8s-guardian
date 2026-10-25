# Roadmap

The plan lives in [GitHub milestones](https://github.com/andronaft/k8s-guardian/milestones) and issues. This page explains the priorities. Release notes for what already shipped are on the [releases page](https://github.com/andronaft/k8s-guardian/releases).

**Guiding principle:** a finding people can't trust is worse than no finding. Accuracy on real manifests comes before new features, and every new rule is run against [30 real Helm charts](real-world-test.md) before it ships.

## Shipped: [v0.7.0](https://github.com/andronaft/k8s-guardian/releases/tag/v0.7.0), accuracy and CI ergonomics

`diff --server-side`, LV003 with taints and node affinity, Helm `--kube-version`, KG017 checked against the official guide, tests on macOS and Windows, and the 30-chart test in CI (#6–#11).

## Ready to release: [v0.8.0](https://github.com/andronaft/k8s-guardian/milestone/2), trust in `--fix`, speed, easy adoption

All on `main`:

| | |
|---|---|
| [#21](https://github.com/andronaft/k8s-guardian/issues/21) | `--fix` writes only the lines it adds: 1,043 → 134 changed lines on the 30 charts |
| [#20](https://github.com/andronaft/k8s-guardian/issues/20) | Linear-time checks: 24,000 objects in 2.6 s instead of 61 s |
| [#19](https://github.com/andronaft/k8s-guardian/issues/19) | `diff --server-side` reports the API server's warnings (DF009): pods Pod Security will reject |
| [#22](https://github.com/andronaft/k8s-guardian/issues/22) | SARIF: custom rules described, repository-relative URIs, schema-tested |
| | `k8s-guardian init`: config, baseline and GitHub workflow in one step |
| | GitHub Action `new-only`: a pull request fails only on problems it adds |
| | `--live`: LV003 warns when no node has the pod's requests free |
| [#13](https://github.com/andronaft/k8s-guardian/issues/13) | KG019: no host ports |
| [#14](https://github.com/andronaft/k8s-guardian/issues/14) | KG020: PodDisruptionBudget for workloads with 2+ replicas |
| [#17](https://github.com/andronaft/k8s-guardian/issues/17) | KG021: literal credentials in env values (the prototype was precise on real manifests) |
| | KG013, KG014 and KG019 exported as ValidatingAdmissionPolicies and Kyverno policies, verified against a real API server and Kyverno |
| | Every `kubectl` call has a timeout; unknown rules in `skip` are an error; Helm charts render their `crds/` |
| [#29](https://github.com/andronaft/k8s-guardian/issues/29) | `rules --format json` for tooling |

## Next: [v0.9.0](https://github.com/andronaft/k8s-guardian/milestone/4), coverage

Check what matters most in the Kubernetes security model, and more of the workloads people actually run.

| Issue | |
|-------|---|
| [#12](https://github.com/andronaft/k8s-guardian/issues/12) | Pod Security Standards profiles: `--profile baseline\|restricted` |
| [#15](https://github.com/andronaft/k8s-guardian/issues/15) | Configurable workload kinds for CRDs (Knative, KubeVirt, …) |
| [#16](https://github.com/andronaft/k8s-guardian/issues/16) | Export Argo Rollouts and probe rules to VAP and Kyverno |
| [#26](https://github.com/andronaft/k8s-guardian/issues/26) | GitHub Action: post the findings as a pull request comment |
| [#30](https://github.com/andronaft/k8s-guardian/issues/30) | GitHub Action: use the release binary instead of building from source |
| [#28](https://github.com/andronaft/k8s-guardian/issues/28) | LV003: required pod anti-affinity and topology spread constraints |
| [#27](https://github.com/andronaft/k8s-guardian/issues/27) | Kustomize: render `helmCharts` entries *(good first issue)* |

## [Later](https://github.com/andronaft/k8s-guardian/milestone/3): ideas that need evidence first

| Issue | |
|-------|---|
| [#18](https://github.com/andronaft/k8s-guardian/issues/18) | Recommend HPA targets from Prometheus history (`cost`, experimental) |

## How to help

* **Try it on your manifests** ([try it](try-it.md)) and [report false positives](https://github.com/andronaft/k8s-guardian/issues/new?labels=false+positive). They are the most valuable reports.
* **Pick an issue:** [good first issues](https://github.com/andronaft/k8s-guardian/issues?q=is%3Aopen+label%3A%22good+first+issue%22) come with pointers to the code. [CONTRIBUTING.md](../CONTRIBUTING.md) explains how to add a rule.
* **Propose something:** open an issue first for larger changes, so we can agree on the approach before you write code.
