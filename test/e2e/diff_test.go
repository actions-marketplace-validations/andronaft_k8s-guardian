//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// injectProxy stands in for a sidecar-injecting webhook.
const injectProxy = `apiVersion: admissionregistration.k8s.io/v1
kind: MutatingAdmissionPolicy
metadata: {name: e2e-inject-proxy}
spec:
  failurePolicy: Fail
  reinvocationPolicy: Never
  matchConstraints:
    resourceRules: [{apiGroups: [apps], apiVersions: [v1], operations: [CREATE, UPDATE], resources: [deployments]}]
  mutations:
    - patchType: ApplyConfiguration
      applyConfiguration:
        expression: >
          Object{spec: Object.spec{template: Object.spec.template{spec: Object.spec.template.spec{
            containers: [Object.spec.template.spec.containers{name: "proxy", image: "r.io/proxy:2.0"}]}}}}
---
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingAdmissionPolicyBinding
metadata: {name: e2e-inject-proxy}
spec:
  policyName: e2e-inject-proxy
  matchResources:
    namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: ssa}}
`

const ssaLive = `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: ssa}
spec:
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers: [{name: web, image: "r.io/web:1.0"}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: ssa}
spec:
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      containers: [{name: api, image: "r.io/api:1.0"}]
`

func TestDiffServerSide(t *testing.T) {
	namespace(t, "ssa")
	mustKubectl(t, ssaLive, "apply", "-f", "-")
	// Policies come after the workloads, so the live objects have no proxy.
	mustKubectl(t, injectProxy, "apply", "-f", "-")
	applyPolicies(t, []string{"KG010"}, nil, "ssa")

	// web is unchanged, api changes its selector, latest is new.
	local := ssaLive[:strings.Index(ssaLive, "---")] + `---
apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: ssa}
spec:
  selector: {matchLabels: {app: api-v2}}
  template:
    metadata: {labels: {app: api-v2}}
    spec:
      containers: [{name: api, image: "r.io/api:1.0"}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: latest, namespace: ssa}
spec:
  selector: {matchLabels: {app: latest}}
  template:
    metadata: {labels: {app: latest}}
    spec:
      containers: [{name: app, image: "r.io/app:latest"}]
`
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out := run("diff", "-f", path)
	if code != 1 || strings.Contains(out, "DF008") || strings.Contains(out, "by the API server") {
		t.Errorf("without --server-side (exit %d):\n%s", code, out)
	}

	code, out = run("diff", "-f", path, "--server-side")
	for _, want := range []string{
		// The policy injects a proxy: applying web restarts its pods.
		"spec.template.spec.containers[proxy]: <none> → image: r.io/proxy:2.0",
		"(by the API server)",
		"DF007  the API server changes the pod template (spec.template.spec.containers[proxy]): applying triggers a rolling restart",
		// The client-side check and the API server agree on the selector.
		"DF001  spec.selector is immutable",
		"DF008  the API server rejects it in a dry run: spec.selector: Invalid value",
		"field is immutable: apply will fail",
		// The exported KG010 policy denies the new Deployment.
		"DF008  the API server rejects it in a dry run: ValidatingAdmissionPolicy 'k8s-guardian-kg010",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff --server-side misses %q (exit %d):\n%s", want, code, out)
		}
	}
	if code != 1 {
		t.Errorf("diff --server-side exit %d", code)
	}

	// A dry run stores nothing.
	if out, err := kubectl(t, "", "get", "deployment", "latest", "-n", "ssa"); err == nil {
		t.Errorf("the dry run created a Deployment:\n%s", out)
	}
	if got := mustKubectl(t, "", "get", "deployment", "web", "-n", "ssa", "-o", "jsonpath={.spec.template.spec.containers[*].name}"); got != "web" {
		t.Errorf("the dry run changed the live Deployment: containers %q", got)
	}
}

func TestDiffServerSidePodSecurity(t *testing.T) {
	for ns, level := range map[string]string{"psa-enforce": "enforce", "psa-warn": "warn"} {
		namespace(t, ns)
		mustKubectl(t, "", "label", "--overwrite", "namespace", ns, "pod-security.kubernetes.io/"+level+"=restricted")
	}
	deploy := func(name, ns, securityContext string) string {
		return `apiVersion: apps/v1
kind: Deployment
metadata: {name: ` + name + `, namespace: ` + ns + `}
spec:
  selector: {matchLabels: {app: ` + name + `}}
  template:
    metadata: {labels: {app: ` + name + `}}
    spec:
      securityContext: {runAsNonRoot: true, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: app
          image: r.io/app:1.0
          securityContext: ` + securityContext + `
---
`
	}
	const loose, restricted = "{}", "{allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}"
	path := filepath.Join(t.TempDir(), "psa.yaml")
	manifests := deploy("loose", "psa-enforce", loose) + deploy("warned", "psa-warn", loose) + deploy("compliant", "psa-enforce", restricted)
	if err := os.WriteFile(path, []byte(manifests), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := run("diff", "-f", path, "--server-side")
	for _, want := range []string{
		// The API server accepts the Deployment but would reject its pods.
		`DF009  pods will be rejected: namespace "psa-enforce" enforces Pod Security "restricted": allowPrivilegeEscalation != false`,
		`DF009  the API server warns: would violate PodSecurity "restricted:latest"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff --server-side misses %q (exit %d):\n%s", want, code, out)
		}
	}
	if code != 1 || strings.Count(out, "DF009") != 2 {
		t.Errorf("expected exactly two DF009 findings and exit 1, got exit %d:\n%s", code, out)
	}
}
