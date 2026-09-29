//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// Each custom rule is exported to CEL, bound to its own namespace and
// evaluated by the API server; the decision must equal the Go engine's.
func TestCustomRulesParity(t *testing.T) {
	type tc struct {
		rule    string
		objects []string
	}
	cm := func(meta, data string) string {
		return fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: cm, namespace: NS%s}\ndata: {%s}\n", meta, data)
	}
	deploy := func(replicas, image, res, volumes string) string {
		return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata: {name: d, namespace: NS}
spec:
  replicas: %s
  selector: {matchLabels: {app: d}}
  template:
    metadata: {labels: {app: d}}
    spec:
      containers:
        - name: c
          image: %s
          resources: %s
      volumes: %s
`, replicas, image, res, volumes)
	}
	cases := []tc{
		{`match: {kinds: [ConfigMap]}
  assert: [{path: metadata.labels.team, op: in, values: [a, b]}]`,
			[]string{cm(", labels: {team: a}", ""), cm(", labels: {team: z}", ""), cm("", "")}},
		{`match: {kinds: [ConfigMap]}
  assert: [{path: "metadata.annotations['example.com/owner']", op: matches, value: '^[a-z]+@example\.com$'}]`,
			[]string{cm(`, annotations: {"example.com/owner": "ops@example.com"}`, ""), cm(`, annotations: {"example.com/owner": "Ops@evil.io"}`, ""), cm("", "")}},
		{`match: {kinds: [ConfigMap]}
  assert: [{path: data.mode, op: notEquals, value: debug}]`,
			[]string{cm("", "mode: debug"), cm("", "mode: prod"), cm("", "")}},
		{`match: {kinds: [ConfigMap]}
  when: [{path: data.env, op: equals, value: prod}]
  assert: [{path: data.replicas, op: gte, value: "2"}]`,
			[]string{cm("", "env: prod, replicas: '1'"), cm("", "env: prod, replicas: '3'"), cm("", "env: dev, replicas: '1'"), cm("", "env: prod")}},
		{`match: {kinds: [ConfigMap]}
  assert: [{path: metadata.labels.tier, op: notExists}, {path: "data['a.b']", op: exists}]`,
			[]string{cm("", "a.b: x"), cm(", labels: {tier: web}", "a.b: x"), cm("", "c: x")}},
		{`match: {kinds: [ConfigMap]}
  assert: [{path: metadata.labels.env, op: notIn, values: [dev, test]}]`,
			[]string{cm(", labels: {env: dev}", ""), cm(", labels: {env: prod}", ""), cm("", "")}},
		{`match: {kinds: [Deployment]}
  assert: [{path: spec.replicas, op: gt, value: "1"}]`,
			[]string{deploy("1", "r.io/a:1", "{}", "[]"), deploy("3", "r.io/a:1", "{}", "[]")}},
		{`match: {scope: container}
  assert: [{path: resources.limits.cpu, op: lte, value: "2"}]`,
			[]string{deploy("1", "r.io/a:1", "{limits: {cpu: 1500m}}", "[]"), deploy("1", "r.io/a:1", "{limits: {cpu: '3'}}", "[]"), deploy("1", "r.io/a:1", "{}", "[]")}},
		{`match: {scope: container}
  assert: [{path: image, op: notMatches, value: ':latest$'}, {path: image, op: matches, value: '^r\.io/'}]`,
			[]string{deploy("1", "r.io/a:1", "{}", "[]"), deploy("1", "r.io/a:latest", "{}", "[]"), deploy("1", "evil.io/a:1", "{}", "[]")}},
		{`match: {scope: pod}
  assert: [{path: "volumes[*].emptyDir", op: exists}]`,
			[]string{deploy("1", "r.io/a:1", "{}", "[{name: t, emptyDir: {}}]"), deploy("1", "r.io/a:1", "{}", "[{name: t, emptyDir: {}}, {name: h, hostPath: {path: /x}}]"), deploy("1", "r.io/a:1", "{}", "[]")}},
		{`match: {scope: pod}
  assert: [{path: "volumes[*].hostPath", op: notExists}]`,
			[]string{deploy("1", "r.io/a:1", "{}", "[{name: t, emptyDir: {}}]"), deploy("1", "r.io/a:1", "{}", "[{name: h, hostPath: {path: /x}}]"), deploy("1", "r.io/a:1", "{}", "[]")}},
	}
	for i, c := range cases {
		id := fmt.Sprintf("E2E%02d", i+1)
		ns := "e2e-" + strings.ToLower(id)
		namespace(t, ns)
		src := fmt.Sprintf("apiVersion: k8s-guardian.io/v1\nkind: Rule\nmetadata: {name: rule-%d}\nspec:\n  id: %s\n  severity: error\n  description: e2e\n  %s\n", i+1, id, c.rule)
		docs, err := custom.Parse([]byte(src), id)
		if err != nil {
			t.Fatalf("%s: %v\n%s", id, err, src)
		}
		compiled, err := custom.Compile(docs[0])
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		applyPolicies(t, nil, docs, ns)
		opts := rules.Options{Skip: map[string]bool{}, Custom: []*rules.Rule{compiled}}
		outcomes := map[bool]bool{}
		for j, obj := range c.objects {
			obj = strings.ReplaceAll(obj, "namespace: NS", "namespace: "+ns)
			compare(t, fmt.Sprintf("%s case %d", id, j+1), obj, opts, map[string]bool{id: true})
			outcomes[len(engine(t, obj, opts, map[string]bool{id: true})) > 0] = true
		}
		if len(outcomes) != 2 {
			t.Errorf("%s: cases should cover both a violation and a pass", id)
		}
	}
}
