"""Render the charts in charts.txt (cloned by run.sh) with default values.

Dependencies that live in a remote repository are dropped unless a local
copy is known, so a few optional subcharts are not rendered.
"""
import os, shutil, subprocess, sys

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
H = os.environ.get("HELM", "helm")
extra = {
 "external-dns": ["--set","policy=upsert-only"],
 "cluster-autoscaler": ["--set","autoDiscovery.clusterName=demo"],
 "aws-load-balancer-controller": ["--set","clusterName=demo"],
 "opentelemetry-collector": ["--set","mode=deployment","--set","image.repository=otel/opentelemetry-collector-k8s"],
 "loki": ["--set","deploymentMode=SingleBinary","--set","loki.storage.type=filesystem","--set","loki.commonConfig.replication_factor=1",
          "--set","singleBinary.replicas=1","--set","loki.useTestSchema=true","--set","backend.replicas=0","--set","read.replicas=0","--set","write.replicas=0",
          "--set","chunksCache.enabled=false","--set","resultsCache.enabled=false","--set","minio.enabled=false"],
}
os.makedirs("work", exist_ok=True); os.makedirs("out", exist_ok=True)
for line in open(os.path.join(HERE, "charts.txt")):
    name, repo, path = line.split()
    src = os.path.join("src", repo.replace("/","__"), path)
    dst = os.path.join("work", name)
    if not os.path.isdir(src): print("MISSING", name); continue
    shutil.rmtree(dst, ignore_errors=True); shutil.copytree(src, dst, ignore=shutil.ignore_patterns(".git"))
    cy = os.path.join(dst, "Chart.yaml")
    if not os.path.exists(cy) and not os.path.exists(os.path.join(dst,"Chart.template.yaml")): print("MISSING", name); continue
    if not os.path.exists(cy) and os.path.exists(os.path.join(dst,"Chart.template.yaml")):
        s = open(os.path.join(dst,"Chart.template.yaml")).read().replace("{{IMAGE_TAG}}","v1.0.0").replace("{{VERSION}}","1.0.0")
        open(cy,"w").write(s)
    c = yaml.safe_load(open(cy))
    keep = []
    for d in c.get("dependencies") or []:
        local = os.path.join(dst, "charts", d["name"])
        if d["name"] == "common" and "bitnami" in repo:
            shutil.copytree(os.path.join("src","bitnami__charts","bitnami","common"), local, dirs_exist_ok=True); keep.append(d); d.pop("repository",None); continue
        alt = {"kube-state-metrics":"src/prometheus-community__helm-charts/charts/kube-state-metrics",
               "prometheus-node-exporter":"src/prometheus-community__helm-charts/charts/prometheus-node-exporter",
               "grafana":"src/grafana-community__helm-charts/charts/grafana"}.get(d["name"])
        if alt and not os.path.isdir(local):
            shutil.copytree(alt, local); d.pop("repository",None); keep.append(d); continue
        if os.path.isdir(local) or str(d.get("repository","")).startswith("file://"):
            keep.append(d); continue
    c["dependencies"] = keep
    yaml.safe_dump(c, open(cy,"w"))
    for f in ("Chart.lock","requirements.lock"):
        p=os.path.join(dst,f)
        if os.path.exists(p): os.remove(p)
    kube = "1.34.0" if name == "longhorn" else "1.33.0"
    r = subprocess.run([H,"template",name,dst,"--namespace","demo","--kube-version",kube]+extra.get(name,[]), capture_output=True, text=True)
    if r.returncode: print("FAIL", name, r.stderr.strip()[:300]); continue
    open(f"out/{name}.yaml","w").write(r.stdout)
    print("ok", name, r.stdout.count("\nkind: "), "objects", f"(deps kept {len(keep)})")
