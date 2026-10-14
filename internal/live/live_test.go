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

// lv003 checks a workload with the given pod spec lines (indented by 6)
// on nodes, and returns the LV003 findings and all notes.
func lv003(t *testing.T, nodes []cluster.Node, kind, spec string) (findings []rules.Finding, notes []string) {
	t.Helper()
	doc := "apiVersion: apps/v1\nkind: " + kind + "\nmetadata: {name: app, namespace: prod}\nspec:\n  selector: {matchLabels: {app: app}}\n  template:\n    metadata: {labels: {app: app}}\n    spec:\n" +
		spec + "      containers:\n        - name: app\n          image: r.io/app:1\n          resources: {requests: {cpu: \"8\", memory: 1Gi}}\n"
	f, err := manifest.Parse([]byte(doc), "app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := fake()
	c.NodeList = nodes
	res := Check(c, f.Objects, "", rules.NewOptions(""))
	for _, fd := range res.Findings {
		if fd.RuleID == "LV003" {
			findings = append(findings, fd)
		}
	}
	return findings, res.Notes
}

func TestNodeFitSchedulingConstraints(t *testing.T) {
	small := cluster.Node{Name: "small", Labels: map[string]string{"pool": "general", "cores": "4"}, AllocCPU: "4", AllocMemory: "16Gi"}
	gpu := cluster.Node{Name: "gpu", Labels: map[string]string{"pool": "gpu", "cores": "32"}, AllocCPU: "32", AllocMemory: "128Gi",
		Taints: []cluster.Taint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}, {Key: "spot", Effect: "PreferNoSchedule"}}}
	big := cluster.Node{Name: "big", Labels: map[string]string{"pool": "general", "cores": "16"}, AllocCPU: "16", AllocMemory: "64Gi"}
	cordoned := big
	cordoned.Name, cordoned.Unschedulable = "cordoned", true
	affinity := func(terms string) string {
		return "      affinity:\n        nodeAffinity:\n          requiredDuringSchedulingIgnoredDuringExecution:\n            nodeSelectorTerms:\n" + terms
	}

	for _, tc := range []struct {
		name  string
		nodes []cluster.Node
		kind  string
		spec  string
		want  string // "" = no LV003 finding
	}{
		{name: "big enough node is tainted", nodes: []cluster.Node{small, gpu},
			want: "the largest matching node (small) has 4 allocatable; nodes with enough room are excluded (taint dedicated=gpu:NoSchedule: gpu)"},
		{name: "taint tolerated", nodes: []cluster.Node{small, gpu},
			spec: "      tolerations: [{key: dedicated, operator: Equal, value: gpu, effect: NoSchedule}]\n"},
		{name: "toleration without operator defaults to Equal", nodes: []cluster.Node{small, gpu},
			spec: "      tolerations: [{key: dedicated, value: gpu}]\n"},
		{name: "toleration with another value", nodes: []cluster.Node{small, gpu},
			spec: "      tolerations: [{key: dedicated, value: cpu}]\n", want: "taint dedicated=gpu:NoSchedule: gpu"},
		{name: "toleration for another effect", nodes: []cluster.Node{small, gpu},
			spec: "      tolerations: [{key: dedicated, operator: Exists, effect: NoExecute}]\n", want: "taint dedicated=gpu:NoSchedule: gpu"},
		{name: "Exists with a key", nodes: []cluster.Node{small, gpu},
			spec: "      tolerations: [{key: dedicated, operator: Exists}]\n"},
		{name: "Exists without a key tolerates everything", nodes: []cluster.Node{small, gpu},
			spec: "      tolerations: [{operator: Exists}]\n"},
		{name: "only tainted nodes", nodes: []cluster.Node{gpu},
			want: "no node accepts the pod (taint dedicated=gpu:NoSchedule: gpu): pods will stay Pending"},
		{name: "affinity excludes every node", nodes: []cluster.Node{small, big},
			spec: affinity("              - matchExpressions: [{key: pool, operator: In, values: [gpu]}]\n"),
			want: "no node accepts the pod (node affinity: small, big)"},
		{name: "affinity matches the small node only", nodes: []cluster.Node{small, big},
			spec: affinity("              - matchExpressions: [{key: cores, operator: Lt, values: [\"8\"]}]\n"),
			want: "the largest matching node (small) has 4 allocatable; nodes with enough room are excluded (node affinity: big)"},
		{name: "affinity matches the big node", nodes: []cluster.Node{small, big},
			spec: affinity("              - matchExpressions: [{key: cores, operator: Gt, values: [\"8\"]}, {key: pool, operator: NotIn, values: [gpu]}]\n")},
		{name: "terms are ORed", nodes: []cluster.Node{small, big},
			spec: affinity("              - matchExpressions: [{key: pool, operator: DoesNotExist}]\n              - matchFields: [{key: metadata.name, operator: In, values: [big]}]\n")},
		{name: "expressions in a term are ANDed", nodes: []cluster.Node{small, big},
			spec: affinity("              - matchExpressions: [{key: pool, operator: Exists}]\n                matchFields: [{key: metadata.name, operator: NotIn, values: [big]}]\n"),
			want: "nodes with enough room are excluded (node affinity: big)"},
		{name: "nodeSelector and affinity both apply", nodes: []cluster.Node{small, gpu},
			spec: "      nodeSelector: {pool: gpu}\n      tolerations: [{operator: Exists}]\n" + affinity("              - matchExpressions: [{key: cores, operator: Lt, values: [\"8\"]}]\n"),
			want: "no node accepts the pod (nodeSelector: small; node affinity: gpu)"},
		{name: "cordoned node", nodes: []cluster.Node{small, cordoned},
			want: "nodes with enough room are excluded (cordoned: cordoned)"},
		{name: "nodeName bypasses the scheduler", nodes: []cluster.Node{small, cordoned, gpu},
			spec: "      nodeName: gpu\n"},
		{name: "nodeName still needs NoExecute tolerated", nodes: []cluster.Node{small, {Name: "big", AllocCPU: "16", AllocMemory: "64Gi",
			Taints: []cluster.Taint{{Key: "evict", Effect: "NoExecute"}}}},
			spec: "      nodeName: big\n", want: "taint evict:NoExecute: big"},
		{name: "DaemonSets tolerate cordoned nodes", nodes: []cluster.Node{small, cordoned}, kind: "DaemonSet"},
		{name: "DaemonSet on no node", nodes: []cluster.Node{small, big}, kind: "DaemonSet",
			spec: "      nodeSelector: {pool: gpu}\n",
			want: "no node accepts the pod (nodeSelector: small, big): the DaemonSet runs no pods"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := tc.kind
			if kind == "" {
				kind = "Deployment"
			}
			got, notes := lv003(t, tc.nodes, kind, tc.spec)
			if len(notes) != 0 {
				t.Errorf("unexpected notes: %v", notes)
			}
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("expected no LV003, got %q", got[0].Message)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0].Message, tc.want) {
				t.Fatalf("expected one LV003 containing %q, got %v", tc.want, got)
			}
			if kind == "DaemonSet" && got[0].Severity != rules.Warning {
				t.Errorf("a DaemonSet that runs nowhere is a warning, got %v", got[0].Severity)
			}
		})
	}
}

