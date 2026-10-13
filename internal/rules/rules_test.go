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

func TestHighAvailabilityHonoursHPA(t *testing.T) {
	const deploy = `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: shop}
spec:
  template:
    spec:
      containers: [{name: web, image: nginx:1.27}]
`
	hpa := func(min string) string {
		return deploy + `---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: web, namespace: shop}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: web}
  ` + min + `
  maxReplicas: 10
`
	}
	cases := []struct{ name, doc, want string }{
		{"no replicas, no HPA", deploy, "spec.replicas is not set"},
		{"HPA with minReplicas 3", hpa("minReplicas: 3"), ""},
		{"HPA with minReplicas 1", hpa("minReplicas: 1"), "minReplicas 1"},
		{"HPA without minReplicas", hpa(""), "minReplicas 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(messages(validateDoc(t, c.doc), "KG015"), "; ")
			if c.want == "" && got != "" || !strings.Contains(got, c.want) {
				t.Errorf("KG015 = %q, want %q", got, c.want)
			}
		})
	}
}

func TestArgoRollout(t *testing.T) {
	const rollout = `apiVersion: %s
kind: Rollout
metadata: {name: web}
spec:
  replicas: 3
  template:
    spec:
      containers: [{name: web, image: nginx:1.27}]
`
	got := ids(validateDoc(t, fmt.Sprintf(rollout, "argoproj.io/v1alpha1")))
	for _, id := range []string{"KG001", "KG002", "KG003", "KG008"} {
		if got[id] == 0 {
			t.Errorf("expected %s for an Argo Rollout, got %v", id, got)
		}
	}
	if got["KG015"] != 0 {
		t.Errorf("3 replicas reported as not highly available: %v", got)
	}
	// Another CRD that happens to be called Rollout is not a workload.
	if got := ids(validateDoc(t, fmt.Sprintf(rollout, "example.com/v1"))); len(got) != 0 {
		t.Errorf("findings for a foreign Rollout kind: %v", got)
	}
}

func TestRBACWildcard(t *testing.T) {
	fs := validateDoc(t, `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: broad}
rules:
  - {apiGroups: [""], resources: ["*"], verbs: ["*"]}
  - {apiGroups: [apps], resources: [deployments], verbs: [get, list]}
  - {apiGroups: [""], resources: [pods], verbs: ["*"]}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: narrow}
rules:
  - {apiGroups: [""], resources: [configmaps], verbs: [get]}
`)
	got := messages(fs, "KG018")
	if len(got) != 1 || got[0] != `grants "*" verbs in rules[0,2] and "*" resources in rules[0]` {
		t.Errorf("KG018 = %q", got)
	}
}

