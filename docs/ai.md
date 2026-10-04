# AI features

All AI features are optional. Nothing is sent to an API unless you pass `--ai`, run `rule create` or set `use_ai` in the MCP server.

| Feature | Command | Status |
|---------|---------|--------|
| Fix findings that have no deterministic fix (probes, image pinning, custom rules) | `check --fix --ai` | stable |
| Fix proposals in the review TUI | `interactive --ai` | experimental, see [interactive review](interactive.md) |
| Right-sizing with savings | `cost --ai` | experimental, see [cost](cost.md) |
| Policies from plain language | `rule create "…"` | experimental |

## How it works

* They use the official Anthropic Go SDK with `claude-opus-5-5` by default. Override the model with `--model` or `K8S_GUARDIAN_MODEL`. Requests are streamed and use server-side refusal fallback, and rule generation and right-sizing use structured JSON outputs.
* Deterministic fixes always run first. Claude only gets what rules can't solve, and its output is re-parsed, re-fixed and re-validated.
* An AI fix is rejected if it adds, drops or renames a resource, or introduces a new error that can't be fixed automatically. That protects against prompt injection hidden in a manifest.
* Files containing a Secret are never sent. For cost advice, only image names, ports, env var **names**, commands and resources are sent, never env values.
* Claude is told never to invent image versions, hosts or secrets. Where it can't decide safely, it leaves a `# TODO(k8s-guardian):` comment. **Review AI changes before applying them.**
* Credentials come from `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` or an `ant auth login` profile.

## Policies from plain language (experimental)

```bash
k8s-guardian rule create "Forbid :latest image tags and require a contact email annotation on every workload"
```

Claude writes rules in the [custom rule format](custom-rules.md). k8s-guardian compiles them and sends any validation errors back to Claude for a correction, then saves them to `.k8s-guardian/rules/`. Read the generated rule before you commit it: it is a draft, not a reviewed policy. Rules can always be written by hand.
