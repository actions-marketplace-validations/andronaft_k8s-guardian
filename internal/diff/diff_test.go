package diff

import (
	"encoding/json"
	"fmt"
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
	res := Run(c, f.Objects, "", rules.NewOptions(""), false)

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
	res := Run(&cluster.Fake{GitVersion: "v1.27.1"}, f.Objects, "default", rules.NewOptions(""), false)
	if fs := res.Findings(); len(fs) != 1 || fs[0].RuleID != "DF004" {
		t.Errorf("expected DF004, got %+v", fs)
	}
	res = Run(&cluster.Fake{GitVersion: "v1.24.0"}, f.Objects, "default", rules.NewOptions(""), false)
	if fs := res.Findings(); len(fs) != 0 {
		t.Errorf("CronJob batch/v1beta1 is still served on 1.24, got %+v", fs)
	}
}

func TestSecretValuesAreNeverShown(t *testing.T) {
	live, _ := manifest.Parse([]byte("apiVersion: v1\nkind: Secret\nmetadata: {name: db, namespace: prod}\ndata: {password: UHJvZFBhc3N3MHJkIQ==, user: YWRtaW4=}\n"), "cluster")
	local, _ := manifest.Parse([]byte("apiVersion: v1\nkind: Secret\nmetadata: {name: db, namespace: prod}\ndata: {password: Y2hhbmdlbWU=, user: YWRtaW4=, token: bmV3}\n"), "s.yaml")
	c := &cluster.Fake{Objects: map[string]*manifest.Object{"Secret/prod/db": live.Objects[0]}}
	res := Run(c, local.Objects, "", rules.NewOptions(""), false)
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

func TestServerSideDryRun(t *testing.T) {
	lf, _ := manifest.Parse([]byte(liveYAML), "cluster")
	objs := map[string]*manifest.Object{}
	for _, o := range lf.Objects {
		objs[o.Kind()+"/prod/"+o.Name()] = o
	}
	local := `apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: prod}
spec:
  replicas: 3
  selector: {matchLabels: {app: web}}
---
apiVersion: v1
kind: Service
metadata: {name: web, namespace: prod}
spec:
  selector: {app: web}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: v1
kind: ConfigMap
metadata: {name: new-cm, namespace: prod}
---
apiVersion: v1
kind: Secret
metadata: {name: creds, namespace: prod}
data: {password: aHVudGVyMg==}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: elsewhere, namespace: staging}
`
	var calls []string
	c := &cluster.Fake{Objects: objs, DryRun: func(o *manifest.Object, ns string) (*cluster.DryRun, error) {
		calls = append(calls, o.Ref()+"@"+ns)
		switch o.Ref() {
		case "Deployment/web":
			// A mutating webhook injects a proxy into the live object.
			dry, _ := manifest.Parse([]byte(strings.Replace(strings.Split(liveYAML, "---")[0], "replicas: 2", "replicas: 3", 1)+
				"        - name: proxy\n          image: envoy:1.31\n"), "dry-run")
			return &cluster.DryRun{Object: dry.Objects[0]}, nil
		case "Service/web":
			return nil, &cluster.Rejection{Message: "ValidatingAdmissionPolicy 'ports' with binding 'ports' denied request: no port 80"}
		case "Secret/creds":
			return nil, &cluster.Rejection{Message: `data[password]: Invalid value: "hunter2"`}
		case "ConfigMap/elsewhere":
			return nil, fmt.Errorf("%w: namespaces \"staging\" not found", cluster.ErrNotFound)
		}
		return nil, fmt.Errorf("%w: cannot patch resource", cluster.ErrForbidden)
	}}
	f, _ := manifest.Parse([]byte(local), "app.yaml")

	if res := Run(c, f.Objects, "", rules.NewOptions(""), false); len(calls) != 0 || len(res.Notes) != 0 {
		t.Fatalf("without --server-side nothing is sent: calls %v, notes %v", calls, res.Notes)
	}
	res := Run(c, f.Objects, "", rules.NewOptions(""), true)
	if want := "Deployment/web@prod Service/web@prod ConfigMap/new-cm@prod Secret/creds@prod ConfigMap/elsewhere@staging"; strings.Join(calls, " ") != want {
		t.Errorf("dry runs %v, want %s", calls, want)
	}

	byRes := map[string]*Object{}
	for _, o := range res.Objects {
		byRes[o.Resource] = o
	}
	var server []string
	for _, ch := range byRes["Deployment/web"].Changes {
		if ch.Server {
			server = append(server, ch.Path)
		}
		if ch.Path == "spec.replicas" && ch.Server {
			t.Error("a change the manifest makes itself must not be attributed to the server")
		}
	}
	if strings.Join(server, ",") != "spec.template.spec.containers[proxy]" {
		t.Errorf("server changes %v", server)
	}
	msgs := ""
	for _, fd := range res.Findings() {
		msgs += fd.Resource + " " + fd.RuleID + " " + fd.Message + "\n"
	}
	for _, want := range []string{
		"Deployment/web DF007 the API server changes the pod template (spec.template.spec.containers[proxy]): applying triggers a rolling restart",
		"Service/web DF008 the API server rejects it in a dry run: ValidatingAdmissionPolicy 'ports' with binding 'ports' denied request: no port 80: apply will fail",
		"Secret/creds DF008 the API server rejects it in a dry run: details hidden",
	} {
		if !strings.Contains(msgs, want) {
			t.Errorf("missing %q in:\n%s", want, msgs)
		}
	}
	if strings.Contains(msgs, "hunter2") {
		t.Error("a Secret's rejection message must not be shown")
	}
	if strings.Count(msgs, "DF008") != 2 {
		t.Errorf("RBAC and missing namespaces are not rejections:\n%s", msgs)
	}
	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{
		"server-side dry run of ConfigMap objects skipped: RBAC doesn't allow it",
		`server-side dry run of ConfigMap/elsewhere skipped: namespaces "staging" not found`,
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing note %q in:\n%s", want, notes)
		}
	}
}

