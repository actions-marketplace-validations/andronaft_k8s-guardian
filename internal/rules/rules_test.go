package rules_test

import (
	"os"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

func load(t *testing.T, path string) *manifest.File {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := manifest.Parse(data, path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func ids(fs []rules.Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.RuleID]++
	}
	return m
}

func TestSecureManifestPasses(t *testing.T) {
	f := load(t, "../../examples/secure-deployment.yaml")
	if fs := rules.Validate(f.Objects, rules.NewOptions("")); len(fs) != 0 {
		t.Fatalf("expected no findings, got %+v", fs)
	}
}

func TestInsecureManifestFindings(t *testing.T) {
	f := load(t, "../../examples/insecure-deployment.yaml")
	got := ids(rules.Validate(f.Objects, rules.NewOptions("")))
	for _, id := range []string{"KG001", "KG002", "KG003", "KG004", "KG005", "KG006", "KG007", "KG008", "KG009", "KG010", "KG011", "KG013", "KG014", "KG015"} {
		if got[id] == 0 {
			t.Errorf("expected finding %s", id)
		}
	}
}

func TestFixIsIdempotentAndKeepsComments(t *testing.T) {
	f := load(t, "../../examples/insecure-deployment.yaml")
	opts := rules.NewOptions("")
	if n := rules.Fix(f.Objects, opts); n == 0 {
		t.Fatal("expected fixes")
	}
	for _, fd := range rules.Validate(f.Objects, opts) {
		if fd.Fixable {
			t.Errorf("fixable finding left after fix: %+v", fd)
		}
	}
	if n := rules.Fix(f.Objects, opts); n != 0 {
		t.Errorf("second fix run changed %d things", n)
	}
	out, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# TODO pin me", "# Example:", "kind: Service", "memory: 128Mi", "type: RuntimeDefault", "- ALL"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("fixed output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "hostNetwork") {
		t.Error("hostNetwork should have been removed")
	}
}

func TestSkipAndIgnoreAnnotation(t *testing.T) {
	src := `apiVersion: v1
kind: Pod
metadata:
  name: p
  annotations:
    k8s-guardian.io/ignore: "KG008, readiness-probe"
spec:
  containers:
    - name: c
      image: busybox
`
	f, err := manifest.Parse([]byte(src), "p.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(rules.Validate(f.Objects, rules.NewOptions("KG010")))
	for _, id := range []string{"KG008", "KG009", "KG010"} {
		if got[id] != 0 {
			t.Errorf("%s should be skipped", id)
		}
	}
	if got["KG001"] == 0 {
		t.Error("KG001 should still run")
	}
}

func TestImageTags(t *testing.T) {
	cases := map[string]bool{ // image -> violation expected
		"nginx":                        true,
		"nginx:latest":                 true,
		"registry:5000/team/app":       true,
		"registry:5000/team/app:1.2.3": false,
		"nginx@sha256:abcdef":          false,
		"ghcr.io/org/app:v2":           false,
	}
	r := rules.Lookup("KG010")
	for img, bad := range cases {
		src := "kind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n    - name: c\n      image: " + img + "\n"
		f, err := manifest.Parse([]byte(src), "p.yaml")
		if err != nil {
			t.Fatal(err)
		}
		tg := rules.NewTarget(f.Objects[0])
		if got := r.Container(tg, tg.Containers[0]) != ""; got != bad {
			t.Errorf("%s: violation=%v, want %v", img, got, bad)
		}
	}
}

func TestWorkloadKindsAndLists(t *testing.T) {
	src := `apiVersion: v1
kind: List
items:
  - apiVersion: batch/v1
    kind: CronJob
    metadata: {name: backup}
    spec:
      schedule: "0 * * * *"
      jobTemplate:
        spec:
          template:
            spec:
              containers:
                - name: b
                  image: busybox:1.36
  - apiVersion: v1
    kind: ConfigMap
    metadata: {name: cm}
`
	f, err := manifest.Parse([]byte(src), "list.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(f.Objects))
	}
	got := ids(rules.Validate(f.Objects, rules.NewOptions("")))
	if got["KG001"] != 1 {
		t.Errorf("expected KG001 on CronJob container, got %v", got)
	}
	if got["KG008"] != 0 || got["KG009"] != 0 {
		t.Errorf("probes must not be required for batch workloads: %v", got)
	}
}
