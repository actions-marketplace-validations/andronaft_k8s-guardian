"""Compare k8s-guardian findings on the charts with expected.json.

    python3 compare.py <results dir> <expected.json> [--update]

expected.json maps chart -> rule ID -> number of findings. Any difference
fails, so every change in behaviour on real charts shows up in review:
update the file in the same pull request when the change is intended.
"""
import collections
import glob
import json
import os
import sys


def counts(results):
    out = {}
    for path in sorted(glob.glob(os.path.join(results, "*.json"))):
        chart = os.path.basename(path)[: -len(".json")]
        c = collections.Counter()
        with open(path) as f:
            for finding in json.load(f)["findings"] or []:
                c[finding["ruleId"]] += 1
        out[chart] = dict(sorted(c.items()))
    return out


def main():
    results, expected_path = sys.argv[1], sys.argv[2]
    got = counts(results)
    totals = collections.Counter()
    for c in got.values():
        totals.update(c)
    print(f"{len(got)} charts, {sum(totals.values())} findings:", dict(sorted(totals.items())))

    if "--update" in sys.argv[3:]:
        with open(expected_path, "w") as f:
            json.dump(got, f, indent=2, sort_keys=True)
            f.write("\n")
        print(f"wrote {expected_path}")
        return 0

    with open(expected_path) as f:
        want = json.load(f)
    diffs = []
    for chart in sorted(set(want) | set(got)):
        if chart not in got:
            diffs.append(f"{chart}: not rendered (expected {sum(want[chart].values())} findings)")
            continue
        if chart not in want:
            diffs.append(f"{chart}: new chart, not in {os.path.basename(expected_path)}")
            continue
        for rule in sorted(set(want[chart]) | set(got[chart])):
            w, g = want[chart].get(rule, 0), got[chart].get(rule, 0)
            if w != g:
                diffs.append(f"{chart}: {rule} {w} -> {g}")
    if diffs:
        print("Findings differ from expected.json:")
        for d in diffs:
            print("  " + d)
        print("If this is intended, run with UPDATE=1 and commit expected.json.")
        return 1
    print("matches expected.json")
    return 0


if __name__ == "__main__":
    sys.exit(main())
