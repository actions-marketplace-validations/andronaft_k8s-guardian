# Contributing

Thanks for helping make Kubernetes deployments safer! 🛡️

## Development

```bash
make build   # bin/k8s-guardian + bin/kubectl-guard
make test    # unit and end-to-end tests (no cluster or API key needed)
make lint    # go vet + gofmt
```

Tests never talk to a real cluster or the Claude API: cluster access is
exercised with `internal/cluster.Fake` and a scripted fake `kubectl`
(`internal/cli/testdata/fakebin`), and Claude with a mocked HTTP server
(`internal/ai/*_test.go`).

## Adding a built-in rule

1. Add a `*rules.Rule` to `internal/rules/rules.go` (per container/pod) or
   `internal/rules/cross.go` (whole-object / cross-resource checks). Use the
   next free `KG0xx` ID; IDs are stable and never reused.
2. If the fix is unambiguous, implement `Fix`, keep it idempotent and edit
   YAML nodes through `internal/yamlx` so comments survive.
3. Add tests (violation, no violation, fix) and a row to the README rule table.

Policies that are specific to one organisation are better expressed as
[custom rules](docs/custom-rules.md).

## Commits and PRs

- Keep PRs focused; describe *why* in the PR description.
- CI must be green (lint, race tests, the GitHub Action self-test).
