# Interactive review: `interactive`

> **Experimental.** The keys and layout may still change.

```bash
k8s-guardian interactive -f deploy.yaml [--ai]     # or: check -f deploy.yaml -i
```

```text
◆ k8s-guardian interactive fix  deploy.yaml
╭─────────────────────────────────────────────────╮╭─────────────────────────────────────────────────────────────────────╮
│ Deployment/web                                  ││ ERROR KG002 memory-limit                                            │
│ ✔ KG013 securityContext.seccompProfile is not…  ││ container "nginx": missing resources.limits.memory                  │
│ ✗ KG001 [nginx] missing resources.requests (c…  ││ Containers must declare a memory limit to avoid starving the node … │
│ ▶ KG002 [nginx] missing resources.limits.memo…  ││                                                                     │
│ ● KG003 [nginx] securityContext.runAsNonRoot …  ││   …                                                                 │
│ ● KG006 [nginx] securityContext.readOnlyRootF…  ││                 containerPort: 8080                                 │
│ ○ KG015 spec.replicas is 1                      ││             securityContext:                                        │
│ ○ KG011 hostNetwork enabled                     ││               privileged: true                                      │
│ ○ KG014 automountServiceAccountToken is not f…  ││ +           resources:                                              │
│ ○ KG004 [nginx] securityContext.privileged is…  ││ +             limits:                                               │
│ ○ KG008 [nginx] missing livenessProbe           ││ +               memory: 256Mi                                       │
╰─────────────────────────────────────────────────╯╰─────────────────────────────────────────────────────────────────────╯
[y] accept  [n] skip  [e] edit  [a] accept all rule fixes  [↑↓] next/prev  [J/K] scroll  [q] save & quit  [ctrl+c] abort   2/12 reviewed
```

* **Markers.** ✔ accepted, ✗ skipped, ▶ current, ● a deterministic fix waiting for review, ○ needs a manual (or `--ai`) fix.
* **Diffs.** Each fix is shown as a diff against the *current* state, so the effect of fixes you already accepted is included.
* **Unsafe fixes.** Every deterministic fix is offered, including the ones `--fix` only applies with `--unsafe-fixes`. Those are marked "⚠ can change how the workload runs", so review them before accepting.
* **Editing.** `[e]` opens the proposal in `$EDITOR`.
* **AI.** With `--ai`, Claude proposes a fix per resource for everything the rules can't fix, and shows its reasoning (see [AI features](ai.md)).
