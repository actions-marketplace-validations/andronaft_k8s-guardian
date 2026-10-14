//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/cli"
)

func run(args ...string) (int, string) {
	var out bytes.Buffer
	app := &cli.App{Name: "k8s-guardian", Version: "e2e", Stdout: &out, Stderr: &out}
	code := app.Run(args)
	return code, out.String()
}

const clusterState = `apiVersion: v1
kind: Namespace
metadata: {name: shop}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: default, namespace: shop}
---
apiVersion: v1
kind: ResourceQuota
metadata: {name: shop-compute, namespace: shop}
spec:
  hard: {requests.cpu: "12", requests.memory: 32Gi, pods: "30"}
---
apiVersion: v1
kind: LimitRange
metadata: {name: shop-limits, namespace: shop}
spec:
  limits:
    - type: Container
      max: {cpu: "2", memory: 4Gi}
      defaultRequest: {cpu: 100m, memory: 128Mi}
      default: {memory: 256Mi}
---
apiVersion: v1
kind: Node
metadata: {name: node-a, labels: {pool: general}}
---
apiVersion: v1
kind: Node
metadata: {name: node-gpu, labels: {pool: gpu}}
spec:
  taints: [{key: dedicated, value: gpu, effect: NoSchedule}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: orders-api, namespace: shop}
spec:
  replicas: 2
  selector: {matchLabels: {app: orders}}
  template:
    metadata: {labels: {app: orders}}
    spec:
      containers:
        - name: api
          image: ghcr.io/example/orders-api:2.2.0
          resources: {requests: {cpu: 500m, memory: 512Mi}, limits: {memory: 512Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: orders-api, namespace: shop}
spec:
  selector: {app: orders-api}
  ports: [{port: 443, targetPort: 8000}]
`

func TestLiveChecksAndDiffAgainstAPIServer(t *testing.T) {
	mustKubectl(t, clusterState, "apply", "-f", "-")
	// No controller-manager: set quota usage and node capacity by hand, and
	// remove the not-ready taint the API server puts on new nodes.
	mustKubectl(t, "", "taint", "nodes", "node-a", "node-gpu", "node.kubernetes.io/not-ready:NoSchedule-")
	mustKubectl(t, "", "patch", "resourcequota", "shop-compute", "-n", "shop", "--subresource=status", "--type=merge", "-p",
		`{"status":{"hard":{"requests.cpu":"12","requests.memory":"32Gi","pods":"30"},"used":{"requests.cpu":"3","requests.memory":"6Gi","pods":"5"}}}`)
	mustKubectl(t, "", "patch", "node", "node-a", "--subresource=status", "--type=merge", "-p",
		`{"status":{"allocatable":{"cpu":"3920m","memory":"15Gi","pods":"110"},"capacity":{"cpu":"4","memory":"16Gi","pods":"110"}}}`)
	mustKubectl(t, "", "patch", "node", "node-gpu", "--subresource=status", "--type=merge", "-p",
		`{"status":{"allocatable":{"cpu":"31","memory":"120Gi","pods":"110"},"capacity":{"cpu":"32","memory":"128Gi","pods":"110"}}}`)

	manifest := "../../examples/costly-app.yaml"
	code, out := run("check", "-f", manifest, "--live", "--skip", "KG003,KG005,KG006,KG007,KG008,KG009,KG013,KG014,KG016")
	if code != 1 {
		t.Errorf("check --live exit %d", code)
	}
	for _, want := range []string{
		`LV005  container "api": requests.cpu 4 exceeds LimitRange "shop-limits" max 2`,
		// node-gpu is big enough, but the pods don't tolerate its taint.
		"LV003  pod requests 4 CPU but the largest matching node (node-a) has 3920m allocatable; nodes with enough room are excluded (taint dedicated=gpu:NoSchedule: node-gpu)",
		`needs requests.cpu=15 more (4 replica(s)) but ResourceQuota "shop-compute" in namespace "shop" only has 9 left`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check --live misses %q:\n%s", want, out)
		}
	}

	code, out = run("diff", "-f", manifest)
	if code != 1 || !strings.Contains(out, "DF001  spec.selector is immutable") || !strings.Contains(out, "DF005  Service port 443 is removed") {
		t.Errorf("diff (exit %d):\n%s", code, out)
	}
	// ...and the API server agrees that the selector change is rejected.
	if out, err := kubectl(t, "", "apply", "--dry-run=server", "-f", manifest); err == nil || !strings.Contains(out, "field is immutable") {
		t.Errorf("expected the API server to reject the selector change:\n%s", out)
	}

	code, out = run("audit", "deployment/orders-api", "-n", "shop", "-o", "json")
	if code != 1 || !strings.Contains(out, `"ruleId": "KG003"`) {
		t.Errorf("audit (exit %d):\n%s", code, out)
	}

	// A LimitRange violation predicted by LV005 is what the API server enforces.
	pod := "apiVersion: v1\nkind: Pod\nmetadata: {name: big, namespace: shop}\nspec:\n  containers:\n  - name: c\n    image: r.io/a:1\n    resources: {requests: {cpu: '4'}, limits: {cpu: '4'}}\n"
	if out, err := kubectl(t, pod, "apply", "--dry-run=server", "-f", "-"); err == nil || !strings.Contains(out, "maximum cpu usage per Container is 2") {
		t.Errorf("expected LimitRange rejection:\n%s", out)
	}
}

