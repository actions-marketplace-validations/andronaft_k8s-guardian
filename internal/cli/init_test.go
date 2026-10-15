package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo creates a repository with k8s/ manifests and makes it the working
// directory.
func repo(t *testing.T, manifests ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "k8s"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, m := range manifests {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "k8s", filepath.Base(m)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

func runVersion(t *testing.T, version string, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := &App{Name: "k8s-guardian", Version: version, Stdout: &out, Stderr: &errb}
	return app.Run(args), errb.String()
}

func TestInit(t *testing.T) {
	ins, sec := mustAbs(t, insecure), mustAbs(t, secure)
	repo(t, ins, sec)
	code, out := runVersion(t, "0.7.0", "init", "-f", "k8s")
	if code != 0 {
		t.Fatalf("init exit %d:\n%s", code, out)
	}
	for _, f := range []string{".k8s-guardian.yaml", ".k8s-guardian-baseline.json", ".github/workflows/k8s-guardian.yml"} {
		if !strings.Contains(out, "wrote    "+f) {
			t.Errorf("%s not written:\n%s", f, out)
		}
	}
	cfg, _ := os.ReadFile(".k8s-guardian.yaml")
	if !strings.Contains(string(cfg), "\nbaseline: .k8s-guardian-baseline.json\n") {
		t.Errorf("config doesn't use the baseline:\n%s", cfg)
	}
	wf, _ := os.ReadFile(".github/workflows/k8s-guardian.yml")
	if !strings.Contains(string(wf), "uses: andronaft/k8s-guardian@v0.7.0\n        with:\n          path: k8s\n") {
		t.Errorf("workflow:\n%s", wf)
	}

	// The recorded findings don't fail the check; a new problem does.
	if code, _, stderr := run(t, "check", "-f", "k8s"); code != 0 || !strings.Contains(stderr, "known finding(s) hidden by baseline") {
		t.Errorf("check after init: exit %d\n%s", code, stderr)
	}
	b, _ := os.ReadFile(ins)
	if err := os.WriteFile(filepath.Join("k8s", "new.yaml"), bytes.ReplaceAll(b, []byte("name: web"), []byte("name: web2")), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := run(t, "check", "-f", "k8s"); code != 1 {
		t.Errorf("a new problem must fail the check, exit %d", code)
	}

	// A second init keeps everything.
	if err := os.WriteFile(".k8s-guardian.yaml", []byte("failOn: warning\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runVersion(t, "0.7.0", "init", "-f", "k8s"); code != 0 || strings.Contains(out, "wrote") {
		t.Errorf("second init (exit %d) wrote files:\n%s", code, out)
	}
	if cfg, _ := os.ReadFile(".k8s-guardian.yaml"); string(cfg) != "failOn: warning\n" {
		t.Errorf("config overwritten:\n%s", cfg)
	}
}

// An existing config keeps its name (a new .yaml would take precedence over
// a .yml) and decides where the baseline goes.
func TestInitKeepsExistingConfig(t *testing.T) {
	repo(t, mustAbs(t, insecure))
	cfg := "baseline: ci/known.json\nskip: [KG010]\n"
	if err := os.WriteFile(".k8s-guardian.yml", []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runVersion(t, "dev", "init", "-f", "k8s", "--no-workflow")
	if code != 0 || !strings.Contains(out, "wrote    ci/known.json") || !strings.Contains(out, "git add ci/known.json\n") {
		t.Fatalf("init exit %d:\n%s", code, out)
	}
	for _, f := range []string{".k8s-guardian.yaml", ".k8s-guardian-baseline.json"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s written next to the existing config", f)
		}
	}
	if b, _ := os.ReadFile(".k8s-guardian.yml"); string(b) != cfg {
		t.Errorf("config changed:\n%s", b)
	}
	if code, _, stderr := run(t, "check", "-f", "k8s"); code != 0 {
		t.Errorf("check after init: exit %d\n%s", code, stderr)
	}

	// --force replaces the config, so its skip list doesn't shape the
	// baseline either.
	if code, out = runVersion(t, "dev", "init", "-f", "k8s", "--no-workflow", "--force"); code != 0 || !strings.Contains(out, "wrote    .k8s-guardian.yml\n") {
		t.Fatalf("init --force exit %d:\n%s", code, out)
	}
	if code, _, stderr := run(t, "check", "-f", "k8s"); code != 0 {
		t.Errorf("check after init --force: exit %d\n%s", code, stderr)
	}
}

func TestInitConfigWithoutBaseline(t *testing.T) {
	repo(t, mustAbs(t, insecure))
	if err := os.WriteFile(".k8s-guardian.yaml", []byte("failOn: error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runVersion(t, "dev", "init", "-f", "k8s", "--no-workflow")
	if code != 0 || !strings.Contains(out, "names no baseline") {
		t.Fatalf("init exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(".k8s-guardian-baseline.json"); err == nil {
		t.Error("baseline written that the config doesn't use")
	}
}

func TestInitCleanRepository(t *testing.T) {
	repo(t, mustAbs(t, secure))
	if code, out := runVersion(t, "dev", "init", "--no-workflow"); code != 0 || !strings.Contains(out, "0 finding(s)") {
		t.Fatalf("init exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(".k8s-guardian-baseline.json"); err == nil {
		t.Error("no findings, so no baseline")
	}
	if _, err := os.Stat(".github"); err == nil {
		t.Error("--no-workflow wrote a workflow")
	}
	if cfg, _ := os.ReadFile(".k8s-guardian.yaml"); !strings.Contains(string(cfg), "# baseline: ") {
		t.Errorf("config:\n%s", cfg)
	}
}

func TestInitWithoutManifests(t *testing.T) {
	repo(t)
	if code, out := runVersion(t, "dev", "init"); code != ExitError || !strings.Contains(out, "no Kubernetes manifests found") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(".k8s-guardian.yaml"); err == nil {
		t.Error("nothing to set up, but a config was written")
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
