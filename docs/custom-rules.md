# Custom rules

With custom rules you can enforce your organisation's policies without writing Rego or Kyverno templates. Write them by hand, or let Claude draft them (`rule create` is [experimental](ai.md#policies-from-plain-language-experimental): review what it generates):

```bash
k8s-guardian rule create "Every Deployment needs a team label and at most 4 CPUs per container"
k8s-guardian rule create "..." --dry-run          # print only
k8s-guardian rule create "..." -f k8s/            # also test the new rules against your manifests
k8s-guardian rule validate                        # validate .k8s-guardian/rules
```

## Where rules are loaded from

1. `.k8s-guardian/rules/*.yaml` in the working directory (loaded automatically)
2. `$K8S_GUARDIAN_RULES`: a list of files or directories, separated like `$PATH`
3. `--rules <file|dir>` (repeatable)

Custom rules run in `check`, `check --fix --ai` (Claude fixes their violations), `interactive` and the MCP server. Skip them with `--skip` or the `k8s-guardian.io/ignore` annotation, like built-in rules.

## Format

```yaml
apiVersion: k8s-guardian.io/v1
kind: Rule
metadata:
  name: require-team-label          # kebab-case, used as the rule name
spec:
  id: ORG001                        # unique; IDs starting with KG are reserved
  severity: error                   # error | warning | info
  description: Every workload must declare its owning team.
  message: add metadata.labels.team # optional; defaults to the failing condition
  match:
    kinds: [Deployment, StatefulSet]   # optional; empty = every kind
    scope: resource                    # resource | pod | container
  when:                             # optional preconditions; rule is skipped if any is false
    - {path: metadata.namespace, op: notIn, values: [kube-system]}
  assert:                           # all must hold, the first failing one is reported
    - {path: metadata.labels.team, op: exists}
```

### Scope

| scope | paths are relative to | evaluated for |
|-------|----------------------|---------------|
| `resource` (default) | the whole object | every object (Services, ConfigMaps, workloads, CRs, …) |
| `pod` | the pod spec (`spec.template.spec`, CronJob's job template, …) | workloads |
| `container` | each container and init container | workloads |

### Paths

* dots between keys: `spec.template.metadata.labels`
* quoted keys for dots or slashes: `metadata.annotations['example.com/owner']`
* index: `ports[0].containerPort`
* every element: `volumes[*].name`. **All** elements must satisfy the condition. For `exists`, `equals`, `in` and `matches`, the list must also be non-empty. For the other operators, a missing or empty list passes.

### Operators

| op | meaning | missing field |
|----|---------|---------------|
| `exists` / `notExists` | field is present / absent | – |
| `equals` / `notEquals` | string equality (`value`) | fails / passes |
| `in` / `notIn` | one of `values` | fails / passes |
| `matches` / `notMatches` | Go RE2 regex (`value`) | fails / passes |
| `gt` `gte` `lt` `lte` | numeric comparison; values may be quantities (`500m`, `2Gi`) | passes |

Combine a comparison with `exists` to require the field too.

## Examples

See [`examples/rules/org-policies.yaml`](../examples/rules/org-policies.yaml): a contact email annotation, trusted registries, a CPU ceiling, and LoadBalancer source ranges.

## Enforcing rules in the cluster

`k8s-guardian export vap` translates custom rules (and most built-ins) into
Kubernetes ValidatingAdmissionPolicies with CEL expressions that have the
same semantics as the CLI engine. Resource-scoped rules need `match.kinds` to
be exportable. An e2e test suite runs every operator against a real API
server to make sure both agree.
