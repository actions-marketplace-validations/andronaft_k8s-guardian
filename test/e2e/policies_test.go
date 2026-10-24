//go:build e2e

package e2e

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/export"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// applyPolicies exports policies and binds them to exactly one namespace so
// test groups don't interfere with each other.
func applyPolicies(t *testing.T, builtin []string, docs []custom.Document, ns string) {
	t.Helper()
	applyPoliciesWith(t, builtin, docs, ns, export.Options{})
}

// applyPoliciesWith is applyPolicies with export options (e.g. Action Deny,
// which makes warning and info rules deny too).
func applyPoliciesWith(t *testing.T, builtin []string, docs []custom.Document, ns string, opts export.Options) {
	t.Helper()
	res, err := export.VAP(builtin, docs, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) > 0 {
		t.Fatalf("skipped: %v", res.Skipped)
	}
	var out []string
	for _, doc := range strings.Split(string(res.YAML), "\n---\n") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || m == nil {
			continue
		}
		if m["kind"] == "ValidatingAdmissionPolicyBinding" {
			m["spec"].(map[string]any)["matchResources"] = map[string]any{"namespaceSelector": map[string]any{
				"matchLabels": map[string]any{"kubernetes.io/metadata.name": ns}}}
		}
		b, _ := yaml.Marshal(m)
		out = append(out, string(b))
	}
	mustKubectl(t, strings.Join(out, "---\n"), "apply", "-f", "-")
	time.Sleep(2 * time.Second) // let the admission plugin pick up the policies
}

type verdict struct {
	denied bool
	by     string // rule ID of the denying policy
	out    string
}

var ruleIDInPolicy = strings.NewReplacer("k8s-guardian-", "")

func dryRun(t *testing.T, doc string) verdict {
	out, err := kubectl(t, doc, "apply", "--dry-run=server", "-f", "-")
	v := verdict{out: out}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "denied request") {
			v.denied = true
			name := strings.SplitN(strings.SplitN(l, "ValidatingAdmissionPolicy '", 2)[1], "'", 2)[0]
			v.by = strings.ToUpper(strings.SplitN(ruleIDInPolicy.Replace(name), "-", 2)[0])
		}
	}
	if err != nil && !v.denied {
		t.Fatalf("unexpected kubectl error:\n%s\n%s", out, doc)
	}
	return v
}

