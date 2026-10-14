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
| LV003 | Some node the pod can be scheduled on can fit its requests (and has them free: a warning otherwise). Like the scheduler, it skips cordoned nodes, nodes that don't match `nodeSelector` or required node affinity, and nodes with a `NoSchedule`/`NoExecute` taint the pod doesn't tolerate, and the message names which nodes were excluded and why |
| LV004 | Enough ResourceQuota is left for the *delta* against what is already running, and quota-mandated requests/limits are set (LimitRange defaults are taken into account) |
| LV005 | Requests/limits within LimitRange min/max |
| LV006 | Referenced ConfigMaps, Secrets, ServiceAccounts, PVCs and PriorityClasses exist in the cluster or in the manifest set |
| LV007 | StorageClass exists, or the cluster has a default one |

Everything is read-only (`kubectl get`). Checks that RBAC doesn't allow are skipped with a note.

```text
✖ error   LV003  pod requests 8 CPU but the largest matching node (node-a) has 3920m allocatable;
                 nodes with enough room are excluded (taint dedicated=gpu:NoSchedule: gpu-1): pods will stay Pending
```

DaemonSet pods tolerate cordoned and not-ready nodes, as the DaemonSet controller adds those tolerations. Pods with `nodeName` bypass the scheduler, so for them only `NoExecute` taints count. A DaemonSet that no node accepts is a warning ("runs no pods"), because it creates no pods rather than Pending ones.

When a pod fits a node's allocatable resources but no candidate node has that much **free** after the requests of the pods already running on it, LV003 reports a **warning**: the pods stay Pending until capacity is freed, or the cluster autoscaler adds a node. This reads the pods of all namespaces (`kubectl get pods -A`, read-only); without that permission the check is skipped with a note. Updates of existing StatefulSets and DaemonSets are not checked, because they replace each pod in place.

**Limits.** LV003 checks that one pod fits, not that all replicas do, and doesn't model preferred affinity, pod affinity and anti-affinity, or topology spread. "Fits" means "a node the pod may be scheduled on has room for it", not "the scheduler will place every replica now". Fields it can't evaluate (a toleration or affinity operator it doesn't know, `matchFields` other than `metadata.name`) count as matching and are reported in a note, so a node is never excluded by mistake.
