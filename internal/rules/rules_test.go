package rules_test

import (
	"fmt"
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
	for _, id := range []string{"KG001", "KG002", "KG003", "KG004", "KG006", "KG008", "KG009", "KG010", "KG011", "KG013", "KG014", "KG015"} {
		if got[id] == 0 {
			t.Errorf("expected finding %s", id)
		}
	}
	// The container is privileged: KG004 covers escalation and capabilities.
	if got["KG005"]+got["KG007"] != 0 {
		t.Errorf("KG005/KG007 reported for a privileged container: %v", got)
	}
}

func TestFixIsIdempotentAndKeepsComments(t *testing.T) {
	f := load(t, "../../examples/insecure-deployment.yaml")
	opts := rules.NewOptions("")
	opts.UnsafeFixes = true
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
	for _, want := range []string{"# TODO pin me", "# Example:", "kind: Service", "memory: 128Mi", "type: RuntimeDefault", "runAsNonRoot: true",
		// Deliberate host access is reported, never silently removed.
		"hostNetwork: true", "privileged: true"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("fixed output missing %q:\n%s", want, out)
		}
	}
}

func TestOnlySafeFixesByDefault(t *testing.T) {
	const pod = `apiVersion: v1
kind: Pod
metadata: {name: p}
spec:
  containers: [{name: c, image: nginx:1.27}]
`
	f, err := manifest.Parse([]byte(pod), "p.yaml")
	if err != nil {
		t.Fatal(err)
	}
	opts := rules.NewOptions("")
	for _, fd := range rules.Validate(f.Objects, opts) {
		switch fd.RuleID {
		case "KG001", "KG002", "KG003", "KG006", "KG007":
			if fd.Fixable || !fd.UnsafeFix {
				t.Errorf("%s should need --unsafe-fixes: %+v", fd.RuleID, fd)
			}
		case "KG005", "KG013":
			if !fd.Fixable || fd.UnsafeFix {
				t.Errorf("%s should be a safe fix: %+v", fd.RuleID, fd)
			}
		}
	}
	rules.Fix(f.Objects, opts)
	out, _ := f.Encode()
	if !strings.Contains(string(out), "allowPrivilegeEscalation: false") || strings.Contains(string(out), "runAsNonRoot") || strings.Contains(string(out), "resources") {
		t.Errorf("default --fix applied more than the safe fixes:\n%s", out)
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

func validateDoc(t *testing.T, doc string) []rules.Finding {
	t.Helper()
	f, err := manifest.Parse([]byte(doc), "x.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return rules.Validate(f.Objects, rules.NewOptions(""))
}

func messages(fs []rules.Finding, id string) []string {
	var out []string
	for _, f := range fs {
		if f.RuleID == id {
			out = append(out, f.Message)
		}
	}
	return out
}

// Regressions found by running against popular public Helm charts.
func TestServiceTargetPort(t *testing.T) {
	const web = `apiVersion: apps/v1
kind: Deployment
metadata: {name: web}
spec:
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web, tier: frontend}}
    spec:
      containers:
        - name: web
          image: nginx:1.27
%s
---
apiVersion: v1
kind: Service
metadata: {name: web}
spec:
  selector: %s
  ports: [%s]
`
	cases := []struct {
		name, ports, selector, svcPorts string
		want                            string
	}{
		{"numeric target without declared ports works", "", "{app: web}", "{port: 5432}", ""},
		{"numeric target missing from declared ports", "          ports: [{containerPort: 8080}]", "{app: web}", "{port: 80, targetPort: 9090}", "targetPort 9090"},
		{"named target missing", "          ports: [{name: http, containerPort: 8080}]", "{app: web}", "{port: 80, targetPort: metrics}", `targetPort "metrics"`},
		{"declared numeric target", "          ports: [{containerPort: 8080}]", "{app: web}", "{port: 80, targetPort: 8080}", ""},
		{"pods created by an operator", "", "{app.kubernetes.io/name: alertmanager}", "{port: 9093}", ""},
		{"near-miss selector", "", "{app: web, tier: backend}", "{port: 80}", "carries only some"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(messages(validateDoc(t, fmt.Sprintf(web, c.ports, c.selector, c.svcPorts)), "KG016"), "; ")
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Errorf("KG016 = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOneShotPodsNeedNoProbes(t *testing.T) {
	const pod = `apiVersion: v1
kind: Pod
metadata:
  name: chart-test
  annotations: {helm.sh/hook: test}
spec:
  %s
  containers: [{name: t, image: busybox:1.36}]
`
	if got := ids(validateDoc(t, fmt.Sprintf(pod, "restartPolicy: Never"))); got["KG008"]+got["KG009"] != 0 {
		t.Errorf("probes required on a one-shot pod: %v", got)
	}
	if got := ids(validateDoc(t, fmt.Sprintf(pod, "restartPolicy: Always"))); got["KG008"] == 0 || got["KG009"] == 0 {
		t.Errorf("probes not required on a long-running pod: %v", got)
	}
}
