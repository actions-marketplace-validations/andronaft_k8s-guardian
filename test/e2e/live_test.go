//go:build e2e

package e2e

import (
	"bytes"
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
	// No controller-manager: set quota usage and node capacity by hand.
	mustKubectl(t, "", "patch", "resourcequota", "shop-compute", "-n", "shop", "--subresource=status", "--type=merge", "-p",
		`{"status":{"hard":{"requests.cpu":"12","requests.memory":"32Gi","pods":"30"},"used":{"requests.cpu":"3","requests.memory":"6Gi","pods":"5"}}}`)
	mustKubectl(t, "", "patch", "node", "node-a", "--subresource=status", "--type=merge", "-p",
		`{"status":{"allocatable":{"cpu":"3920m","memory":"15Gi","pods":"110"},"capacity":{"cpu":"4","memory":"16Gi","pods":"110"}}}`)

	manifest := "../../examples/costly-app.yaml"
	code, out := run("check", "-f", manifest, "--live", "--skip", "KG003,KG005,KG006,KG007,KG008,KG009,KG013,KG014,KG016")
	if code != 1 {
		t.Errorf("check --live exit %d", code)
	}
	for _, want := range []string{
		`LV005  container "api": requests.cpu 4 exceeds LimitRange "shop-limits" max 2`,
		"LV003  pod requests 4 CPU but the largest matching node (node-a) has 3920m allocatable",
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
