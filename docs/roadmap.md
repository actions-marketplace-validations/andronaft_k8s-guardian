# Roadmap

The plan lives in [GitHub milestones](https://github.com/andronaft/k8s-guardian/milestones) and issues. This page explains the priorities. Release notes for what already shipped are on the [releases page](https://github.com/andronaft/k8s-guardian/releases).

**Guiding principle:** a finding people can't trust is worse than no finding. Accuracy on real manifests comes before new features, and every new rule is run against [30 real Helm charts](real-world-test.md) before it ships.

## Shipped: [v0.7.0](https://github.com/andronaft/k8s-guardian/releases/tag/v0.7.0), accuracy and CI ergonomics

`diff --server-side`, LV003 with taints and node affinity, Helm `--kube-version`, KG017 checked against the official guide, tests on macOS and Windows, and the 30-chart test in CI (#6–#11).

## Next: [v0.8.0](https://github.com/andronaft/k8s-guardian/milestone/2), coverage and trust in `--fix`

Check what matters most in the Kubernetes security model and more of the workloads people actually run, and keep what `--fix` changes small and fast enough for large repositories.

| Issue | |
|-------|---|
| [#12](https://github.com/andronaft/k8s-guardian/issues/12) | Pod Security Standards profiles: `--profile baseline\|restricted` |
| [#19](https://github.com/andronaft/k8s-guardian/issues/19) | `diff --server-side`: report the API server's warnings (Pod Security, deprecations) |
| [#21](https://github.com/andronaft/k8s-guardian/issues/21) | `--fix`: keep the document's list indentation *(help wanted)* |
| [#20](https://github.com/andronaft/k8s-guardian/issues/20) | Cross-resource rules in linear time (24,000 objects take 61 s today) *(help wanted)* |
| [#13](https://github.com/andronaft/k8s-guardian/issues/13) | New rule: no host ports *(good first issue)* |
| [#14](https://github.com/andronaft/k8s-guardian/issues/14) | New rule: PodDisruptionBudget for workloads with 2+ replicas |
| [#15](https://github.com/andronaft/k8s-guardian/issues/15) | Configurable workload kinds for CRDs (Knative, KubeVirt, …) |
| [#16](https://github.com/andronaft/k8s-guardian/issues/16) | Export Argo Rollouts and probe rules to VAP and Kyverno |
| [#5](https://github.com/andronaft/k8s-guardian/issues/5) | GitHub Action: inputs for config, baseline and Helm values *(good first issue)* |
| [#22](https://github.com/andronaft/k8s-guardian/issues/22) | SARIF: describe custom rules, repository-relative URIs, schema test *(good first issue)* |

## [Later](https://github.com/andronaft/k8s-guardian/milestone/3): ideas that need evidence first

| Issue | |
|-------|---|
| [#17](https://github.com/andronaft/k8s-guardian/issues/17) | Flag literal secrets in env values, only if a prototype proves precise on real manifests |
| [#18](https://github.com/andronaft/k8s-guardian/issues/18) | Recommend HPA targets from Prometheus history (`cost`, experimental) |

## How to help

* **Try it on your manifests** ([try it](try-it.md)) and [report false positives](https://github.com/andronaft/k8s-guardian/issues/new?labels=false+positive). They are the most valuable reports.
* **Pick an issue:** [good first issues](https://github.com/andronaft/k8s-guardian/issues?q=is%3Aopen+label%3A%22good+first+issue%22) come with pointers to the code. [CONTRIBUTING.md](../CONTRIBUTING.md) explains how to add a rule.
* **Propose something:** open an issue first for larger changes, so we can agree on the approach before you write code.
