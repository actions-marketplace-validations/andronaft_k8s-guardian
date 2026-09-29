# Security policy

## Reporting a vulnerability

Please **do not** open a public issue. Report it privately through
[GitHub Security Advisories](https://github.com/andronaft/k8s-guardian/security/advisories/new).
You should get an answer within a few days.

## Scope and design notes

- k8s-guardian only **reads** from clusters (`kubectl get`, `kubectl top`).
  It never applies changes; `--fix` edits local files or prints YAML.
- AI features (`--ai`, `rule create`, `use_ai` in MCP) send manifests to the
  Claude API only when explicitly requested. `cost --ai` sends image names,
  ports, env var *names*, commands and resources, never env values or
  Secrets.
- The Docker image runs as a non-root user on a distroless base.