// engine returns the rule IDs k8s-guardian reports for doc (restricted to ids)
// with severity error.
func engine(t *testing.T, doc string, opts rules.Options, ids map[string]bool) []string {
	f, err := manifest.Parse([]byte(doc), "case.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, fd := range rules.Validate(f.Objects, opts) {
		if ids[fd.RuleID] && fd.Severity == rules.Error {
			out = append(out, fd.RuleID)
		}
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// compare asserts that the API server (with exported policies) and the
// in-process engine reach the same decision.
func compare(t *testing.T, name, doc string, opts rules.Options, ids map[string]bool) {
	t.Helper()
	want := engine(t, doc, opts, ids)
	got := dryRun(t, doc)
	switch {
	case len(want) == 0 && got.denied:
		t.Errorf("%s: engine allows it but the cluster denied it (%s):\n%s", name, got.by, got.out)
	case len(want) > 0 && !got.denied:
		t.Errorf("%s: engine reports %v but the cluster allowed it:\n%s", name, want, doc)
	case got.denied && !contains(want, got.by):
		t.Errorf("%s: cluster denied with %s, engine reports %v", name, got.by, want)
	default:
		t.Logf("✔ %s: engine %v, cluster denied=%v %s", name, want, got.denied, got.by)
	}
}

func workload(kind, ns, image, resources, sc, podExtra string) string {
	spec := fmt.Sprintf(`      containers:
        - name: c
          image: %s
          resources: %s
          securityContext: %s
%s`, image, resources, sc, podExtra)
	switch kind {
	case "Pod":
		return fmt.Sprintf("apiVersion: v1\nkind: Pod\nmetadata: {name: m, namespace: %s}\nspec:\n%s", ns, dedent(spec, 4))
	case "Deployment":
		return fmt.Sprintf("apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: m, namespace: %s}\nspec:\n  selector: {matchLabels: {app: m}}\n  template:\n    metadata: {labels: {app: m}}\n    spec:\n%s", ns, spec)
	}
	return fmt.Sprintf("apiVersion: batch/v1\nkind: CronJob\nmetadata: {name: m, namespace: %s}\nspec:\n  schedule: \"* * * * *\"\n  jobTemplate:\n    spec:\n      template:\n        metadata: {labels: {app: m}}\n        spec:\n          restartPolicy: Never\n%s", ns, indent(spec, 4))
}

func dedent(s string, n int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if len(l) >= n && strings.TrimSpace(l[:n]) == "" {
			lines[i] = l[n:]
		}
	}
	return strings.Join(lines, "\n")
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

func TestExportedBuiltinPoliciesMatchEngine(t *testing.T) {
	ns := "e2e-builtin"
	namespace(t, ns)
	ids := export.BuiltinIDs()
	applyPolicies(t, ids, nil, ns)
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	opts := rules.NewOptions("")

	okRes := "{requests: {cpu: 10m, memory: 8Mi}, limits: {memory: 8Mi}}"
	okSC := "{runAsNonRoot: true, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}"
	img := "registry.example.com/app:1.0"
	cases := []struct{ name, image, res, sc, extra string }{
		{"compliant", img, okRes, okSC, ""},
		{"no requests", img, "{limits: {memory: 8Mi}}", okSC, ""},
		{"no memory limit", img, "{requests: {cpu: 10m, memory: 8Mi}}", okSC, ""},
		{"runAsNonRoot false", img, okRes, "{runAsNonRoot: false, allowPrivilegeEscalation: false}", ""},
		{"runAsUser 0", img, okRes, "{runAsUser: 0, allowPrivilegeEscalation: false}", ""},
		{"runAsUser 1000", img, okRes, "{runAsUser: 1000, allowPrivilegeEscalation: false}", ""},
		{"pod-level runAsNonRoot", img, okRes, "{allowPrivilegeEscalation: false}", "      securityContext: {runAsNonRoot: true}\n"},
		{"privileged", img, okRes, "{runAsNonRoot: true, privileged: true}", ""},
		{":latest", "registry.example.com/app:latest", okRes, okSC, ""},
		{"no tag", "registry.example.com/app", okRes, okSC, ""},
		{"registry port, no tag", "registry.example.com:5000/app", okRes, okSC, ""},
		{"registry port + tag", "registry.example.com:5000/app:2", okRes, okSC, ""},
		{"digest", "registry.example.com/app@sha256:" + strings.Repeat("0", 64), okRes, okSC, ""},
		{"hostNetwork", img, okRes, okSC, "      hostNetwork: true\n"},
		{"hostPID", img, okRes, okSC, "      hostPID: true\n"},
		{"init container without requests", img, okRes, okSC,
			"      initContainers:\n        - name: init\n          image: " + img + "\n          securityContext: " + okSC + "\n"},
	}
	for _, kind := range []string{"Pod", "Deployment", "CronJob"} {
		for _, c := range cases {
			compare(t, kind+"/"+c.name, workload(kind, ns, c.image, c.res, c.sc, c.extra), opts, idSet)
		}
	}
}

// Warning and info rules export as Warn and Audit, which a dry run doesn't
// deny. With --action Deny they must deny exactly what the engine reports.
func TestExportedBuiltinPoliciesMatchEngineWithDeny(t *testing.T) {
	ns := "e2e-builtin-deny"
	namespace(t, ns)
	ids := export.BuiltinIDs()
	applyPoliciesWith(t, ids, nil, ns, export.Options{Action: "Deny"})
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	okRes := "{requests: {cpu: 10m, memory: 8Mi}, limits: {memory: 8Mi}}"
	okSC := "{runAsNonRoot: true, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}, seccompProfile: {type: RuntimeDefault}}"
	noSeccomp := "{runAsNonRoot: true, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}"
	img := "registry.example.com/app:1.0"
	noMount := "      automountServiceAccountToken: false\n"
	cases := []struct{ name, image, res, sc, extra string }{
		{"compliant", img, okRes, okSC, noMount},
		{"seccomp at pod level only", img, okRes, noSeccomp, noMount + "      securityContext: {seccompProfile: {type: Localhost, localhostProfile: p.json}}\n"},
		{"no seccomp", img, okRes, noSeccomp, noMount},
		{"seccomp Unconfined", img, okRes, "{runAsNonRoot: true, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}, seccompProfile: {type: Unconfined}}", noMount},
		{"token mounted by default", img, okRes, okSC, ""},
		{"token mounted explicitly", img, okRes, okSC, "      automountServiceAccountToken: true\n"},
		{"host port", img, okRes, okSC, "          ports: [{containerPort: 80, hostPort: 8080}]\n" + noMount},
		{"host port 0", img, okRes, okSC, "          ports: [{containerPort: 80, hostPort: 0}]\n" + noMount},
		{"init container with a host port", img, okRes, okSC,
			noMount + "      initContainers:\n        - name: init\n          image: " + img + "\n          resources: " + okRes + "\n          securityContext: " + okSC + "\n          ports: [{containerPort: 53, hostPort: 53}]\n"},
	}
	for _, kind := range []string{"Pod", "Deployment", "CronJob"} {
		for _, c := range cases {
			doc := workload(kind, ns, c.image, c.res, c.sc, c.extra)
			want := engineAll(t, doc, rules.NewOptions(""), idSet)
			got := dryRun(t, doc)
			switch {
			case len(want) == 0 && got.denied:
				t.Errorf("%s/%s: engine allows it but the cluster denied it (%s):\n%s", kind, c.name, got.by, got.out)
			case len(want) > 0 && !got.denied:
				t.Errorf("%s/%s: engine reports %v but the cluster allowed it:\n%s", kind, c.name, want, doc)
			case got.denied && !contains(want, got.by):
				t.Errorf("%s/%s: cluster denied with %s, engine reports %v", kind, c.name, got.by, want)
			default:
				t.Logf("✔ %s/%s: engine %v, cluster denied=%v %s", kind, c.name, want, got.denied, got.by)
			}
		}
	}
}
