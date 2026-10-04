# Live cluster checks: `--live`

The YAML is valid, but will it actually run in `prod`?

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

**Limits.** LV003 looks at allocatable resources and `nodeSelector`. It does not model taints and tolerations, affinity, topology spread or how full each node already is, so "fits" means "a matching node is big enough", not "the scheduler will place it".
