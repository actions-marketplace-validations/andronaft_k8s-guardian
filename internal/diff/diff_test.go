package diff

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

const liveYAML = `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: prod}
spec:
  replicas: 2
  strategy: {type: Recreate}
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      dnsPolicy: ClusterFirst
      containers:
        - name: web
          image: nginx:1.25
          ports: [{name: http, containerPort: 8080}]
          resources: {requests: {cpu: 500m}}
        - name: sidecar
          image: envoy:1.30
---
apiVersion: v1
kind: Service
metadata: {name: web, namespace: prod}
spec:
  clusterIP: 10.0.0.10
  selector: {app: web}
  ports: [{name: http, port: 80, targetPort: http}]
`

const localYAML = `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: prod}
spec:
  replicas: 3
  selector: {matchLabels: {app: web, tier: frontend}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - name: web
          image: nginx:1.27
          securityContext: {}
          resources: {requests: {cpu: "0.5"}}
---
apiVersion: v1
kind: Service
metadata: {name: web, namespace: prod}
spec:
  selector: {app: web}
  ports: [{name: http, port: 8080, targetPort: http}]
---
apiVersion: v1
kind: ConfigMap
metadata: {name: new-cm, namespace: prod}
`

func TestDiff(t *testing.T) {
	lf, _ := manifest.Parse([]byte(liveYAML), "cluster")
	objs := map[string]*manifest.Object{}
	for _, o := range lf.Objects {
		objs[o.Kind()+"/prod/"+o.Name()] = o
	}
	c := &cluster.Fake{GitVersion: "v1.29.0", Objects: objs}
	f, _ := manifest.Parse([]byte(localYAML), "app.yaml")
	res := Run(c, f.Objects, "", rules.NewOptions(""))

	byRes := map[string]*Object{}
	for _, o := range res.Objects {
		byRes[o.Resource] = o
	}
	if byRes["ConfigMap/new-cm"].Status != New {
		t.Errorf("ConfigMap should be new")
	}
	dep := byRes["Deployment/web"]
	var paths []string
	for _, c := range dep.Changes {
		paths = append(paths, c.Path)
	}
	all := strings.Join(paths, "\n")
	for _, want := range []string{"spec.replicas", "spec.selector.matchLabels.tier", "spec.template.spec.containers[web].image", "spec.template.spec.containers[sidecar]"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing change %q in:\n%s", want, all)
		}
	}
	for _, unwanted := range []string{"resources", "securityContext", "dnsPolicy"} {
		if strings.Contains(all, unwanted) {
			t.Errorf("unexpected change %q in:\n%s", unwanted, all)
		}
	}
	msgs := ""
	for _, fd := range res.Findings() {
		msgs += fd.RuleID + " " + fd.Message + "\n"
	}
	for _, want := range []string{"DF001 spec.selector is immutable", "DF003 selector requires tier=frontend", "DF006 container \"sidecar\" is removed", "DF006 pod template changes with strategy Recreate", "DF007 image change: web: nginx:1.25 -> nginx:1.27", "DF005 Service port 80 -> 8080"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing finding %q in:\n%s", want, msgs)
		}
	}
	if strings.Contains(msgs, "clusterIP") {
		t.Error("clusterIP not set locally must not be reported")
	}
}

func TestRemovedAPIOnCluster(t *testing.T) {
	f, _ := manifest.Parse([]byte("apiVersion: batch/v1beta1\nkind: CronJob\nmetadata: {name: c}\n"), "c.yaml")
	res := Run(&cluster.Fake{GitVersion: "v1.27.1"}, f.Objects, "default", rules.NewOptions(""))
	if fs := res.Findings(); len(fs) != 1 || fs[0].RuleID != "DF004" {
		t.Errorf("expected DF004, got %+v", fs)
	}
	res = Run(&cluster.Fake{GitVersion: "v1.24.0"}, f.Objects, "default", rules.NewOptions(""))
	if fs := res.Findings(); len(fs) != 0 {
		t.Errorf("CronJob batch/v1beta1 is still served on 1.24, got %+v", fs)
	}
}

func TestSecretValuesAreNeverShown(t *testing.T) {
	live, _ := manifest.Parse([]byte("apiVersion: v1\nkind: Secret\nmetadata: {name: db, namespace: prod}\ndata: {password: UHJvZFBhc3N3MHJkIQ==, user: YWRtaW4=}\n"), "cluster")
	local, _ := manifest.Parse([]byte("apiVersion: v1\nkind: Secret\nmetadata: {name: db, namespace: prod}\ndata: {password: Y2hhbmdlbWU=, user: YWRtaW4=, token: bmV3}\n"), "s.yaml")
	c := &cluster.Fake{Objects: map[string]*manifest.Object{"Secret/prod/db": live.Objects[0]}}
	res := Run(c, local.Objects, "", rules.NewOptions(""))
	b, _ := json.Marshal(res)
	for _, leak := range []string{"UHJvZFBhc3N3MHJkIQ", "Y2hhbmdlbWU", "bmV3"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("secret value %s leaked: %s", leak, b)
		}
	}
	if len(res.Objects[0].Changes) != 2 {
		t.Errorf("changes to password and token must still be detected: %+v", res.Objects[0].Changes)
	}
}
