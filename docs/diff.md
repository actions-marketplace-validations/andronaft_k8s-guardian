# Breaking-change detection: `diff`

What breaks if you apply this over what is running now?

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

The diff compares only the fields you declare, so server defaults don't show up as noise. It detects:

* immutable selectors and fields: StatefulSet `volumeClaimTemplates`/`serviceName`, Job templates, Service `clusterIP`, PVC class and shrinking, immutable ConfigMaps and Secrets;
* selector/template mismatches;
* APIs removed in *your* cluster's version;
* Recreate downtime, scale-to-zero, removed containers and named ports;
* Service port changes and HPA conflicts;
* with `--server-side`: everything the API server would reject or change (see below).

Secret values are always shown as `(redacted)`.

## Asking the API server: `--server-side`

By default the comparison happens on the client, so it can't see what the API server adds or refuses: defaulting, mutating webhooks and policies (sidecar injection, for example), and validation and admission that only run on the server. `--server-side` sends every object to the API server as a server-side apply **dry run** as well. Nothing is stored.

```bash
k8s-guardian diff -f deploy.yaml -n prod --server-side
```

```text
~ Deployment/web [shop]  1 change(s)
    spec.template.spec.containers[proxy]: <none> → image: r.io/proxy:2.0 … (by the API server)
  ℹ info    DF007  the API server changes the pod template (spec.template.spec.containers[proxy]): applying triggers a rolling restart
+ Deployment/latest [shop]  new (will be created)
  ✖ error   DF008  the API server rejects it in a dry run: ValidatingAdmissionPolicy 'k8s-guardian-kg010-pinned-image-tag-workloads'
                   with binding '…' denied request: …: apply will fail
```

* **DF008** reports objects the API server rejects: invalid or immutable fields, fields the schema doesn't have, and denials by admission policies, webhooks, quotas or Pod Security. Applying them would fail the same way. For Secrets, the server's message is not shown, because it can contain secret values.
* Fields the server would change beyond the ones you declare are added to the diff, marked *(by the API server)*. If they change a workload's pod template, applying it restarts the pods even though the manifest didn't change.
* The dry run runs as a server-side apply with its own field manager (`k8s-guardian`) and `--force-conflicts`, so it previews your values even where another field manager owns a field. Fields you remove from a manifest are still reported by the client-side comparison.
* It needs the `patch` permission on each resource, like `kubectl apply --dry-run=server`. When RBAC doesn't allow it, the namespace or CRD doesn't exist yet (for example, because the same manifest set creates it), or a webhook doesn't support dry runs, the object is skipped with a note on stderr and is not reported as a finding.
* Webhooks only run in a dry run if they declare `sideEffects: None` or `NoneOnDryRun`, so other webhooks' changes don't show up.

**Limits.** Without `--server-side`, changes that defaulting, mutating webhooks or policies would make are not taken into account, and server-only validation is not run.
