# CI integration

## GitHub Action

Findings appear as **annotations directly on the PR diff**, together with a Markdown job summary and an optional cost estimate:

```yaml
# .github/workflows/k8s-guardian.yml
name: k8s-guardian
on: [pull_request]
permissions:
  contents: read
jobs:
  guardrails:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: andronaft/k8s-guardian@v0.5.0
        with:
          path: k8s/ charts/my-app     # files, directories or Helm charts
          fail-on: error               # error | warning | info
          skip: KG015                  # optional
          cost: "true"                 # add $/month to the job summary
```

The action runs in the repository root, so a [`.k8s-guardian.yaml`](configuration.md) there (skip, severity, exclude, baseline) applies automatically.

| Input | Default | Description |
|-------|---------|-------------|
| `path` | `.` | Files, directories or Helm charts (whitespace separated) |
| `fail-on` | `error` | Fail when findings at or above this severity remain |
| `skip` | | Comma-separated rule IDs or names to skip |
| `rules` | | Custom rule files or directories (`.k8s-guardian/rules` is always loaded) |
| `live` | `false` | Also check against the cluster in the job's kubeconfig (`--live`) |
| `namespace` | | Namespace for objects without one |
| `sarif-file` | | Also write SARIF, for `github/codeql-action/upload-sarif` |
| `cost` | `false` | Add a monthly cost estimate to the job summary |
| `args` | | Extra arguments for `k8s-guardian check`, e.g. `--values values-prod.yaml` |

Outputs: `errors`, `warnings`, `infos`, `fixable`, `sarif-file`.

### Code scanning (SARIF)

```yaml
permissions:
  contents: read
  security-events: write
steps:
  - uses: actions/checkout@v7
  - uses: andronaft/k8s-guardian@v0.5.0
    with:
      path: k8s/
      sarif-file: k8s-guardian.sarif
  - uses: github/codeql-action/upload-sarif@v4
    if: always()
    with:
      sarif_file: k8s-guardian.sarif
```

### Pre-deploy checks

With cluster credentials in the job, you can block breaking changes before deploying:

```yaml
  - uses: andronaft/k8s-guardian@v0.5.0        # also puts k8s-guardian on PATH
    with: {path: k8s/, live: "true", namespace: prod}
  - run: k8s-guardian diff -f k8s/ -n prod --format github
```

## Other CI systems

Use `--format github|markdown|sarif|json` and the exit codes: `0` passed, `1` findings at or above `--fail-on` (default `error`), `2` usage or runtime error. The Docker image works anywhere:

```bash
docker run --rm -v "$PWD:/work" ghcr.io/andronaft/k8s-guardian:0.5.0 check -f k8s/
```

## pre-commit

```yaml
# .pre-commit-config.yaml
repos:
  - repo: https://github.com/andronaft/k8s-guardian
    rev: v0.5.0
    hooks:
      - id: k8s-guardian        # or k8s-guardian-fix
        files: ^k8s/.*\.ya?ml$
```
