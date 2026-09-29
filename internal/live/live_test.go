package live

import (
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

const app = `apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: prod}
spec:
  replicas: 3
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      serviceAccountName: api
      nodeSelector: {pool: general}
      containers:
        - name: api
          image: ghcr.io/x/api:1.0
          envFrom:
            - configMapRef: {name: api-config}
            - secretRef: {name: api-secrets}
          resources:
            requests: {cpu: "2", memory: 1Gi}
            limits: {memory: 1Gi}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: api-config, namespace: prod}
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata: {name: api, namespace: prod}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db, namespace: staging}
spec:
  serviceName: db
  selector: {matchLabels: {app: db}}
  template:
    metadata: {labels: {app: db}}
    spec:
      containers:
        - name: db
          image: postgres:16
          resources:
            requests: {cpu: 64, memory: 1Ti}
  volumeClaimTemplates:
    - metadata: {name: data}
      spec:
        storageClassName: fast-ssd
`

func fake() *cluster.Fake {
	return &cluster.Fake{
		GitVersion: "v1.30.2",
		APIs:       map[string]bool{"v1": true, "apps/v1": true},
		NodeList: []cluster.Node{
			{Name: "n1", Labels: map[string]string{"pool": "general"}, AllocCPU: "3920m", AllocMemory: "15Gi"},
			{Name: "n2", Labels: map[string]string{"pool": "gpu"}, AllocCPU: "32", AllocMemory: "120Gi"},
		},
		Namespaces: map[string]*cluster.Namespace{
			"prod": {Name: "prod", Exists: true,
				Quotas: []cluster.Quota{{Name: "compute",
					Hard: map[string]string{"requests.cpu": "8", "limits.memory": "16Gi", "pods": "20"},
					Used: map[string]string{"requests.cpu": "4", "limits.memory": "2Gi", "pods": "4"}}},
				Names: map[string]map[string]bool{"serviceaccount": {"api": true}},
			},
			"staging": {Name: "staging", Exists: true, Names: map[string]map[string]bool{}},
		},
		SCs: &cluster.StorageClasses{Names: map[string]bool{"standard": true}, Default: "standard"},
	}
}

func run(t *testing.T, c cluster.Cluster) map[string][]string {
	t.Helper()
	f, err := manifest.Parse([]byte(app), "app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res := Check(c, f.Objects, "", rules.NewOptions(""))
	got := map[string][]string{}
	for _, fd := range res.Findings {
		got[fd.RuleID] = append(got[fd.RuleID], fd.Resource+": "+fd.Message)
	}
	return got
}

func TestLiveChecks(t *testing.T) {
	got := run(t, fake())
	expect := map[string][]string{
		"LV001": {"ServiceMonitor", "CRD"},
		"LV003": {"StatefulSet/db", "64 CPU", "(n2) has 32"},
		"LV004": {"Deployment/api", "requests.cpu=6", "only has 4 left"},
		"LV006": {"Secret \"api-secrets\""},
		"LV007": {"fast-ssd"},
	}
	for id, parts := range expect {
		joined := strings.Join(got[id], "\n")
		for _, p := range parts {
			if !strings.Contains(joined, p) {
				t.Errorf("%s: expected %q in %q", id, p, joined)
			}
		}
	}
	if len(got["LV002"]) != 0 {
		t.Errorf("unexpected LV002: %v", got["LV002"])
	}
	if strings.Contains(strings.Join(got["LV006"], ""), "api-config") {
		t.Error("ConfigMap created by the same set must not be reported")
	}
}

func TestQuotaCountsOnlyDelta(t *testing.T) {
	c := fake()
	// The deployment already runs with 2 replicas of 2 CPU: +2 CPU fits into the 4 left.
	existing, _ := manifest.Parse([]byte(strings.Replace(strings.Split(app, "---")[0], "replicas: 3", "replicas: 2", 1)), "cluster")
	c.Objects = map[string]*manifest.Object{"Deployment/prod/api": existing.Objects[0]}
	if lv4 := run(t, c)["LV004"]; len(lv4) != 0 {
		t.Errorf("expected no quota finding for a +1 replica change, got %v", lv4)
	}
}

func TestMissingNamespace(t *testing.T) {
	c := fake()
	delete(c.Namespaces, "staging")
	if got := run(t, c)["LV002"]; len(got) != 1 || !strings.Contains(got[0], `"staging"`) {
		t.Errorf("expected LV002 for staging, got %v", got)
	}
}
