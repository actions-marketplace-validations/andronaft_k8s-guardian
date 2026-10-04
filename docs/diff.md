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
* Service port changes and HPA conflicts.

Secret values are always shown as `(redacted)`.

**Limits.** The comparison happens on the client. Changes that mutating webhooks or server-side defaulting would make are not taken into account. For an exact preview of what the API server will store, also run `kubectl diff --server-side`.
