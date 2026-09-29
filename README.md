# 🛡️ k8s-guardian

> **AI-powered Kubernetes guardrails that know your cluster**: a CLI, a `kubectl` plugin and an MCP server in one binary.

[![CI](https://github.com/andronaft/k8s-guardian/actions/workflows/ci.yml/badge.svg)](https://github.com/andronaft/k8s-guardian/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Most Kubernetes linters are static: they read a YAML file and print an error. `k8s-guardian` also answers the questions you hit **at deploy time**:

* *"The YAML is valid, but will it actually run in `prod`?"* Quotas, node sizes, LimitRanges, missing Secrets, CRDs.
* *"What breaks if I apply this over what's running now?"* Immutable selectors, removed APIs, downtime, port changes.
* *"How much will this cost per month, and is 4 CPU really needed for a Go service?"*
* *"Can you just fix it?"* Deterministic fixes, Claude for the rest, and an interactive review TUI.
* *"How do I enforce our own policy without learning Rego?"* Describe it in plain language and get a rule.

## ✨ Features

| | Feature | Command |
|---|---|---|
| 🔍 | **17 built-in guardrails**: requests/limits, non-root, privilege escalation, capabilities, probes, `:latest`, host namespaces, seccomp, Service→Pod port wiring, removed APIs | `check -f` |
| 🔧 | **Deterministic auto-fix**: idempotent and comment-preserving | `check --fix` |
| 🤖 | **AI auto-fix**: Claude fixes probes, image pinning and custom-rule violations, and leaves `TODO` comments instead of guessing | `check --fix --ai` |
| 🖥️ | **Interactive review TUI**: finding on the left, colored diff on the right, `[y]` accept `[n]` skip `[e]` edit | `interactive -f` |
| 🌐 | **Live cluster context**: ResourceQuota headroom, node capacity with nodeSelector, LimitRanges, missing ConfigMaps/Secrets/SAs/PVCs, CRDs, StorageClasses | `check --live` |
| 🔀 | **Breaking-change detector**: compares with what is deployed, flags immutable fields, selector drift, removed APIs for *your* cluster version, Recreate downtime, HPA conflicts | `diff -f` |
| 💰 | **Cost estimation + AI right-sizing**: $/month per workload (HPA-aware), and Claude recommends requests per workload type with the savings | `cost -f [--ai]` |
| 📜 | **Policies from plain language**: *"forbid :latest and require a contact email"* becomes a validated rule | `rule create "…"` |
| 🔌 | **kubectl plugin** | `kubectl guard …` |
| 🧠 | **MCP server** for Claude Code / Cursor: validate, fix, live-check, diff, cost, custom rules | `mcp` |
| ⚡ | **GitHub Action & CI**: inline PR annotations, job summary, SARIF, exit codes, pre-commit hooks | `uses: andronaft/k8s-guardian@v0.3.0` |

---

## 📦 Installation

```bash
# Go
go install github.com/andronaft/k8s-guardian/cmd/k8s-guardian@latest

# From source (also creates the kubectl-guard plugin symlink)
git clone https://github.com/andronaft/k8s-guardian && cd k8s-guardian
make build && sudo make install

# Docker (multi-arch, published to GHCR on every release)
docker run --rm -v "$PWD:/work" ghcr.io/andronaft/k8s-guardian check -f .

# Krew (once published to krew-index)
kubectl krew install guard
```

## 🚀 Quick tour

### 1. Validate and fix

```bash
k8s-guardian check -f k8s/                      # files, directories, Helm charts, stdin (-f -)
k8s-guardian check -f deploy.yaml --fix         # deterministic fixes, in place, comments kept
k8s-guardian check -f deploy.yaml --fix --ai    # + Claude for the rest (needs ANTHROPIC_API_KEY)
```

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

### 2. Review fixes interactively

```bash
k8s-guardian interactive -f deploy.yaml [--ai]     # or: check -f deploy.yaml -i
```

```text
◆ k8s-guardian interactive fix  deploy.yaml
╭────────────────────────────────────────────────────────╮╭─────────────────────────────────────────────────────────────╮
│ Deployment/web                                         ││ ERROR KG002 memory-limit                                    │
│ ✔ KG011 hostNetwork enabled                            ││ container "nginx": missing resources.limits.memory          │
│ ✗ KG013 securityContext.seccompProfile is not set to…  ││ Containers must declare a memory limit to avoid starving …  │
│ ✔ KG001 [nginx] missing resources.requests (cpu, mem…  ││                                                             │
│ ▶ KG002 [nginx] missing resources.limits.memory        ││   …                                                         │
│ ● KG003 [nginx] securityContext.runAsNonRoot is not …  ││             securityContext:                                │
│ ● KG004 [nginx] securityContext.privileged is true     ││               privileged: true                              │
│ ○ KG008 [nginx] missing livenessProbe                  ││ +           resources:                                      │
│ ○ KG010 [nginx] image "nginx:latest" uses the mutabl…  ││ +             limits:                                       │
│                                                        ││ +               memory: 128Mi                               │
╰────────────────────────────────────────────────────────╯╰─────────────────────────────────────────────────────────────╯
[y] accept  [n] skip  [e] edit  [a] accept all rule fixes  [↑↓] next/prev  [J/K] scroll  [q] save & quit  [ctrl+c] abort
```

Each fix is shown as a real diff against the *current* state, so the effect of fixes you already accepted is included. `[e]` opens the proposal in `$EDITOR`. With `--ai`, Claude proposes a fix per resource for everything the rules can't fix, and shows its reasoning.

### 3. Will it actually run there? `--live`

```bash
k8s-guardian check -f deploy.yaml --live            # uses your current kubectl context
kubectl guard -f deploy.yaml --live -n prod
```

```text
examples/costly-app.yaml  Deployment/orders-api
  ✖ error   LV005  container "api": requests.cpu 4 exceeds LimitRange "shop-limits" max 2
  ✖ error   LV003  pod requests 4 CPU but the largest matching node (node-a) has 3920m allocatable: pods will stay Pending
  ✖ error   LV004  needs requests.cpu=15 more (4 replica(s)) but ResourceQuota "shop-compute" in namespace "shop"
                   only has 9 left (hard 12, used 3): pods will be rejected
```

| ID | Check |
|----|-------|
| LV001 | apiVersion served by the cluster (CRD installed? API removed in this version?) |
| LV002 | Namespace exists, or is created by the same manifest set |
| LV003 | Some schedulable node matching `nodeSelector` can fit the pod's requests |
| LV004 | Enough ResourceQuota is left for the *delta* against what is already running, and quota-mandated requests/limits are set (LimitRange defaults are taken into account) |
| LV005 | Requests/limits within LimitRange min/max |
| LV006 | Referenced ConfigMaps, Secrets, ServiceAccounts, PVCs and PriorityClasses exist in the cluster or in the manifest set |
| LV007 | StorageClass exists, or the cluster has a default one |

Everything is read-only (`kubectl get`). Checks that RBAC doesn't allow are skipped with a note.

### 4. What breaks when I apply this? `diff`

```bash
k8s-guardian diff -f deploy.yaml -n prod
```

```text
Comparing local manifests with the cluster (v1.29.4)

~ Deployment/orders-api [shop]  6 change(s)
    spec.replicas: 2 → 4
    spec.selector.matchLabels.app: orders → orders-api
    spec.template.spec.containers[api].image: ghcr.io/example/orders-api:2.2.0 → ghcr.io/example/orders-api:2.3.1
  ✖ error   DF001  spec.selector is immutable (live: matchLabels: {app: orders}): apply will fail; delete and recreate
                   (downtime) or deploy under a new name and migrate traffic
  ⚠ warning DF006  spec.replicas (4) is set but HorizontalPodAutoscaler "orders-api" manages this workload: every apply resets it
  ℹ info    DF007  pod template changed: triggers a rolling restart of 4 pod(s)
+ HorizontalPodAutoscaler/orders-api [shop]  new (will be created)
~ Service/orders-api [shop]  1 change(s)
    spec.ports[0].port: 443 → 80
  ⚠ warning DF005  Service port 443 is removed: existing clients using the old port will break
```

The diff compares only the fields you declare, so server defaults don't show up as noise. It detects immutable selectors and fields (StatefulSet `volumeClaimTemplates`/`serviceName`, Job templates, Service `clusterIP`, PVC class and shrinking, immutable ConfigMaps and Secrets), selector/template mismatches, APIs removed in *your* cluster's version, Recreate downtime, scale-to-zero, removed containers and named ports, Service port changes and HPA conflicts.

### 5. What does it cost? `cost`

```bash
k8s-guardian cost -f k8s/                       # offline estimate
k8s-guardian cost -f k8s/ --ai                  # + Claude right-sizing with savings
k8s-guardian cost -f k8s/ --ai --apply          # write the recommendations into the files
```

```text
WORKLOAD                                 REPLICAS  CPU/POD   MEM/POD   $/MONTH
Deployment/orders-api (shop)             4-10      4         8Gi       $467.20–$1168.00
    ↳ container "api" requests 4 CPU: make sure it really uses it (run `cost --ai` for advice)
    ↳ scaled by an HPA between 4 and 10 replicas

💡 Claude's right-sizing advice
  Deployment/orders-api container "api" (Go HTTP service)
    cpu 4, memory 8Gi  →  cpu 500m, memory 512Mi (limit 1Gi): saves ~$414.93/mo
```

(The advice block is an example of the output format.) Prices default to $0.0316 per vCPU-hour and $0.0042 per GiB-hour, which approximate on-demand general-purpose nodes. Set your own with `--cpu-hour`/`--gib-hour` or `K8S_GUARDIAN_CPU_HOUR`/`K8S_GUARDIAN_GIB_HOUR`. Only image names, ports, env var **names**, commands and resources are sent to Claude, never env values or Secrets.

### 6. Company policies in plain language: `rule create`

```bash
k8s-guardian rule create "Forbid :latest image tags and require a contact email annotation on every workload"
```

Claude writes rules in a small declarative format ([docs/custom-rules.md](docs/custom-rules.md)). k8s-guardian compiles them and sends any validation errors back to Claude for a correction, then saves them to `.k8s-guardian/rules/`. From then on they are enforced by `check`, `--fix --ai`, the TUI and the MCP server. You can also write rules by hand:

```yaml
apiVersion: k8s-guardian.io/v1
kind: Rule
metadata: {name: public-lb-needs-source-ranges}
spec:
  id: ORG004
  severity: error
  description: LoadBalancer Services must restrict source IP ranges.
  match: {kinds: [Service]}
  when:   [{path: spec.type, op: equals, value: LoadBalancer}]
  assert: [{path: spec.loadBalancerSourceRanges, op: exists}]
```

See [`examples/rules/`](examples/rules) for more.

---

## 📏 Built-in rules

| ID | Name | Severity | Fix |
|----|------|----------|-----|
| KG001 | `resource-requests`: CPU and memory requests set | error | auto |
| KG002 | `memory-limit`: memory limit set | error | auto |
| KG003 | `run-as-non-root` | error | auto |
| KG004 | `no-privileged` | error | auto |
| KG005 | `no-privilege-escalation` | warning | auto |
| KG006 | `read-only-root-filesystem` | warning | auto |
| KG007 | `drop-all-capabilities` | warning | auto |
| KG008 | `liveness-probe` (not for Jobs, CronJobs, init containers) | warning | AI |
| KG009 | `readiness-probe` (not for Jobs, CronJobs, init containers) | warning | AI |
| KG010 | `pinned-image-tag`: no `:latest` or untagged images | error | AI |
| KG011 | `no-host-namespaces` | error | auto |
| KG012 | `no-host-path` | warning | AI |
| KG013 | `seccomp-profile` | warning | auto |
| KG014 | `automount-service-account-token` | info | AI |
| KG015 | `high-availability`: at least 2 replicas | info | AI |
| KG016 | `service-target-port`: Service selector matches a workload and targetPort matches its containerPort | warning | AI |
| KG017 | `removed-api-version`: e.g. `extensions/v1beta1`, `batch/v1beta1` CronJob, `autoscaling/v2beta2` | error | AI |

To ignore rules for one resource, add `k8s-guardian.io/ignore: "KG011,no-host-path"` to its annotations. To skip them globally, use `--skip`. `k8s-guardian rules` lists every rule, including custom, live and diff rules.

## 🔌 kubectl plugin

Any executable named `kubectl-<name>` in `$PATH` is a kubectl plugin. `make install` symlinks `kubectl-guard`:

```bash
kubectl guard deployment/my-app -n prod          # audit a live resource
kubectl guard -f deployment.yaml --fix           # check + fix
kubectl guard diff -f deployment.yaml -n prod    # every command works
```

## 🧠 MCP server (Claude Code, Cursor, ...)

```bash
claude mcp add k8s-guardian -- k8s-guardian mcp
```

```json
{ "mcpServers": { "k8s-guardian": { "command": "k8s-guardian", "args": ["mcp"] } } }
```

| Tool | What it does |
|------|--------------|
| `validate_manifest` | Validate YAML, including your custom rules |
| `fix_manifest` | Fixed YAML plus remaining findings (`use_ai: true` also uses Claude) |
| `check_live` | Check YAML against the live cluster (quotas, nodes, references, CRDs) |
| `diff_cluster` | Breaking changes compared with what is deployed |
| `estimate_cost` | Monthly cost of the workloads |
| `validate_custom_rule` | Validate and test an organisation rule; the agent can author policies without an API key |
| `audit_cluster_resource` | Validate a live resource |
| `list_rules` | All rules |

Then ask your agent: *"Write a Deployment for the orders API, make sure it passes k8s-guardian, fits the prod quota and costs less than $100/month."*

## 🤖 About the AI features

* They use the official Anthropic Go SDK with `claude-opus-5-5` by default. Override the model with `--model` or `K8S_GUARDIAN_MODEL`. Requests are streamed and use server-side refusal fallback, and rule generation and right-sizing use structured JSON outputs.
* Deterministic fixes always run first. Claude only gets what rules can't solve, and its output is re-parsed, re-fixed and re-validated.
* Claude is told never to invent image versions, hosts or secrets. Where it can't decide safely, it leaves a `# TODO(k8s-guardian):` comment. **Review AI changes before applying them.**
* Credentials come from `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN` or an `ant auth login` profile. Nothing is sent unless you pass `--ai`, `rule create` or `use_ai`.

## ⚙️ GitHub Action

Findings appear as **annotations directly on the PR diff**, together with a Markdown job summary (and an optional cost estimate):

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
      - uses: andronaft/k8s-guardian@v0.3.0
        with:
          path: k8s/ charts/my-app     # files, directories or Helm charts
          fail-on: error               # error | warning | info
          skip: KG015                  # optional
          cost: "true"                 # add $/month to the job summary
```

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
| `args` | | Extra arguments for `k8s-guardian check` |

Outputs: `errors`, `warnings`, `infos`, `fixable`, `sarif-file`.

<details>
<summary>Code scanning (SARIF) and pre-deploy checks</summary>

```yaml
permissions:
  contents: read
  security-events: write
steps:
  - uses: actions/checkout@v7
  - uses: andronaft/k8s-guardian@v0.3.0
    with:
      path: k8s/
      sarif-file: k8s-guardian.sarif
  - uses: github/codeql-action/upload-sarif@v4
    if: always()
    with:
      sarif_file: k8s-guardian.sarif
```

With cluster credentials in the job, you can block breaking changes before deploying:

```yaml
  - uses: andronaft/k8s-guardian@v0.3.0        # also puts k8s-guardian on PATH
    with: {path: k8s/, live: "true", namespace: prod}
  - run: k8s-guardian diff -f k8s/ -n prod --format github
```
</details>

Outside GitHub, use `--format github|markdown|sarif|json` in any CI. There is also a pre-commit hook:

```yaml
# .pre-commit-config.yaml
repos:
  - repo: https://github.com/andronaft/k8s-guardian
    rev: v0.3.0
    hooks:
      - id: k8s-guardian        # or k8s-guardian-fix
        files: ^k8s/.*\.ya?ml$
```

Exit codes: `0` passed, `1` findings at or above `--fail-on` (default `error`), `2` usage or runtime error.

## 🏗️ Architecture

```text
cmd/k8s-guardian     one binary: CLI, kubectl-guard plugin, MCP server
action.yml           GitHub Action (composite, builds from source)
internal/rules       rule engine + 17 built-in rules and deterministic fixes
internal/custom      declarative custom-rule DSL (paths, when/assert, ops)
internal/live        cluster-aware checks (quota, nodes, LimitRange, references)
internal/diff        structural diff + breaking-change analysis
internal/cost        cost model, HPA-aware estimates, right-sizing
internal/ai          Claude: fixes, rule generation, right-sizing (structured outputs)
internal/tui         Bubble Tea review UI
internal/cluster     read-only kubectl adapter (cached) + fake for tests
internal/mcp         MCP stdio server
```

## 🗺️ Roadmap

- [x] v0.1: CLI validator, `--fix`, AI fix, kubectl plugin
- [x] v0.2: MCP server
- [x] v0.3: live cluster context, interactive TUI, breaking-change diff, cost estimation, natural-language rules, GitHub Action
- [ ] Real usage data for right-sizing (metrics-server / Prometheus)
- [ ] Kustomize support (`-k`)
- [ ] Export custom rules to Kyverno / ValidatingAdmissionPolicy (CEL)
- [ ] Admission webhook mode

## 🛠️ Development

```bash
make test    # unit tests (fake cluster + mocked Claude API, no credentials needed)
make lint    # go vet + gofmt
make build   # bin/k8s-guardian + bin/kubectl-guard
```

Releases: push a `v*` tag. GoReleaser builds the binaries, and krew-release-bot updates the Krew index after the first manual submission of [`.krew.yaml`](.krew.yaml).

## 📄 License

[Apache 2.0](LICENSE)
