// Package diff compares local manifests with what is running in the cluster
// and flags breaking or risky changes before `kubectl apply` hits them:
// immutable fields, selector drift, removed APIs, downtime-inducing rollouts.
package diff

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Rules describes the diff findings (listed by `k8s-guardian rules`).
var Rules = []*rules.Rule{
	{ID: "DF001", Name: "immutable-selector", Severity: rules.Error, Description: "Workload selectors are immutable; changing them makes kubectl apply fail."},
	{ID: "DF002", Name: "immutable-field", Severity: rules.Error, Description: "Changes to immutable fields (StatefulSet volumeClaimTemplates/serviceName, Job template, Service clusterIP, PVC class, immutable ConfigMaps/Secrets) are rejected."},
	{ID: "DF003", Name: "selector-template-mismatch", Severity: rules.Error, Description: "The pod template labels must satisfy the workload selector."},
	{ID: "DF004", Name: "removed-api-on-cluster", Severity: rules.Error, Description: "The apiVersion has been removed in the cluster's Kubernetes version."},
	{ID: "DF005", Name: "traffic-change", Severity: rules.Warning, Description: "Service selector or port changes move or break traffic for existing clients."},
	{ID: "DF006", Name: "rollout-risk", Severity: rules.Warning, Description: "Risky rollouts: Recreate strategy downtime, scaling to zero, removed containers or ports."},
	{ID: "DF007", Name: "rollout-info", Severity: rules.Info, Description: "Informational summary of rollouts, image and replica changes."},
}

func rule(id string) *rules.Rule {
	for _, r := range Rules {
		if r.ID == id {
			return r
		}
	}
	panic(id)
}

// Change is a single changed leaf.
type Change struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// Status of an object compared with the cluster.
const (
	New       = "new"
	Changed   = "changed"
	Unchanged = "unchanged"
	Unknown   = "unknown"
)