// TestRemovedAPIsMatchOfficialGuide lists every removal in the official
// Deprecated API Migration Guide
// (https://kubernetes.io/docs/reference/using-api/deprecation-guide/, as of
// kubernetes/website 78de91b, 2026-10-12; its newest section is v1.32).
// When the guide gets a new release section, add its rows here and to
// removedAPIs.
func TestRemovedAPIsMatchOfficialGuide(t *testing.T) {
	guide := []struct{ apiVersion, kind, removedIn string }{
		{"flowcontrol.apiserver.k8s.io/v1beta3", "FlowSchema", "1.32"},
		{"flowcontrol.apiserver.k8s.io/v1beta3", "PriorityLevelConfiguration", "1.32"},
		{"flowcontrol.apiserver.k8s.io/v1beta2", "FlowSchema", "1.29"},
		{"flowcontrol.apiserver.k8s.io/v1beta2", "PriorityLevelConfiguration", "1.29"},
		{"storage.k8s.io/v1beta1", "CSIStorageCapacity", "1.27"},
		{"flowcontrol.apiserver.k8s.io/v1beta1", "FlowSchema", "1.26"},
		{"flowcontrol.apiserver.k8s.io/v1beta1", "PriorityLevelConfiguration", "1.26"},
		{"autoscaling/v2beta2", "HorizontalPodAutoscaler", "1.26"},
		{"batch/v1beta1", "CronJob", "1.25"},
		{"discovery.k8s.io/v1beta1", "EndpointSlice", "1.25"},
		{"events.k8s.io/v1beta1", "Event", "1.25"},
		{"autoscaling/v2beta1", "HorizontalPodAutoscaler", "1.25"},
		{"policy/v1beta1", "PodDisruptionBudget", "1.25"},
		{"policy/v1beta1", "PodSecurityPolicy", "1.25"},
		{"node.k8s.io/v1beta1", "RuntimeClass", "1.25"},
		{"admissionregistration.k8s.io/v1beta1", "MutatingWebhookConfiguration", "1.22"},
		{"admissionregistration.k8s.io/v1beta1", "ValidatingWebhookConfiguration", "1.22"},
		{"apiextensions.k8s.io/v1beta1", "CustomResourceDefinition", "1.22"},
		{"apiregistration.k8s.io/v1beta1", "APIService", "1.22"},
		{"authentication.k8s.io/v1beta1", "TokenReview", "1.22"},
		{"authorization.k8s.io/v1beta1", "LocalSubjectAccessReview", "1.22"},
		{"authorization.k8s.io/v1beta1", "SelfSubjectAccessReview", "1.22"},
		{"authorization.k8s.io/v1beta1", "SubjectAccessReview", "1.22"},
		{"authorization.k8s.io/v1beta1", "SelfSubjectRulesReview", "1.22"},
		{"certificates.k8s.io/v1beta1", "CertificateSigningRequest", "1.22"},
		{"coordination.k8s.io/v1beta1", "Lease", "1.22"},
		{"extensions/v1beta1", "Ingress", "1.22"},
		{"networking.k8s.io/v1beta1", "Ingress", "1.22"},
		{"networking.k8s.io/v1beta1", "IngressClass", "1.22"},
		{"rbac.authorization.k8s.io/v1beta1", "ClusterRole", "1.22"},
		{"rbac.authorization.k8s.io/v1beta1", "ClusterRoleBinding", "1.22"},
		{"rbac.authorization.k8s.io/v1beta1", "Role", "1.22"},
		{"rbac.authorization.k8s.io/v1beta1", "RoleBinding", "1.22"},
		{"scheduling.k8s.io/v1beta1", "PriorityClass", "1.22"},
		{"storage.k8s.io/v1beta1", "CSIDriver", "1.22"},
		{"storage.k8s.io/v1beta1", "CSINode", "1.22"},
		{"storage.k8s.io/v1beta1", "StorageClass", "1.22"},
		{"storage.k8s.io/v1beta1", "VolumeAttachment", "1.22"},
		{"extensions/v1beta1", "NetworkPolicy", "1.16"},
		{"extensions/v1beta1", "DaemonSet", "1.16"},
		{"apps/v1beta2", "DaemonSet", "1.16"},
		{"extensions/v1beta1", "Deployment", "1.16"},
		{"apps/v1beta1", "Deployment", "1.16"},
		{"apps/v1beta2", "Deployment", "1.16"},
		{"apps/v1beta1", "StatefulSet", "1.16"},
		{"apps/v1beta2", "StatefulSet", "1.16"},
		{"extensions/v1beta1", "ReplicaSet", "1.16"},
		{"apps/v1beta1", "ReplicaSet", "1.16"},
		{"apps/v1beta2", "ReplicaSet", "1.16"},
		{"extensions/v1beta1", "PodSecurityPolicy", "1.16"},
	}
	for _, g := range guide {
		r, ok := rules.LookupRemovedAPI(g.apiVersion, g.kind)
		if !ok {
			t.Errorf("%s %s (removed in %s) is missing", g.apiVersion, g.kind, g.removedIn)
			continue
		}
		if r.RemovedIn != g.removedIn {
			t.Errorf("%s %s: removed in %s, the guide says %s", g.apiVersion, g.kind, r.RemovedIn, g.removedIn)
		}
	}
}

// Beta versions gain kinds after older kinds of the same version were
// removed. These are served today and must not be reported.
func TestRemovedAPIsDoNotFlagCurrentBetaKinds(t *testing.T) {
	for _, gk := range [][2]string{
		{"admissionregistration.k8s.io/v1beta1", "ValidatingAdmissionPolicy"},
		{"admissionregistration.k8s.io/v1beta1", "ValidatingAdmissionPolicyBinding"},
		{"admissionregistration.k8s.io/v1beta1", "MutatingAdmissionPolicy"},
		{"certificates.k8s.io/v1beta1", "ClusterTrustBundle"},
		{"certificates.k8s.io/v1beta1", "PodCertificateRequest"},
		{"coordination.k8s.io/v1beta1", "LeaseCandidate"},
		{"scheduling.k8s.io/v1beta1", "Workload"},
		{"authentication.k8s.io/v1beta1", "SelfSubjectReview"},
	} {
		if r, ok := rules.LookupRemovedAPI(gk[0], gk[1]); ok {
			t.Errorf("%s %s reported as removed in %s", gk[0], gk[1], r.RemovedIn)
		}
	}
}

// generated returns n apps (Deployment, Service, HPA) spread over 20
// namespaces, the shape that made the cross-resource rules quadratic.
func generated(b *testing.B, n int) []*manifest.Object {
	b.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `apiVersion: apps/v1
kind: Deployment
metadata: {name: app%[1]d, namespace: ns%[2]d}
spec:
  selector: {matchLabels: {app: app%[1]d}}
  template:
    metadata: {labels: {app: app%[1]d}}
    spec:
      containers: [{name: app, image: r.io/app:1, ports: [{name: http, containerPort: 8080}]}]
---
apiVersion: v1
kind: Service
metadata: {name: app%[1]d, namespace: ns%[2]d}
spec: {selector: {app: app%[1]d}, ports: [{port: 80, targetPort: http}]}
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: app%[1]d, namespace: ns%[2]d}
spec: {scaleTargetRef: {kind: Deployment, name: app%[1]d}, minReplicas: 2, maxReplicas: 5}
---
`, i, i%20)
	}
	f, err := manifest.Parse([]byte(sb.String()), "bench.yaml")
	if err != nil {
		b.Fatal(err)
	}
	return f.Objects
}

func BenchmarkValidate(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		objs := generated(b, n/3)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			for b.Loop() {
				rules.Validate(objs, rules.NewOptions(""))
			}
		})
	}
}