// A pod fits a node's allocatable CPU, but a pod bound to that node
// already requests most of it.
func TestLiveFreeCapacity(t *testing.T) {
	namespace(t, "busy")
	mustKubectl(t, `apiVersion: v1
kind: Node
metadata: {name: node-busy, labels: {pool: busy}}
spec:
  taints: [{key: dedicated, value: busy, effect: NoSchedule}]
---
apiVersion: v1
kind: Pod
metadata: {name: hog, namespace: busy}
spec:
  nodeName: node-busy
  containers: [{name: hog, image: r.io/hog:1, resources: {requests: {cpu: "2", memory: 1Gi}}}]
`, "apply", "-f", "-")
	mustKubectl(t, "", "taint", "nodes", "node-busy", "node.kubernetes.io/not-ready:NoSchedule-")
	mustKubectl(t, "", "patch", "node", "node-busy", "--subresource=status", "--type=merge", "-p",
		`{"status":{"allocatable":{"cpu":"3","memory":"8Gi","pods":"110"},"capacity":{"cpu":"3","memory":"8Gi","pods":"110"}}}`)

	app := `apiVersion: apps/v1
kind: Deployment
metadata: {name: app, namespace: busy}
spec:
  selector: {matchLabels: {app: app}}
  template:
    metadata: {labels: {app: app}}
    spec:
      nodeSelector: {pool: busy}
      tolerations: [{key: dedicated, value: busy}]
      containers: [{name: app, image: r.io/app:1, resources: {requests: {cpu: "%s", memory: 1Gi}}}]
`
	path := filepath.Join(t.TempDir(), "app.yaml")
	for cpu, want := range map[string]string{
		"2":    "LV003  pod requests 2 CPU and 1Gi memory: matching nodes are big enough, but none has that much free after the requests of running pods (most: node-busy has 1 CPU free",
		"500m": "",
	} {
		if err := os.WriteFile(path, []byte(fmt.Sprintf(app, cpu)), 0o600); err != nil {
			t.Fatal(err)
		}
		_, out := run("check", "-f", path, "--live", "--skip", "KG003,KG005,KG006,KG007,KG008,KG009,KG010,KG013,KG014,KG015,KG002")
		if want == "" && strings.Contains(out, "LV003") || want != "" && !strings.Contains(out, want) {
			t.Errorf("cpu %s: want %q in:\n%s", cpu, want, out)
		}
		if strings.Contains(out, "free node capacity") {
			t.Errorf("listing pods failed:\n%s", out)
		}
	}
}
