package baseline

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

func finding(rule, msg string, line int) rules.Finding {
	return rules.Finding{RuleID: rule, Message: msg, Source: "k8s/app.yaml", Resource: "Deployment/web", Container: "web", Line: line}
}

func TestFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.json")
	if err := Write(path, []rules.Finding{finding("KG001", "missing requests", 10), finding("KG008", "missing probe", 10), finding("KG008", "missing probe", 30)}); err != nil {
		t.Fatal(err)
	}
	now := []rules.Finding{
		finding("KG001", "missing requests", 14), // moved down: still known
		finding("KG008", "missing probe", 14),
		finding("KG008", "missing probe", 34),
		finding("KG008", "missing probe", 50), // a third occurrence is new
		finding("KG010", "uses :latest", 14),  // new rule hit
	}
	left, hidden, err := Filter(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if hidden != 3 || len(left) != 2 || left[0].Line != 50 || left[1].RuleID != "KG010" {
		t.Errorf("hidden=%d left=%+v", hidden, left)
	}
}

func TestFilterMissingBaseline(t *testing.T) {
	_, _, err := Filter(filepath.Join(t.TempDir(), "none.json"), nil)
	if err == nil || !strings.Contains(err.Error(), "--update-baseline") {
		t.Errorf("expected a hint to create the baseline, got %v", err)
	}
}

// A baseline written on Linux must match on Windows and vice versa.
func TestFilterIgnoresPathSeparators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.json")
	recorded := finding("KG001", "missing requests", 10)
	recorded.Source = "k8s/app.yaml"
	if err := Write(path, []rules.Finding{recorded}); err != nil {
		t.Fatal(err)
	}
	now := finding("KG001", "missing requests", 12)
	now.Source = filepath.Join("k8s", "app.yaml") // k8s\app.yaml on Windows
	left, hidden, err := Filter(path, []rules.Finding{now})
	if err != nil || hidden != 1 || len(left) != 0 {
		t.Errorf("hidden=%d left=%v err=%v", hidden, left, err)
	}
}