// Object is the diff result for one resource.
type Object struct {
	Resource  string          `json:"resource"`
	Namespace string          `json:"namespace,omitempty"`
	Source    string          `json:"source"`
	Status    string          `json:"status"`
	Changes   []Change        `json:"changes,omitempty"`
	Findings  []rules.Finding `json:"findings,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Result of a diff run.
type Result struct {
	ClusterVersion string    `json:"clusterVersion,omitempty"`
	Objects        []*Object `json:"objects"`
}

// Findings returns all findings across objects.
func (r *Result) Findings() []rules.Finding {
	var out []rules.Finding
	for _, o := range r.Objects {
		out = append(out, o.Findings...)
	}
	return out
}

// Run diffs every object against the cluster.
func Run(c cluster.Cluster, objs []*manifest.Object, namespace string, opts rules.Options) *Result {
	res := &Result{}
	res.ClusterVersion, _ = c.Version()
	if namespace == "" {
		namespace = c.DefaultNamespace()
	}
	for _, o := range objs {
		ns := o.Namespace()
		if ns == "" && !clusterScoped[o.Kind()] {
			ns = namespace
		}
		d := &Object{Resource: o.Ref(), Namespace: ns, Source: o.Source}
		res.Objects = append(res.Objects, d)
		a := &analysis{obj: o, d: d, opts: opts, all: objs}

		a.removedAPI(res.ClusterVersion)
		a.selectorTemplate()

		live, err := c.Get(o.Kind(), o.APIVersion(), ns, o.Name())
		switch {
		case errors.Is(err, cluster.ErrNotFound):
			d.Status = New
			continue
		case err != nil:
			d.Status, d.Error = Unknown, err.Error()
			continue
		}
		walk(o.Root, live.Root, "", &d.Changes)
		d.Status = Changed
		if len(d.Changes) == 0 {
			d.Status = Unchanged
		}
		a.live = live
		a.compare()
	}
	return res
}

var clusterScoped = map[string]bool{
	"Namespace": true, "Node": true, "PersistentVolume": true, "StorageClass": true, "ClusterRole": true,
	"ClusterRoleBinding": true, "CustomResourceDefinition": true, "PriorityClass": true, "IngressClass": true,
	"ValidatingWebhookConfiguration": true, "MutatingWebhookConfiguration": true, "APIService": true, "RuntimeClass": true,
}

// ---- structural diff ----------------------------------------------------------

func render(n *yaml.Node) string {
	if n == nil {
		return "<none>"
	}
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	b, _ := yaml.Marshal(n)
	s := strings.TrimSpace(string(b))
	if len(s) > 80 || strings.Contains(s, "\n") {
		s = strings.Join(strings.Fields(s), " ")
		if len(s) > 80 {
			s = s[:77] + "..."
		}
	}
	return s
}

func sameScalar(a, b string) bool {
	if a == b {
		return true
	}
	x, err1 := quantity.Parse(a)
	y, err2 := quantity.Parse(b)
	return err1 == nil && err2 == nil && x == y
}

func namedList(n *yaml.Node) bool {
	items := yamlx.Items(n)
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if yamlx.String(it, "name") == "" {
			return false
		}
	}
	return true
}

// walk compares only fields present locally (like kubectl apply, which does
// not remove server-defaulted fields), plus elements removed from named lists.
func walk(local, live *yaml.Node, path string, out *[]Change) {
	// The API server drops empty maps/lists; they are not real changes.
	if live == nil && (local.Kind == yaml.MappingNode || local.Kind == yaml.SequenceNode) && len(local.Content) == 0 {
		return
	}
	switch local.Kind {
	case yaml.MappingNode:
		if live == nil || live.Kind != yaml.MappingNode {
			*out = append(*out, Change{path, render(live), render(local)})
			return
		}
		for i := 0; i+1 < len(local.Content); i += 2 {
			k := local.Content[i].Value
			if path == "" && k == "status" {
				continue
			}
			walk(local.Content[i+1], yamlx.Get(live, k), join(path, k), out)
		}
	case yaml.SequenceNode:
		if live == nil || live.Kind != yaml.SequenceNode {
			*out = append(*out, Change{path, render(live), render(local)})
			return
		}
		if namedList(local) && namedList(live) {
			seen := map[string]bool{}
			for _, it := range local.Content {
				name := yamlx.String(it, "name")
				seen[name] = true
				walk(it, byName(live, name), fmt.Sprintf("%s[%s]", path, name), out)
			}
			for _, it := range live.Content {
				if name := yamlx.String(it, "name"); !seen[name] {
					*out = append(*out, Change{fmt.Sprintf("%s[%s]", path, name), "present", "<removed>"})
				}
			}
			return
		}
		if len(local.Content) != len(live.Content) {
			*out = append(*out, Change{path, render(live), render(local)})
			return
		}
		for i := range local.Content {
			walk(local.Content[i], live.Content[i], fmt.Sprintf("%s[%d]", path, i), out)
		}
	case yaml.ScalarNode:
		if live == nil {
			if local.Tag == "!!null" {
				return
			}
			*out = append(*out, Change{path, "<none>", local.Value})
			return
		}
		if live.Kind != yaml.ScalarNode || !sameScalar(local.Value, live.Value) {
			*out = append(*out, Change{path, render(live), local.Value})
		}
	}
}

func byName(seq *yaml.Node, name string) *yaml.Node {
	for _, it := range yamlx.Items(seq) {
		if yamlx.String(it, "name") == name {
			return it
		}
	}
	return nil
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	if strings.ContainsAny(key, "./") {
		return path + "['" + key + "']"
	}
	return path + "." + key
}

// ---- semantic analysis ------------------------------------------------------

type analysis struct {
	obj  *manifest.Object
	live *manifest.Object
	d    *Object
	opts rules.Options
	all  []*manifest.Object
}

func (a *analysis) add(id, msg string) {
	r := rule(id)
	if !a.opts.Enabled(r, a.obj) {
		return
	}
	a.d.Findings = append(a.d.Findings, rules.Finding{RuleID: r.ID, Rule: r.Name, Severity: r.Severity, Message: msg,
		Source: a.obj.Source, Resource: a.obj.Ref(), Namespace: a.d.Namespace, Line: a.obj.Root.Line})
}

func (a *analysis) changed(path ...string) (old, new *yaml.Node, ok bool) {
	n := yamlx.Path(a.obj.Root, path...)
	if n == nil {
		return nil, nil, false
	}
	o := yamlx.Path(a.live.Root, path...)
	var ch []Change
	walk(n, o, "", &ch)
	return o, n, len(ch) > 0
}

func (a *analysis) removedAPI(version string) {
	r, ok := rules.LookupRemovedAPI(a.obj.APIVersion(), a.obj.Kind())
	if !ok || version == "" || rules.MinorVersion(version) < rules.MinorVersion(r.RemovedIn) {
		return
	}
	a.add("DF004", fmt.Sprintf("apiVersion %s was removed in Kubernetes %s and the cluster runs %s: apply will fail, migrate to %s", a.obj.APIVersion(), r.RemovedIn, version, r.Replacement))
}

func (a *analysis) selectorTemplate() {
	if a.obj.Kind() == "Pod" || rules.NewTarget(a.obj) == nil {
		return
	}
	sel := yamlx.Path(a.obj.Root, "spec", "selector", "matchLabels")
	labels := rules.PodTemplateLabels(a.obj)
	if sel == nil {
		return
	}
	for i := 0; i+1 < len(sel.Content); i += 2 {
		k, v := sel.Content[i].Value, sel.Content[i+1].Value
		if yamlx.String(labels, k) != v {
			a.add("DF003", fmt.Sprintf("selector requires %s=%s but the pod template labels don't have it: the API server rejects this", k, v))
			return
		}
	}
}

func (a *analysis) compare() {
	kind := a.obj.Kind()
	switch kind {
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet":
		if old, _, ok := a.changed("spec", "selector"); ok {
			a.add("DF001", fmt.Sprintf("spec.selector is immutable (live: %s): apply will fail; delete and recreate (downtime) or deploy under a new name and migrate traffic", render(old)))
		}
		a.rollout()
	case "Job":
		if _, _, ok := a.changed("spec", "template"); ok {
			a.add("DF002", "Job spec.template is immutable: delete the Job (or use a new name) before applying")
		}
	case "Service":
		if old, n, ok := a.changed("spec", "clusterIP"); ok && old != nil && old.Value != "" && n.Value != "" {
			a.add("DF002", fmt.Sprintf("spec.clusterIP is immutable (live %s, local %s)", old.Value, n.Value))
		}
		if old, _, ok := a.changed("spec", "selector"); ok {
			a.add("DF005", fmt.Sprintf("Service selector changes from %s: traffic moves to different pods", render(old)))
		}
		a.servicePorts()
	case "PersistentVolumeClaim":
		if old, n, ok := a.changed("spec", "storageClassName"); ok && old != nil {
			a.add("DF002", fmt.Sprintf("spec.storageClassName is immutable (%s -> %s)", old.Value, n.Value))
		}
		if old, n, ok := a.changed("spec", "resources", "requests", "storage"); ok && old != nil {
			ov, _ := quantity.Bytes(old.Value)
			nv, _ := quantity.Bytes(n.Value)
			if nv < ov {
				a.add("DF002", fmt.Sprintf("PVCs cannot shrink (%s -> %s)", old.Value, n.Value))
			}
		}
	case "ConfigMap", "Secret":
		if yamlx.IsTrue(yamlx.Get(a.live.Root, "immutable")) && a.d.Status == Changed {
			a.add("DF002", kind+" is immutable in the cluster: its data cannot be changed, create a new one instead")
		}
	}
	if kind == "StatefulSet" {
		for _, f := range []string{"volumeClaimTemplates", "serviceName", "podManagementPolicy"} {
			if _, _, ok := a.changed("spec", f); ok {
				a.add("DF002", fmt.Sprintf("StatefulSet spec.%s is immutable: apply will fail (delete with --cascade=orphan and recreate)", f))
			}
		}
	}
}

func (a *analysis) rollout() {
	t, lt := rules.NewTarget(a.obj), rules.NewTarget(a.live)
	if t == nil || lt == nil {
		return
	}
	oldR := yamlx.String(a.live.Root, "spec", "replicas")
	newR := yamlx.String(a.obj.Root, "spec", "replicas")
	if newR != "" && oldR != newR {
		if newR == "0" {
			a.add("DF006", fmt.Sprintf("scales %s to 0 replicas (from %s): the workload goes down", a.obj.Kind(), oldR))
		} else if hpa := a.hpaFor(); hpa != "" {
			a.add("DF006", fmt.Sprintf("spec.replicas (%s) is set but HorizontalPodAutoscaler %q manages this workload: every apply resets it to %s (currently %s); remove spec.replicas from the manifest", newR, hpa, newR, oldR))
		} else {
			a.add("DF007", fmt.Sprintf("replicas %s -> %s (if an HPA manages this workload, drop spec.replicas from the manifest)", oldR, newR))
		}
	}
	var imgs []string
	for _, c := range t.Containers {
		lc := byName(yamlx.Get(lt.PodSpec, map[bool]string{true: "initContainers", false: "containers"}[c.Init]), c.Name)
		if lc == nil {
			continue
		}
		if o, n := yamlx.String(lc, "image"), yamlx.String(c.Node, "image"); o != n {
			imgs = append(imgs, fmt.Sprintf("%s: %s -> %s", c.Name, o, n))
		}
		for _, p := range yamlx.Items(yamlx.Get(lc, "ports")) {
			name := yamlx.String(p, "name")
			if name != "" && !hasPortName(c.Node, name) && a.portUsedByService(name) {
				a.add("DF006", fmt.Sprintf("container %q removes named port %q that a Service in this set targets", c.Name, name))
			}
		}
	}
	for _, lc := range lt.Containers {
		if !lc.Init && byName(yamlx.Get(t.PodSpec, "containers"), lc.Name) == nil {
			a.add("DF006", fmt.Sprintf("container %q is removed from the pod", lc.Name))
		}
	}
	if len(imgs) > 0 {
		a.add("DF007", "image change: "+strings.Join(imgs, "; "))
	}
	if _, _, ok := a.changed("spec", "template"); ok {
		strategy := yamlx.String(a.obj.Root, "spec", "strategy", "type")
		if strategy == "" {
			strategy = yamlx.String(a.live.Root, "spec", "strategy", "type")
		}
		replicas := newR
		if replicas == "" {
			replicas = oldR
		}
		if a.obj.Kind() == "Deployment" && strategy == "Recreate" {
			a.add("DF006", "pod template changes with strategy Recreate: all pods are killed before new ones start (downtime)")
		} else {
			msg := "pod template changed: triggers a rolling restart"
			if replicas != "" {
				msg += " of " + replicas + " pod(s)"
			}
			a.add("DF007", msg)
		}
	}
}

func hasPortName(c *yaml.Node, name string) bool {
	for _, p := range yamlx.Items(yamlx.Get(c, "ports")) {
		if yamlx.String(p, "name") == name {
			return true
		}
	}
	return false
}

func (a *analysis) portUsedByService(name string) bool {
	for _, o := range a.all {
		if o.Kind() != "Service" {
			continue
		}
		for _, p := range yamlx.Items(yamlx.Path(o.Root, "spec", "ports")) {
			if yamlx.String(p, "targetPort") == name {
				return true
			}
		}
	}
	return false
}

func (a *analysis) servicePorts() {
	key := func(p *yaml.Node) string {
		if n := yamlx.String(p, "name"); n != "" {
			return n
		}
		return yamlx.String(p, "port")
	}
	local := map[string]*yaml.Node{}
	for _, p := range yamlx.Items(yamlx.Path(a.obj.Root, "spec", "ports")) {
		local[key(p)] = p
	}
	var msgs []string
	for _, lp := range yamlx.Items(yamlx.Path(a.live.Root, "spec", "ports")) {
		np, ok := local[key(lp)]
		switch {
		case !ok:
			msgs = append(msgs, fmt.Sprintf("port %s is removed", yamlx.String(lp, "port")))
		case yamlx.String(np, "port") != yamlx.String(lp, "port"):
			msgs = append(msgs, fmt.Sprintf("port %s -> %s", yamlx.String(lp, "port"), yamlx.String(np, "port")))
		}
	}
	sort.Strings(msgs)
	if len(msgs) > 0 {
		a.add("DF005", "Service "+strings.Join(msgs, ", ")+": existing clients using the old port will break")
	}
}

// hpaFor returns the name of an HPA in the manifest set targeting the object.
func (a *analysis) hpaFor() string {
	for _, o := range a.all {
		if o.Kind() == "HorizontalPodAutoscaler" && o.Namespace() == a.obj.Namespace() &&
			yamlx.String(o.Root, "spec", "scaleTargetRef", "kind") == a.obj.Kind() &&
			yamlx.String(o.Root, "spec", "scaleTargetRef", "name") == a.obj.Name() {
			return o.Name()
		}
	}
	return ""
}