func TestNodeFitUnknownSchedulingFieldsMatch(t *testing.T) {
	gpu := cluster.Node{Name: "gpu", Labels: map[string]string{"pool": "gpu"}, AllocCPU: "32", AllocMemory: "128Gi",
		Taints: []cluster.Taint{{Key: "nvidia.com/gpu-count", Value: "8", Effect: "NoSchedule"}}}
	spec := "      tolerations: [{key: nvidia.com/gpu-count, operator: Gt, value: \"4\"}]\n" +
		"      affinity:\n        nodeAffinity:\n          requiredDuringSchedulingIgnoredDuringExecution:\n            nodeSelectorTerms:\n" +
		"              - matchExpressions: [{key: pool, operator: Like, values: [g*]}]\n"
	got, notes := lv003(t, []cluster.Node{gpu}, "Deployment", spec)
	if len(got) != 0 {
		t.Errorf("fields that can't be evaluated must not exclude a node, got %q", got[0].Message)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{`LV003 Deployment/app: toleration operator "Gt" not evaluated`, `node affinity operator "Like" not evaluated`} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes miss %q:\n%s", want, joined)
		}
	}
}

func TestNodeFitFreeCapacity(t *testing.T) {
	nodes := []cluster.Node{
		{Name: "a", AllocCPU: "16", AllocMemory: "64Gi"},
		{Name: "b", AllocCPU: "16", AllocMemory: "64Gi"},
	}
	run := func(kind string, requested map[string]cluster.Requests, existing bool) []rules.Finding {
		doc := "apiVersion: apps/v1\nkind: " + kind + "\nmetadata: {name: app, namespace: prod}\nspec:\n  template:\n    spec:\n      containers:\n        - name: app\n          image: r.io/app:1\n          resources: {requests: {cpu: \"8\", memory: 1Gi}}\n"
		f, err := manifest.Parse([]byte(doc), "app.yaml")
		if err != nil {
			t.Fatal(err)
		}
		c := fake()
		c.NodeList, c.Requested = nodes, requested
		if existing {
			c.Objects = map[string]*manifest.Object{kind + "/prod/app": f.Objects[0]}
		}
		var out []rules.Finding
		for _, fd := range Check(c, f.Objects, "", rules.NewOptions("")).Findings {
			if fd.RuleID == "LV003" {
				out = append(out, fd)
			}
		}
		return out
	}
	full := map[string]cluster.Requests{"a": {MilliCPU: 12000, Bytes: 8 << 30}, "b": {MilliCPU: 9000, Bytes: 60 << 30}}
	got := run("Deployment", full, false)
	if len(got) != 1 || got[0].Severity != rules.Warning || !strings.Contains(got[0].Message, "none has that much free after the requests of running pods (most: b has 7 CPU free, a has 56Gi memory free)") {
		t.Errorf("full nodes: %+v", got)
	}
	if got := run("Deployment", map[string]cluster.Requests{"a": {MilliCPU: 12000}}, false); len(got) != 0 {
		t.Errorf("node b is free: %+v", got)
	}
	if got := run("Deployment", nil, false); len(got) != 0 {
		t.Errorf("without usage data there is nothing to report: %+v", got)
	}
	// Updating a StatefulSet replaces its pods in place: their requests are
	// already counted, so the check is skipped. A new one is checked.
	if got := run("StatefulSet", full, true); len(got) != 0 {
		t.Errorf("existing StatefulSet: %+v", got)
	}
	if got := run("StatefulSet", full, false); len(got) != 1 {
		t.Errorf("new StatefulSet: %+v", got)
	}
}
