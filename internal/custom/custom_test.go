package custom

import (
	"os"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

func TestExampleRules(t *testing.T) {
	rs, err := Load([]string{"../../examples/rules"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(rs))
	}
	data, _ := os.ReadFile("../../examples/costly-app.yaml")
	f, _ := manifest.Parse(data, "costly.yaml")
	opts := rules.Options{Skip: map[string]bool{}, Custom: rs}
	for _, r := range rules.All {
		opts.Skip[strings.ToLower(r.ID)] = true
	}
	got := map[string]int{}
	for _, fd := range rules.Validate(f.Objects, opts) {
		got[fd.RuleID]++
	}
	// Deployment has a valid contact + trusted registry + 4 CPU (<= 4): only
	// the LoadBalancer without source ranges is reported.
	if got["ORG004"] != 1 || len(got) != 1 {
		t.Errorf("unexpected findings: %v", got)
	}
}

func TestPathsAndOps(t *testing.T) {
	obj := `apiVersion: v1
kind: Pod
metadata:
  name: p
  labels: {team: payments}
  annotations: {"example.com/owner": "a@b.io"}
spec:
  containers:
    - name: a
      image: nginx:1.27
      resources: {limits: {cpu: 1500m, memory: 1Gi}}
    - name: b
      image: busybox:latest
`
	f, _ := manifest.Parse([]byte(obj), "p.yaml")
	root := f.Objects[0].Root
	cases := []struct {
		c    Condition
		want bool
	}{
		{Condition{Path: "metadata.labels.team", Op: "in", Values: []string{"payments", "core"}}, true},
		{Condition{Path: "metadata.labels.team", Op: "notIn", Values: []string{"payments"}}, false},
		{Condition{Path: "metadata.annotations['example.com/owner']", Op: "matches", Value: "@"}, true},
		{Condition{Path: "metadata.labels.missing", Op: "exists"}, false},
		{Condition{Path: "metadata.labels.missing", Op: "notExists"}, true},
		{Condition{Path: "spec.containers[*].image", Op: "notMatches", Value: ":latest$"}, false},
		{Condition{Path: "spec.containers[0].image", Op: "notMatches", Value: ":latest$"}, true},
		{Condition{Path: "spec.containers[0].resources.limits.cpu", Op: "lte", Value: "2"}, true},
		{Condition{Path: "spec.containers[0].resources.limits.memory", Op: "gt", Value: "2Gi"}, false},
		{Condition{Path: "spec.containers[1].resources.limits.cpu", Op: "lte", Value: "2"}, true}, // missing -> ok
		{Condition{Path: "kind", Op: "equals", Value: "Pod"}, true},
	}
	for _, tc := range cases {
		cc, err := compileCond(tc.c)
		if err != nil {
			t.Fatalf("%+v: %v", tc.c, err)
		}
		if got := cc.holds(root); got != tc.want {
			t.Errorf("%s: got %v want %v", cc, got, tc.want)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []Document{
		{Metadata: Metadata{Name: "x"}, Spec: Spec{ID: "KG999", Severity: "error", Assert: []Condition{{Path: "a", Op: "exists"}}}},
		{Metadata: Metadata{Name: "x"}, Spec: Spec{ID: "A1", Severity: "fatal", Assert: []Condition{{Path: "a", Op: "exists"}}}},
		{Metadata: Metadata{Name: "x"}, Spec: Spec{ID: "A1", Severity: "error", Assert: []Condition{{Path: "a", Op: "like"}}}},
		{Metadata: Metadata{Name: "x"}, Spec: Spec{ID: "A1", Severity: "error", Assert: []Condition{{Path: "a", Op: "lte", Value: "lots"}}}},
		{Metadata: Metadata{Name: "x"}, Spec: Spec{ID: "A1", Severity: "error", Match: Match{Scope: "node"}, Assert: []Condition{{Path: "a", Op: "exists"}}}},
		{Metadata: Metadata{Name: "x"}, Spec: Spec{ID: "A1", Severity: "error"}},
	}
	for i, d := range bad {
		if _, err := Compile(d); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}
