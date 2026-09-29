//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/export"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

var kyvernoFailed = regexp.MustCompile(`policy k8s-guardian-([a-z0-9]+)-\S+ -> resource \S+ failed`)

// kyvernoIDs runs `kyverno apply` and returns the IDs of failed policies.
func kyvernoIDs(t *testing.T, policies, doc string) []string {
	t.Helper()
	dir := t.TempDir()
	pf, rf := filepath.Join(dir, "p.yaml"), filepath.Join(dir, "r.yaml")
	os.WriteFile(pf, []byte(policies), 0o600)
	os.WriteFile(rf, []byte(doc), 0o600)
	out, err := exec.Command("kyverno", "apply", pf, "--resource", rf).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "fail:") {
		t.Fatalf("kyverno apply: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "error: 0") {
		t.Fatalf("kyverno reported evaluation errors:\n%s", out)
	}
	seen := map[string]bool{}
	for _, m := range kyvernoFailed.FindAllStringSubmatch(string(out), -1) {
		seen[strings.ToUpper(m[1])] = true
	}
	var ids []string
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// engineAll returns every rule ID (any severity) the engine reports.
func engineAll(t *testing.T, doc string, opts rules.Options, ids map[string]bool) []string {
	f, err := manifest.Parse([]byte(doc), "case.yaml")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, fd := range rules.Validate(f.Objects, opts) {
		if ids[fd.RuleID] {
			seen[fd.RuleID] = true
		}
	}
	var out []string
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func requireKyverno(t *testing.T) {
	if _, err := exec.LookPath("kyverno"); err != nil {
		t.Skip("kyverno CLI not in PATH")
	}
}

func TestKyvernoExportMatchesEngine(t *testing.T) {
	requireKyverno(t)
	ids := export.BuiltinIDs()
	res, err := export.Kyverno(ids, nil, export.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	okRes := "{requests: {cpu: 10m, memory: 8Mi}, limits: {memory: 8Mi}}"
	okSC := "{runAsNonRoot: true, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}"
	img := "registry.example.com/app:1.0"
	cases := []struct{ name, image, res, sc, extra string }{
		{"compliant", img, okRes, okSC, ""},
		{"no requests", img, "{limits: {memory: 8Mi}}", okSC, ""},
		{"runAsUser 0", img, okRes, "{runAsUser: 0}", ""},
		{"pod-level runAsNonRoot", img, okRes, "{allowPrivilegeEscalation: false}", "      securityContext: {runAsNonRoot: true}\n"},
		{"privileged", img, okRes, "{runAsNonRoot: true, privileged: true}", ""},
		{"registry port, no tag", "registry.example.com:5000/app", okRes, okSC, ""},
		{"hostNetwork + hostPath", img, okRes, okSC, "      hostNetwork: true\n      volumes: [{name: h, hostPath: {path: /x}}]\n"},
		{"empty securityContext", img, "{}", "{}", ""},
	}
	for _, kind := range []string{"Pod", "Deployment", "CronJob"} {
		for _, c := range cases {
			doc := workload(kind, "default", c.image, c.res, c.sc, c.extra)
			want := engineAll(t, doc, rules.NewOptions(""), idSet)
			got := kyvernoIDs(t, string(res.YAML), doc)
			if strings.Join(want, ",") != strings.Join(got, ",") {
				t.Errorf("%s/%s: engine %v, kyverno %v", kind, c.name, want, got)
			} else {
				t.Logf("✔ %s/%s: %v", kind, c.name, got)
			}
		}
	}
}

func TestKyvernoCustomRules(t *testing.T) {
	requireKyverno(t)
	docs, _, err := custom.LoadDocuments([]string{"../../examples/rules"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := export.Kyverno(nil, docs, export.Options{})
	if err != nil || len(res.Skipped) > 0 {
		t.Fatalf("%v %v", err, res.Skipped)
	}
	rs, _ := custom.Load([]string{"../../examples/rules"})
	opts := rules.Options{Skip: map[string]bool{}, Custom: rs}
	ids := map[string]bool{}
	for _, r := range rs {
		ids[r.ID] = true
	}
	for _, r := range rules.All {
		opts.Skip[strings.ToLower(r.ID)] = true
	}
	for _, file := range []string{"insecure-deployment.yaml", "secure-deployment.yaml", "costly-app.yaml"} {
		data, _ := os.ReadFile(filepath.Join("../../examples", file))
		for i, doc := range strings.Split(string(data), "\n---\n") {
			want := engineAll(t, doc, opts, ids)
			got := kyvernoIDs(t, string(res.YAML), doc)
			if fmt.Sprint(want) != fmt.Sprint(got) {
				t.Errorf("%s doc %d: engine %v, kyverno %v", file, i, want, got)
			} else {
				t.Logf("✔ %s doc %d: %v", file, i, got)
			}
		}
	}
}
