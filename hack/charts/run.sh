#!/usr/bin/env bash
# Runs k8s-guardian against popular public Helm charts (see charts.txt) and
# checks that --fix is idempotent. Results: docs/real-world-test.md.
#
#   hack/charts/run.sh [workdir]      # needs git, helm, python3 + PyYAML
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=${1:-$(mktemp -d)}
kg=${KG:-k8s-guardian}
mkdir -p "$work" && cd "$work"

# Sparse, shallow clones: one per repository.
awk '{print $2}' "$here/charts.txt" | sort -u | while read -r repo; do
  dir=src/${repo//\//__}
  [ -d "$dir/.git" ] && continue
  git clone -q --depth 1 --filter=blob:none --no-checkout "https://github.com/$repo" "$dir"
  paths=$(awk -v r="$repo" '$2==r{print "/"$3"/"}' "$here/charts.txt")
  [ "$repo" = bitnami/charts ] && paths="$paths /bitnami/common/"
  if [ "$paths" != "/./" ]; then # charts at the repository root need everything
    # shellcheck disable=SC2086 # one path per word
    git -C "$dir" sparse-checkout set --no-cone $paths
  fi
  git -C "$dir" checkout -q
done

python3 "$here/render.py"

mkdir -p results
for f in out/*.yaml; do
  n=$(basename "$f" .yaml)
  "$kg" check -f "$f" --format json > "results/$n.json" || true
  for mode in "" --unsafe-fixes; do
    # Remaining findings exit non-zero; only the YAML matters here.
    "$kg" check -f "$f" --fix --stdout $mode > fixed.yaml 2>/dev/null || true
    "$kg" check -f fixed.yaml --fix --stdout $mode > again.yaml 2>/dev/null || true
    cmp -s fixed.yaml again.yaml || echo "NOT IDEMPOTENT: $n $mode"
  done
done
python3 - <<'PY'
import collections, glob, json
sev, rules = collections.Counter(), collections.Counter()
for f in glob.glob("results/*.json"):
    for x in json.load(open(f))["findings"] or []:
        sev[x["severity"]] += 1
        rules[x["ruleId"]] += 1
print(dict(sev))
print(dict(sorted(rules.items())))
PY
