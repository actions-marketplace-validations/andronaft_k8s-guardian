package export

import (
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/custom"
)

func TestCELTranslation(t *testing.T) {
	cases := []struct {
		c    custom.Condition
		want string
	}{
		{custom.Condition{Path: "metadata.labels.team", Op: "exists"},
			`has(object.metadata) && has(object.metadata.labels) && has(object.metadata.labels.team)`},
		{custom.Condition{Path: "metadata.annotations['example.com/owner']", Op: "matches", Value: "^a"},
			`(has(object.metadata) && has(object.metadata.annotations) && ("example.com/owner" in object.metadata.annotations)) && (string(object.metadata.annotations["example.com/owner"]).matches("^a"))`},
		{custom.Condition{Path: "spec.replicas", Op: "lte", Value: "3"},
			`!(has(object.spec) && has(object.spec.replicas)) || (quantity(string(object.spec.replicas)).compareTo(quantity("3")) <= 0)`},
		{custom.Condition{Path: "data.namespace", Op: "equals", Value: "x"}, // reserved word -> map index
			`(has(object.data) && ("namespace" in object.data)) && (string(object.data["namespace"]) == "x")`},
		{custom.Condition{Path: "volumes[*].hostPath", Op: "notExists"},
			`!(has(object.volumes) && (size(object.volumes) > 0)) || object.volumes.all(x1, !has(x1.hostPath))`},
	}
	for _, tc := range cases {
		c, want := tc.c, tc.want
		got, err := (&gen{}).cond("object", c)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%+v:\n got  %s\n want %s", c, got, want)
		}
	}
}

func TestVAPOutput(t *testing.T) {
	docs, err := custom.Parse([]byte(`apiVersion: k8s-guardian.io/v1
kind: Rule
metadata: {name: team-label}
spec:
  id: ORG1
  severity: warning
  description: needs a team
  match: {kinds: [Deployment, Service]}
  assert: [{path: metadata.labels.team, op: exists}]
---
apiVersion: k8s-guardian.io/v1
kind: Rule
metadata: {name: everything}
spec:
  id: ORG2
  severity: error
  description: no kinds
  assert: [{path: metadata.labels.team, op: exists}]
`), "r.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := VAP([]string{"KG001", "KG016"}, docs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.YAML)
	// KG001: one policy per workload family; ORG1: one resource policy.
	if res.Count != 4 {
		t.Errorf("expected 4 policies, got %d:\n%s", res.Count, out)
	}
	if len(res.Skipped) != 2 || !strings.Contains(res.Skipped[0], "KG016") || !strings.Contains(res.Skipped[1], "ORG2") {
		t.Errorf("unexpected skipped: %v", res.Skipped)
	}
	for _, want := range []string{
		"name: k8s-guardian-kg001-resource-requests-cronjobs",
		"expression: object.spec.jobTemplate.spec.template.spec",
		"- Warn", // ORG1 is a warning
		"- Deny", // KG001 is an error
		"- deployments",
		"- kube-system",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
