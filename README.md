# 🛡️ k8s-guardian

> **AI-powered Kubernetes guardrails**: a CLI, a `kubectl` plugin and an MCP server in one binary.

[![CI](https://github.com/andronaft/k8s-guardian/actions/workflows/ci.yml/badge.svg)](https://github.com/andronaft/k8s-guardian/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

`k8s-guardian` checks Kubernetes manifests and Helm charts against security, resource and reliability standards. It doesn't stop at pointing out errors. It fixes most of them for you and keeps your YAML comments. For the rest, it can ask Claude to write the fix. It also runs as a `kubectl` plugin and as an MCP server for Claude Code and Cursor.

### ✨ Key Features
* 🔍 **Smart Validation:** Audits manifests for missing requests/limits, root users, privilege escalation, capabilities, probes, `:latest` images, host namespaces, seccomp and more.
* 🔧 **Auto-Fix (`--fix`):** Deterministic, idempotent fixes that keep your structure, key order and comments.
* 🤖 **AI Auto-Fix (`--fix --ai`):** Claude fixes what rules alone can't, such as probes on the right port, emptyDir mounts for read-only filesystems and `TODO` comments for decisions only you can make.
* 🔌 **`kubectl` Plugin:** Fits into your cluster workflow (`kubectl guard deployment/my-app -n prod`).
* 🧠 **MCP Server:** Connects to Claude Code and Cursor so that AI-generated YAML follows the guardrails.
* ⚡ **Pre-commit & CI/CD ready:** A single static binary, exit codes for pipelines, and JSON and SARIF output for GitHub code scanning.

---

## 📦 Installation

```bash
# Go
go install github.com/andronaft/k8s-guardian/cmd/k8s-guardian@latest

# From source (also creates the kubectl-guard plugin symlink)
git clone https://github.com/andronaft/k8s-guardian && cd k8s-guardian
make build && sudo make install

# Krew (once published to krew-index)
kubectl krew install guard
```

Prebuilt binaries for Linux, macOS and Windows are attached to every [release](https://github.com/andronaft/k8s-guardian/releases).

## 🚀 Usage

```bash
# Validate files, directories, Helm charts (rendered via `helm template`) or stdin
k8s-guardian check -f deployment.yaml
k8s-guardian check -f k8s/ -f charts/my-app
helm template ./chart | k8s-guardian check -f -

# Auto-fix in place (rule-based, offline)
k8s-guardian check -f deployment.yaml --fix

# Let Claude fix the remaining findings (probes, image tags, ...)
export ANTHROPIC_API_KEY=sk-ant-...
k8s-guardian check -f deployment.yaml --fix --ai

# Print fixed YAML instead of rewriting the file
k8s-guardian check -f deployment.yaml --fix --stdout > fixed.yaml

# Audit live resources in the cluster (uses your kubectl context)
k8s-guardian audit deployment/my-app -n default
k8s-guardian audit deployments,statefulsets -A --format json
k8s-guardian audit deployment/my-app -n prod --fix | kubectl apply -f -   # review first!

# List rules
k8s-guardian rules
```

Example output:

```text
examples/insecure-deployment.yaml  Deployment/web
  ✖ error   KG011  hostNetwork enabled [fixable]
  ✖ error   KG001  container "nginx": missing resources.requests (cpu, memory) [fixable] (line 19)
  ✖ error   KG004  container "nginx": securityContext.privileged is true [fixable] (line 19)
  ⚠ warning KG008  container "nginx": missing livenessProbe (line 19)
  ✖ error   KG010  container "nginx": image "nginx:latest" uses the mutable :latest tag (line 19)
  ...
6 error(s), 6 warning(s), 2 info
9 issue(s) can be fixed automatically with --fix (add --ai to let Claude fix the rest)
```

### Flags

| Flag | Description |
|------|-------------|
| `-f, --filename` | File, directory, Helm chart or `-` for stdin (repeatable) |
| `--fix` | Apply fixes (in place for files, to stdout for stdin, Helm charts and `audit`) |
| `--ai` | With `--fix`, use Claude to fix findings that have no deterministic fix |
| `--model` | Claude model for `--ai` (default `claude-opus-5-5`, or `$K8S_GUARDIAN_MODEL`) |
| `--stdout` | With `--fix`, print fixed YAML instead of rewriting files |
| `--format, -o` | `text` (default), `json`, `sarif` |
| `--fail-on` | Exit 1 when findings at or above `error` (default), `warning` or `info` remain |
| `--skip` | Comma-separated rule IDs or names to skip, e.g. `KG006,liveness-probe` |
| `-n, -A, --context` | Namespace, all namespaces and kube context for `audit` |

Exit codes: `0` passed, `1` findings at or above `--fail-on`, `2` usage or runtime error.

## 📏 Rules

| ID | Name | Severity | Fix |
|----|------|----------|-----|
| KG001 | `resource-requests`: CPU and memory requests set | error | auto |
| KG002 | `memory-limit`: memory limit set | error | auto |
| KG003 | `run-as-non-root`: `runAsNonRoot: true` | error | auto |
| KG004 | `no-privileged`: no privileged containers | error | auto |
| KG005 | `no-privilege-escalation`: `allowPrivilegeEscalation: false` | warning | auto |
| KG006 | `read-only-root-filesystem` | warning | auto |
| KG007 | `drop-all-capabilities`: `capabilities.drop: [ALL]` | warning | auto |
| KG008 | `liveness-probe` (not required for Jobs, CronJobs or init containers) | warning | AI |
| KG009 | `readiness-probe` (not required for Jobs, CronJobs or init containers) | warning | AI |
| KG010 | `pinned-image-tag`: no `:latest` and no untagged images | error | AI |
| KG011 | `no-host-namespaces`: no hostNetwork, hostPID or hostIPC | error | auto |
| KG012 | `no-host-path`: no hostPath volumes | warning | AI |
| KG013 | `seccomp-profile`: `RuntimeDefault` or `Localhost` | warning | auto |
| KG014 | `automount-service-account-token` disabled | info | AI |
| KG015 | `high-availability`: at least 2 replicas | info | AI |

Supported workloads: Pod, Deployment, StatefulSet, DaemonSet, ReplicaSet, ReplicationController, Job, CronJob, and `List` objects that contain them.

**Ignore rules for one resource** with an annotation:

```yaml
metadata:
  annotations:
    k8s-guardian.io/ignore: "KG011,no-host-path"   # e.g. a CNI DaemonSet
```

## 🔌 kubectl plugin

`kubectl` treats any executable named `kubectl-<name>` in your `$PATH` as a plugin. k8s-guardian detects when it runs as `kubectl-guard` and adapts its help output:

```bash
ln -s "$(which k8s-guardian)" /usr/local/bin/kubectl-guard   # `make install` does this for you
kubectl plugin list

kubectl guard deployment/my-app -n prod      # = audit
kubectl guard -f deployment.yaml --fix       # = check
```

## 🧠 MCP server (Claude Code, Cursor, ...)

`k8s-guardian mcp` speaks the Model Context Protocol over stdio and exposes these tools:

| Tool | What it does |
|------|--------------|
| `validate_manifest` | Validate YAML and return the findings |
| `fix_manifest` | Return the fixed YAML (`use_ai: true` also uses Claude) and the remaining findings |
| `audit_cluster_resource` | Validate a live resource via kubectl |
| `list_rules` | List all rules |

**Claude Code:**

```bash
claude mcp add k8s-guardian -- k8s-guardian mcp
# optional, for fix_manifest with use_ai:
claude mcp add k8s-guardian -e ANTHROPIC_API_KEY=sk-ant-... -- k8s-guardian mcp
```

**Cursor** (`.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "k8s-guardian": { "command": "k8s-guardian", "args": ["mcp"] }
  }
}
```

Then ask the agent: *"Write a Deployment for my API and make sure it passes k8s-guardian."*

## 🤖 How AI fix works

1. Deterministic rules fix everything they can, offline and idempotently.
2. The remaining findings (warning and above) are sent to Claude together with the manifest. By default this uses `claude-opus-5-5` through the official Anthropic Go SDK, with streaming and server-side refusal fallback.
3. The answer is parsed and validated as YAML. The deterministic fixes are applied again, the manifest is re-checked, and Claude's list of changes is printed.

Claude is told never to invent image versions, hosts or secrets. Where it can't decide safely, it leaves a `# TODO(k8s-guardian): ...` comment instead. **Always review AI changes before applying them to a cluster.** Only manifests you pass with `--ai` are sent to the API.

Credentials come from `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` or an `ant auth login` profile.

## ⚙️ CI/CD

**GitHub Actions with code scanning:**

```yaml
- run: go install github.com/andronaft/k8s-guardian/cmd/k8s-guardian@latest
- run: k8s-guardian check -f k8s/ --format sarif > k8s-guardian.sarif || true
- uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: k8s-guardian.sarif
- run: k8s-guardian check -f k8s/ --fail-on error
```

**pre-commit** (`.pre-commit-config.yaml`):

```yaml
repos:
  - repo: https://github.com/andronaft/k8s-guardian
    rev: v0.1.0
    hooks:
      - id: k8s-guardian        # or k8s-guardian-fix to fix in place
        files: ^k8s/.*\.ya?ml$
```

**Docker:**

```bash
docker build -t k8s-guardian .
docker run --rm -v "$PWD:/work" -w /work k8s-guardian check -f k8s/
```

## 🗺️ Roadmap

- [ ] Custom rules via a config file (`.k8s-guardian.yaml`) and Rego/CEL policies
- [ ] NetworkPolicy, PodDisruptionBudget and HPA coverage checks
- [ ] Kustomize support (`-k`)
- [ ] Admission webhook mode

## 🛠️ Development

```bash
make test    # unit tests
make lint    # go vet + gofmt
make build   # bin/k8s-guardian + bin/kubectl-guard
```

Releases: push a `v*` tag. GoReleaser builds the binaries and archives, and krew-release-bot updates the Krew index (after the first manual submission of [`.krew.yaml`](.krew.yaml) to [krew-index](https://github.com/kubernetes-sigs/krew-index)).

## 📄 License

[Apache 2.0](LICENSE)
