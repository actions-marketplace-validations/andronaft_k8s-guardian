# Cost estimation and right-sizing: `cost`

> **Experimental.** The estimate multiplies resource requests by a flat price per vCPU-hour and GiB-hour. It doesn't know your node types, spot pricing, bin-packing or discounts. Use it to compare workloads and spot over-provisioning, not as a bill. For real cost allocation, use OpenCost or your cloud provider's tools.

```bash
k8s-guardian cost -f k8s/                       # offline estimate
k8s-guardian cost -f k8s/ --usage -n prod       # + real usage from metrics-server (kubectl top)
k8s-guardian cost -f k8s/ --prometheus http://prometheus:9090 -n prod   # p95 CPU / peak memory over 7d
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

(The advice block above is an example of the output format.)

## Real usage

* **`--usage`** compares requests with what the pods consume right now (`kubectl top`, averaged per container). It suggests right-sized requests with 2× CPU and 1.5× memory headroom and prices the difference. It is a point-in-time snapshot, so check your peak load before cutting requests.
* **`--prometheus`** uses the **p95 of CPU and the peak memory working set over `--window` (default 7d)** from cAdvisor metrics, with smaller headroom (1.25× CPU, 1.2× memory). Set `K8S_GUARDIAN_PROMETHEUS_TOKEN` for authenticated endpoints.
* **Pod matching.** Pods are matched to workloads by name. Generated suffixes such as Deployment hashes use Kubernetes' vowel-free alphabet, so `orders-api-worker-…` is never counted as `orders-api`.
* **With `--ai`,** Claude also gets the observed usage, which makes its recommendations much more accurate.

## Prices

The defaults are $0.0316 per vCPU-hour and $0.0042 per GiB-hour, which approximate on-demand general-purpose nodes. Set your own with `--cpu-hour`/`--gib-hour` or `K8S_GUARDIAN_CPU_HOUR`/`K8S_GUARDIAN_GIB_HOUR`.
