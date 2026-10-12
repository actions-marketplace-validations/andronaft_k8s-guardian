#!/usr/bin/env bash
# Runs k8s-guardian against popular public Helm charts (charts.txt, pinned to
# the commits in refs.txt), checks that --fix is idempotent and compares the
# findings per chart and rule with expected.json. Exits non-zero on any
# difference. Results: docs/real-world-test.md.
#
#   hack/charts/run.sh [workdir]            # needs git, helm, python3 + PyYAML
#   UPDATE=1 hack/charts/run.sh [workdir]   # accept the current findings
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=${1:-$(mktemp -d)}
kg=${KG:-k8s-guardian}
mkdir -p "$work" && cd "$work"

# Sparse, shallow fetch of the pinned commit, one per repository.
awk '{print $2}' "$here/charts.txt" | sort -u | while read -r repo; do
  dir=src/${repo//\//__}
  ref=$(awk -v r="$repo" '$1==r{print $2}' "$here/refs.txt")
  if [ -z "$ref" ]; then
    echo "no pinned commit for $repo in refs.txt" >&2
    exit 1
  fi
  if [ "$(git -C "$dir" rev-parse HEAD 2>/dev/null)" = "$ref" ]; then
    continue
  fi
  rm -rf "$dir"
  git init -q "$dir"
  git -C "$dir" remote add origin "https://github.com/$repo"
  paths=$(awk -v r="$repo" '$2==r{print "/"$3"/"}' "$here/charts.txt")
  [ "$repo" = bitnami/charts ] && paths="$paths /bitnami/common/"
  if [ "$paths" != "/./" ]; then # charts at the repository root need everything
    # shellcheck disable=SC2086 # one path per word
    git -C "$dir" sparse-checkout set --no-cone $paths
  fi
  git -C "$dir" fetch -q --depth 1 --filter=blob:none origin "$ref"
  git -C "$dir" checkout -q FETCH_HEAD
done

python3 "$here/render.py"

failed=0
mkdir -p results
for f in out/*.yaml; do
  n=$(basename "$f" .yaml)
  "$kg" check -f "$f" --format json > "results/$n.json" || true
  for mode in "" --unsafe-fixes; do
    # Remaining findings exit non-zero; only the YAML matters here.
    "$kg" check -f "$f" --fix --stdout $mode > fixed.yaml 2>/dev/null || true
    "$kg" check -f fixed.yaml --fix --stdout $mode > again.yaml 2>/dev/null || true
    if ! cmp -s fixed.yaml again.yaml; then
      echo "NOT IDEMPOTENT: $n $mode"
      failed=1
    fi
  done
done

update=()
[ -n "${UPDATE:-}" ] && update=(--update)
python3 "$here/compare.py" results "$here/expected.json" ${update[@]+"${update[@]}"} || failed=1
exit "$failed"
