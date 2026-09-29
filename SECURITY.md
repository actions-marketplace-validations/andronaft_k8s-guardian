# Security policy

## Reporting a vulnerability

Please **do not** open a public issue. Report it privately through
[GitHub Security Advisories](https://github.com/andronaft/k8s-guardian/security/advisories/new).
You should get an answer within a few days.

## Threat model

k8s-guardian is often run on **untrusted input**: manifests from pull
requests (CI, the GitHub Action), requests from AI agents (MCP) and answers
from an LLM (`--ai`). It is designed so that none of these can escalate:

| Input | Protection |
|-------|------------|
| Manifest values that reach `kubectl` (names, namespaces, labels, kinds) | Validated against Kubernetes naming rules before any call; values are passed as `--flag=value` or `kind/name`, never as bare arguments. A manifest can't inject flags such as `--server=` (which would send your credentials elsewhere). |
| MCP tool arguments (`audit_cluster_resource`, `check_live`, `diff_cluster`) | Same validation; the MCP server only runs read-only `kubectl get/top`. |
| Secrets | Never sent to the Claude API (`--ai` refuses files with a `Secret`, the TUI skips them). `diff` compares Secret data but prints `(redacted)`, and `audit` drops Secret data. |
| LLM answers (`--fix --ai`, TUI, `rule create`) | The fixed manifest must contain exactly the same resources, must not introduce new errors (prompt-injection guard), and is re-validated. Generated rule names are restricted to DNS labels, so they can't write files outside the rules directory. |
| Unrelated/broken YAML in a repository | Skipped with a warning while walking directories (a broken Kubernetes manifest still fails). Aliases are not expanded, so YAML bombs are harmless. |

## Design notes

- k8s-guardian only **reads** from clusters (`kubectl get`, `kubectl top`).
  It never applies changes; `--fix` edits local files or prints YAML.
- AI features (`--ai`, `rule create`, `use_ai` in MCP) send data to the
  Claude API only when explicitly requested. `cost --ai` sends image names,
  ports, env var *names*, commands and resources, never env values.
  Note that `--fix --ai` sends the manifest itself, including literal env
  values; keep credentials in Secrets.
- Kustomize and Helm are rendered with `kustomize build` / `kubectl
  kustomize` / `helm template` without plugins or hooks enabled.
- Release workflows pin third-party actions to commit SHAs; CI runs
  govulncheck, staticcheck and the e2e suite on every push.
- The Docker image runs as a non-root user on a distroless base.
