# Contributing

Thanks for helping make Kubernetes deployments safer! 🛡️

By taking part you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Development

```bash
make build   # bin/k8s-guardian + bin/kubectl-guard
make test    # unit tests (no cluster or API key needed)
make e2e     # real kube-apiserver + etcd (envtest), Kyverno CLI and Prometheus
make lint    # go vet + gofmt
make demo    # re-record docs/demo.gif (needs agg)
hack/charts/run.sh   # run against 30 popular Helm charts (docs/real-world-test.md)
```

Tests never talk to a real cluster or the Claude API: cluster access is
exercised with `internal/cluster.Fake` and a scripted fake `kubectl`
(`internal/cli/testdata/fakebin`), and Claude with a mocked HTTP server
(`internal/ai/*_test.go`).

## Architecture

```text
cmd/k8s-guardian     one binary: CLI, kubectl-guard plugin, MCP server
action.yml           GitHub Action (composite, builds from source)
internal/rules       rule engine, built-in rules and deterministic fixes
internal/custom      declarative custom-rule DSL (paths, when/assert, ops)
internal/config      .k8s-guardian.yaml
internal/baseline    known findings, matched without line numbers
internal/manifest    loading (files, Helm, Kustomize) and byte-preserving writes
internal/live        cluster-aware checks (quota, nodes, LimitRange, references)
internal/diff        structural diff + breaking-change analysis
internal/cost        cost model, HPA-aware estimates, right-sizing
internal/export      rules → ValidatingAdmissionPolicy / Kyverno ClusterPolicy (CEL)
internal/ai          Claude: fixes, rule generation, right-sizing (structured outputs)
internal/tui         Bubble Tea review UI
internal/cluster     read-only kubectl adapter (cached) + fake for tests
internal/mcp         MCP stdio server
```

## Adding a built-in rule

1. Add a `*rules.Rule` to `internal/rules/rules.go` (per container/pod) or
   `internal/rules/cross.go` (whole-object / cross-resource checks). Use the
   next free `KG0xx` ID; IDs are stable and never reused.
2. If the fix is unambiguous, implement `Fix`, keep it idempotent and edit
   YAML nodes through `internal/yamlx` so comments survive. Set `Unsafe: true`
   when the fix can change how a working workload runs (it then needs
   `--unsafe-fixes`).
3. Add tests (violation, no violation, fix) and a row to the rule table in
   [docs/check.md](docs/check.md).
4. Run `hack/charts/run.sh` and look at what the rule reports on real charts
   before calling it done.

Policies that are specific to one organisation are better expressed as
[custom rules](docs/custom-rules.md).

## Releases

Publish a GitHub release with a `v*` tag. GoReleaser builds the binaries and
the Docker image, and krew-release-bot updates the Krew index from
[`.krew.yaml`](.krew.yaml).

## Commits and PRs

- Keep PRs focused; describe *why* in the PR description.
- CI must be green (lint, race tests, the GitHub Action self-test).