func TestServerWarnings(t *testing.T) {
	const deploy = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web, namespace: %s}\nspec:\n  template:\n    spec:\n      containers: [{name: web, image: r.io/web:1}]\n"
	psa := `would violate PodSecurity "restricted:latest": allowPrivilegeEscalation != false (container "web" must set securityContext.allowPrivilegeEscalation=false)`
	c := &cluster.Fake{
		Labels: map[string]map[string]string{
			"strict":   {"pod-security.kubernetes.io/enforce": "restricted"},
			"baseline": {"pod-security.kubernetes.io/enforce": "baseline", "pod-security.kubernetes.io/warn": "restricted"},
			"warned":   {"pod-security.kubernetes.io/warn": "restricted"},
		},
		DryRun: func(o *manifest.Object, ns string) (*cluster.DryRun, error) {
			w := []string{psa}
			if ns == "warned" {
				w = append(w, "apps/v1beta1 Deployment is deprecated")
			}
			return &cluster.DryRun{Object: o, Warnings: w}, nil
		},
	}
	for ns, want := range map[string][]string{
		"strict":   {`error DF009 pods will be rejected: namespace "strict" enforces Pod Security "restricted": allowPrivilegeEscalation != false`},
		"baseline": {`warning DF009 the API server warns: would violate PodSecurity "restricted:latest"`},
		"warned":   {`warning DF009 the API server warns: would violate PodSecurity`, `warning DF009 the API server warns: apps/v1beta1 Deployment is deprecated`},
		"missing":  {`warning DF009 the API server warns: would violate PodSecurity`},
	} {
		f, _ := manifest.Parse([]byte(fmt.Sprintf(deploy, ns)), "app.yaml")
		res := Run(c, f.Objects, "", rules.NewOptions(""), true)
		var got []string
		for _, fd := range res.Findings() {
			got = append(got, fd.Severity.String()+" "+fd.RuleID+" "+fd.Message)
		}
		if len(got) != len(want) {
			t.Errorf("%s: got %q", ns, got)
			continue
		}
		for i := range want {
			if !strings.HasPrefix(got[i], want[i]) {
				t.Errorf("%s: got %q, want prefix %q", ns, got[i], want[i])
			}
		}
	}
}
