package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".k8s-guardian.yaml")
	os.WriteFile(path, []byte("skip: [KG015]\nseverity: {KG006: error}\nfailOn: warning\nexclude: [vendor, \"charts/*\"]\nrules: [policies]\nbaseline: base.json\n"), 0o600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.FailOn != "warning" || c.Skip[0] != "KG015" || c.Severity["KG006"] != "error" {
		t.Errorf("unexpected config %+v", c)
	}
	// Paths are relative to the config file.
	if c.Rules[0] != filepath.Join(dir, "policies") || c.Baseline != filepath.Join(dir, "base.json") {
		t.Errorf("paths not resolved: %+v", c)
	}
	for p, want := range map[string]bool{
		filepath.Join(dir, "vendor"):                      true,
		filepath.Join(dir, "vendor", "x", "a.yaml"):       true,
		filepath.Join(dir, "charts", "redis"):             true,
		filepath.Join(dir, "k8s", "deploy.yaml"):          false,
		filepath.Join(dir, "vendor-not-really", "a.yaml"): false,
	} {
		if got := c.Excluded(p); got != want {
			t.Errorf("Excluded(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(path, []byte("skp: [KG015]\n"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "skp") {
		t.Errorf("expected an error naming the typo, got %v", err)
	}
}

func TestLoadWithoutConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	c, err := Load("")
	if err != nil || c.Path != "" || len(c.Skip) != 0 {
		t.Errorf("expected an empty config, got %+v %v", c, err)
	}
	if _, err := Load("missing.yaml"); err == nil {
		t.Error("an explicit missing config must be an error")
	}
}
