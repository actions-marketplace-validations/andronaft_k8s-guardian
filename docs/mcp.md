# MCP server (Claude Code, Cursor, ...)

`k8s-guardian mcp` runs a Model Context Protocol server over stdio, so coding agents can check their own Kubernetes YAML.

```bash
claude mcp add k8s-guardian -- k8s-guardian mcp
```

```json
{ "mcpServers": { "k8s-guardian": { "command": "k8s-guardian", "args": ["mcp"] } } }
```

| Tool | What it does |
|------|--------------|
| `validate_manifest` | Validate YAML, including your custom rules |
| `fix_manifest` | Fixed YAML plus remaining findings (`unsafe_fixes: true` for the riskier fixes, `use_ai: true` also uses Claude) |
| `check_live` | Check YAML against the live cluster (quotas, nodes, references, CRDs) |
| `diff_cluster` | Breaking changes compared with what is deployed |
| `estimate_cost` | Monthly cost of the workloads |
| `validate_custom_rule` | Validate and test an organisation rule; the agent can author policies without an API key |
| `export_admission_policies` | Built-in + custom rules as ValidatingAdmissionPolicies (CEL) for the cluster |
| `audit_cluster_resource` | Validate a live resource |
| `list_rules` | All rules |

Then ask your agent: *"Write a Deployment for the orders API, make sure it passes k8s-guardian, fits the prod quota and costs less than $100/month."*
