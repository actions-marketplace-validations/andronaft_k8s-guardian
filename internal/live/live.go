// Package live validates manifests against the real state of a cluster:
// served APIs, namespaces, node capacity, ResourceQuotas, LimitRanges and
// referenced ConfigMaps/Secrets/ServiceAccounts/PVCs/StorageClasses. It
// answers "the YAML is valid, but will it actually run *there*?".
package live

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/k8sname"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Rule metadata for live findings (listed by `k8s-guardian rules`).
var Rules = []*rules.Rule{
	{ID: "LV001", Name: "api-served", Severity: rules.Error, Description: "The apiVersion must be served by the target cluster (CRD installed, API not removed)."},
	{ID: "LV002", Name: "namespace-exists", Severity: rules.Error, Description: "The target namespace must exist or be created by the same manifest set."},
	{ID: "LV003", Name: "node-fit", Severity: rules.Error, Description: "At least one schedulable node (matching nodeSelector) must be able to fit the pod's requests."},
	{ID: "LV004", Name: "resource-quota", Severity: rules.Error, Description: "The workload must fit into the namespace's remaining ResourceQuota and satisfy its required requests/limits."},
	{ID: "LV005", Name: "limit-range", Severity: rules.Error, Description: "Container requests/limits must be within the namespace's LimitRange min/max."},
	{ID: "LV006", Name: "references-exist", Severity: rules.Error, Description: "Referenced ConfigMaps, Secrets, ServiceAccounts, PVCs and PriorityClasses must exist in the cluster or the manifest set."},
	{ID: "LV007", Name: "storage-class", Severity: rules.Error, Description: "PVCs must reference an existing StorageClass, or the cluster must have a default one."},
}

func ruleByID(id string) *rules.Rule {
	for _, r := range Rules {
		if r.ID == id {
			return r
		}
	}
	panic("unknown live rule " + id)
}

// Result of a live check.
type Result struct {
	ClusterVersion string
	Findings       []rules.Finding
	Notes          []string // checks that could not run (e.g. RBAC)
}

type checker struct {
	c      cluster.Cluster
	objs   []*manifest.Object
	defNS  string
	opts   rules.Options
	res    *Result
	noted  map[string]bool
	quotas map[string]map[string]int64 // ns/quota/key -> consumed by this run
}

// Check runs all live checks. namespace overrides the namespace for objects
// that don't set one (like `kubectl apply -n`).
func Check(c cluster.Cluster, objs []*manifest.Object, namespace string, opts rules.Options) *Result {
	ch := &checker{c: c, objs: objs, opts: opts, res: &Result{}, noted: map[string]bool{}, quotas: map[string]map[string]int64{}}
	ch.defNS = namespace
	if ch.defNS == "" {
		ch.defNS = c.DefaultNamespace()
	}
	if v, err := c.Version(); err == nil {
		ch.res.ClusterVersion = v
	} else {
		ch.note("cluster version", err)
	}
	for _, o := range objs {
		ch.apiServed(o)
		if !namespaced[o.Kind()] {
			continue
		}
		ns, ok := ch.namespace(o)
		if !ok {
			continue
		}
		ch.storage(o, ns)
		if t := rules.NewTarget(o); t != nil {
			ch.workload(t, ns)
		}
	}
	sort.SliceStable(ch.res.Findings, func(i, j int) bool { return ch.res.Findings[i].Source < ch.res.Findings[j].Source })
	return ch.res
}

var namespaced = map[string]bool{
	"Pod": true, "Deployment": true, "StatefulSet": true, "DaemonSet": true, "ReplicaSet": true, "ReplicationController": true,
	"Job": true, "CronJob": true, "Service": true, "ConfigMap": true, "Secret": true, "ServiceAccount": true,
	"PersistentVolumeClaim": true, "Ingress": true, "Role": true, "RoleBinding": true, "NetworkPolicy": true,
	"HorizontalPodAutoscaler": true, "PodDisruptionBudget": true,
}

func (ch *checker) note(what string, err error) {
	if !ch.noted[what] {
		ch.noted[what] = true
		ch.res.Notes = append(ch.res.Notes, fmt.Sprintf("skipped %s check: %v", what, err))
	}
}

func (ch *checker) add(id string, o *manifest.Object, container, msg string) {
	ch.addSev(id, o, container, msg, ruleByID(id).Severity)
}

func (ch *checker) addSev(id string, o *manifest.Object, container, msg string, sev rules.Severity) {
	r := ruleByID(id)
	if !ch.opts.Enabled(r, o) {
		return
	}
	ch.res.Findings = append(ch.res.Findings, rules.Finding{
		RuleID: r.ID, Rule: r.Name, Severity: sev, Message: msg, Source: o.Source,
		Resource: o.Ref(), Namespace: ch.nsOf(o), Container: container, Line: o.Root.Line,
	})
}

func (ch *checker) nsOf(o *manifest.Object) string {
	if ns := o.Namespace(); ns != "" {
		return ns
	}
	return ch.defNS
}

// inSet reports whether the manifest set itself creates kind/name in ns.
func (ch *checker) inSet(kind, ns, name string) bool {
	for _, o := range ch.objs {
		if o.Kind() == kind && o.Name() == name && (kind == "Namespace" || kind == "PriorityClass" || kind == "StorageClass" || ch.nsOf(o) == ns) {
			return true
		}
	}
	return false
}

func (ch *checker) apiServed(o *manifest.Object) {
	served, err := ch.c.APIVersions()
	if err != nil {
		ch.note("API versions", err)
		return
	}
	av := o.APIVersion()
	if served[av] {
		return
	}
	// CRDs shipped in the same manifest set.
	group, version, _ := strings.Cut(av, "/")
	for _, crd := range ch.objs {
		if crd.Kind() != "CustomResourceDefinition" || yamlx.String(crd.Root, "spec", "group") != group {
			continue
		}
		for _, v := range yamlx.Items(yamlx.Path(crd.Root, "spec", "versions")) {
			if yamlx.String(v, "name") == version {
				return
			}
		}
	}
	msg := fmt.Sprintf("apiVersion %s is not served by this cluster", av)
	if ch.res.ClusterVersion != "" {
		msg += " (" + ch.res.ClusterVersion + ")"
	}
	if r, ok := rules.LookupRemovedAPI(av, o.Kind()); ok {
		msg += fmt.Sprintf(": removed in %s, use %s", r.RemovedIn, r.Replacement)
	} else {
		msg += ": is the CRD/operator installed?"
	}
	ch.add("LV001", o, "", msg)
}

func (ch *checker) namespace(o *manifest.Object) (*cluster.Namespace, bool) {
	name := ch.nsOf(o)
	ns, err := ch.c.Namespace(name)
	var invalid *k8sname.ErrInvalidName
	if errors.As(err, &invalid) {
		ch.add("LV002", o, "", err.Error())
		return nil, false
	}
	if err != nil {
		ch.note("namespace "+name, err)
		return nil, false
	}
	if !ns.Exists {
		if !ch.inSet("Namespace", "", name) {
			ch.add("LV002", o, "", fmt.Sprintf("namespace %q does not exist in the cluster and is not created by this manifest set", name))
		}
		return nil, false // nothing else to compare against
	}
	return ns, true
}

// ---- workloads --------------------------------------------------------------

func (ch *checker) workload(t *rules.Target, ns *cluster.Namespace) {
	pr := podResources(t, ns.LimitRanges)
	ch.references(t, ns)
	ch.limitRange(t, ns, pr)
	nodes, err := ch.c.Nodes()
	if err != nil {
		ch.note("node capacity", err)
	}
	candidates := matchingNodes(t, nodes)
	if err == nil {
		ch.nodeFit(t, pr, nodes, candidates)
	}
	ch.quota(t, ns, pr, len(candidates))
}

func matchingNodes(t *rules.Target, nodes []cluster.Node) []cluster.Node {
	sel := yamlx.Get(t.PodSpec, "nodeSelector")
	var out []cluster.Node
	for _, n := range nodes {
		if n.Unschedulable {
			continue
		}
		ok := true
		if sel != nil {
			for i := 0; i+1 < len(sel.Content); i += 2 {
				if n.Labels[sel.Content[i].Value] != sel.Content[i+1].Value {
					ok = false
				}
			}
		}
		if ok {
			out = append(out, n)
		}
	}
	return out
}

func (ch *checker) nodeFit(t *rules.Target, pr podRes, nodes, candidates []cluster.Node) {
	if len(nodes) == 0 {
		return
	}
	if len(candidates) == 0 {
		msg := "no schedulable node in the cluster"
		if sel := yamlx.Get(t.PodSpec, "nodeSelector"); sel != nil {
			msg = "nodeSelector matches no schedulable node"
		}
		ch.add("LV003", t.Obj, "", msg+": pods will stay Pending")
		return
	}
	var bestCPU, bestMem cluster.Node
	var maxCPU, maxMem int64
	for _, n := range candidates {
		cpu, _ := quantity.MilliCPU(orZero(n.AllocCPU))
		mem, _ := quantity.Bytes(orZero(n.AllocMemory))
		if cpu >= pr.req[cpuKey] && mem >= pr.req[memKey] {
			return // fits on this node (ignoring current utilisation)
		}
		if cpu > maxCPU {
			maxCPU, bestCPU = cpu, n
		}
		if mem > maxMem {
			maxMem, bestMem = mem, n
		}
	}
	var parts []string
	if pr.req[cpuKey] > maxCPU {
		parts = append(parts, fmt.Sprintf("requests %s CPU but the largest matching node (%s) has %s allocatable", quantity.FormatCPU(pr.req[cpuKey]), bestCPU.Name, quantity.FormatCPU(maxCPU)))
	}
	if pr.req[memKey] > maxMem {
		parts = append(parts, fmt.Sprintf("requests %s memory but the largest matching node (%s) has %s allocatable", quantity.FormatBytes(pr.req[memKey]), bestMem.Name, quantity.FormatBytes(maxMem)))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("no single node has both %s CPU and %s memory allocatable", quantity.FormatCPU(pr.req[cpuKey]), quantity.FormatBytes(pr.req[memKey])))
	}
	ch.add("LV003", t.Obj, "", "pod "+strings.Join(parts, "; ")+": pods will stay Pending")
}

func (ch *checker) limitRange(t *rules.Target, ns *cluster.Namespace, pr podRes) {
	for _, lr := range ns.LimitRanges {
		switch lr.Type {
		case "Container":
			for _, c := range pr.containers {
				for _, key := range []string{cpuKey, memKey} {
					if max, ok := parse(key, lr.Max[key]); ok {
						if v, has := c.lim[key]; has && v > max {
							ch.add("LV005", t.Obj, c.name, fmt.Sprintf("limits.%s %s exceeds LimitRange %q max %s", key, format(key, v), lr.LimitRange, lr.Max[key]))
						} else if v, has := c.req[key]; has && v > max {
							ch.add("LV005", t.Obj, c.name, fmt.Sprintf("requests.%s %s exceeds LimitRange %q max %s", key, format(key, v), lr.LimitRange, lr.Max[key]))
						}
					}
					if min, ok := parse(key, lr.Min[key]); ok {
						if v, has := c.req[key]; has && v < min {
							ch.add("LV005", t.Obj, c.name, fmt.Sprintf("requests.%s %s is below LimitRange %q min %s", key, format(key, v), lr.LimitRange, lr.Min[key]))
						}
					}
				}
			}
		case "Pod":
			for _, key := range []string{cpuKey, memKey} {
				if max, ok := parse(key, lr.Max[key]); ok && pr.lim[key] > max {
					ch.add("LV005", t.Obj, "", fmt.Sprintf("pod limits.%s %s exceed LimitRange %q pod max %s", key, format(key, pr.lim[key]), lr.LimitRange, lr.Max[key]))
				}
			}
		}
	}
}

// replicas returns how many pods the workload runs (DaemonSets: one per
// matching node).
func replicas(t *rules.Target, nodes int) int64 {
	root := t.Obj.Root
	var s string
	switch t.Obj.Kind() {
	case "Deployment", "StatefulSet", "ReplicaSet", "ReplicationController":
		s = yamlx.String(root, "spec", "replicas")
	case "Job":
		s = yamlx.String(root, "spec", "parallelism")
	case "CronJob":
		s = yamlx.String(root, "spec", "jobTemplate", "spec", "parallelism")
	case "DaemonSet":
		if nodes > 0 {
			return int64(nodes)
		}
	}
	if v, err := quantity.Parse(s); err == nil && s != "" {
		return int64(v)
	}
	return 1
}

var quotaKeys = map[string]struct {
	res string // cpu | memory | pods
	lim bool
}{
	"cpu": {cpuKey, false}, "requests.cpu": {cpuKey, false},
	"memory": {memKey, false}, "requests.memory": {memKey, false},
	"limits.cpu": {cpuKey, true}, "limits.memory": {memKey, true},
	"pods": {"pods", false},
}

func (ch *checker) quota(t *rules.Target, ns *cluster.Namespace, pr podRes, nodes int) {
	if len(ns.Quotas) == 0 {
		return
	}
	n := replicas(t, nodes)
	demand := func(p podRes, r int64) map[string]int64 {
		d := map[string]int64{"pods": r}
		for q, k := range quotaKeys {
			if k.res == "pods" {
				continue
			}
			if k.lim {
				d[q] = p.lim[k.res] * r
			} else {
				d[q] = p.req[k.res] * r
			}
		}
		return d
	}
	need := demand(pr, n)
	// The object may already run in the cluster: only the delta counts.
	var existing map[string]int64
	live, err := ch.c.Get(t.Obj.Kind(), t.Obj.APIVersion(), ns.Name, t.Obj.Name())
	switch {
	case err == nil:
		if lt := rules.NewTarget(live); lt != nil {
			existing = demand(podResources(lt, ns.LimitRanges), replicas(lt, nodes))
		}
	case !errors.Is(err, cluster.ErrNotFound):
		ch.note("existing "+t.Obj.Ref(), err)
	}

	for _, q := range ns.Quotas {
		key := ns.Name + "/" + q.Name
		if ch.quotas[key] == nil {
			ch.quotas[key] = map[string]int64{}
		}
		keys := make([]string, 0, len(q.Hard))
		for k := range q.Hard {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			qk, ok := quotaKeys[k]
			if !ok {
				continue
			}
			// A quota on requests/limits makes them mandatory.
			if qk.res != "pods" {
				missing := pr.missingReq[qk.res]
				what := "requests"
				if qk.lim {
					missing, what = pr.missingLim[qk.res], "limits"
				}
				if len(missing) > 0 {
					ch.add("LV004", t.Obj, strings.Join(missing, ","), fmt.Sprintf("ResourceQuota %q constrains %s, so every container must set %s.%s (no LimitRange default either): pods will be rejected", q.Name, k, what, qk.res))
					continue
				}
			}
			hard, ok1 := parse(qk.res, q.Hard[k])
			used, ok2 := parse(qk.res, orZero(q.Used[k]))
			if !ok1 || !ok2 {
				continue
			}
			delta := need[k] - existing[k]
			if delta <= 0 {
				continue
			}
			avail := hard - used - ch.quotas[key][k]
			ch.quotas[key][k] += delta
			if delta > avail {
				if avail < 0 {
					avail = 0
				}
				ch.add("LV004", t.Obj, "", fmt.Sprintf("needs %s=%s more (%d replica(s)) but ResourceQuota %q in namespace %q only has %s left (hard %s, used %s): pods will be rejected",
					k, format(qk.res, delta), n, q.Name, ns.Name, format(qk.res, avail), q.Hard[k], orZero(q.Used[k])))
			}
		}
	}
}

// ---- references ---------------------------------------------------------------

type ref struct {
	kind, name, where string
	optional          bool
	severity          rules.Severity
}

func (ch *checker) references(t *rules.Target, ns *cluster.Namespace) {
	var refs []ref
	spec := t.PodSpec
	sa := yamlx.String(spec, "serviceAccountName")
	if sa == "" {
		sa = yamlx.String(spec, "serviceAccount")
	}
	if sa != "" && sa != "default" {
		refs = append(refs, ref{kind: "ServiceAccount", name: sa, where: "serviceAccountName"})
	}
	for _, v := range yamlx.Items(yamlx.Get(spec, "volumes")) {
		vol := "volume " + yamlx.String(v, "name")
		if cm := yamlx.Get(v, "configMap"); cm != nil {
			refs = append(refs, ref{kind: "ConfigMap", name: yamlx.String(cm, "name"), where: vol, optional: yamlx.IsTrue(yamlx.Get(cm, "optional"))})
		}
		if s := yamlx.Get(v, "secret"); s != nil {
			refs = append(refs, ref{kind: "Secret", name: yamlx.String(s, "secretName"), where: vol, optional: yamlx.IsTrue(yamlx.Get(s, "optional"))})
		}
		if p := yamlx.Get(v, "persistentVolumeClaim"); p != nil {
			refs = append(refs, ref{kind: "PersistentVolumeClaim", name: yamlx.String(p, "claimName"), where: vol})
		}
		for _, src := range yamlx.Items(yamlx.Path(v, "projected", "sources")) {
			if cm := yamlx.Get(src, "configMap"); cm != nil {
				refs = append(refs, ref{kind: "ConfigMap", name: yamlx.String(cm, "name"), where: vol, optional: yamlx.IsTrue(yamlx.Get(cm, "optional"))})
			}
			if s := yamlx.Get(src, "secret"); s != nil {
				refs = append(refs, ref{kind: "Secret", name: yamlx.String(s, "name"), where: vol, optional: yamlx.IsTrue(yamlx.Get(s, "optional"))})
			}
		}
	}
	for _, s := range yamlx.Items(yamlx.Get(spec, "imagePullSecrets")) {
		refs = append(refs, ref{kind: "Secret", name: yamlx.String(s, "name"), where: "imagePullSecrets", severity: rules.Warning})
	}
	for _, c := range t.Containers {
		where := fmt.Sprintf("container %q", c.Name)
		for _, e := range yamlx.Items(yamlx.Get(c.Node, "env")) {
			if r := yamlx.Path(e, "valueFrom", "configMapKeyRef"); r != nil {
				refs = append(refs, ref{kind: "ConfigMap", name: yamlx.String(r, "name"), where: where + " env " + yamlx.String(e, "name"), optional: yamlx.IsTrue(yamlx.Get(r, "optional"))})
			}
			if r := yamlx.Path(e, "valueFrom", "secretKeyRef"); r != nil {
				refs = append(refs, ref{kind: "Secret", name: yamlx.String(r, "name"), where: where + " env " + yamlx.String(e, "name"), optional: yamlx.IsTrue(yamlx.Get(r, "optional"))})
			}
		}
		for _, e := range yamlx.Items(yamlx.Get(c.Node, "envFrom")) {
			if r := yamlx.Get(e, "configMapRef"); r != nil {
				refs = append(refs, ref{kind: "ConfigMap", name: yamlx.String(r, "name"), where: where + " envFrom", optional: yamlx.IsTrue(yamlx.Get(r, "optional"))})
			}
			if r := yamlx.Get(e, "secretRef"); r != nil {
				refs = append(refs, ref{kind: "Secret", name: yamlx.String(r, "name"), where: where + " envFrom", optional: yamlx.IsTrue(yamlx.Get(r, "optional"))})
			}
		}
	}
	seen := map[string]bool{}
	for _, r := range refs {
		k := r.kind + "/" + r.name
		if r.optional || r.name == "" || seen[k] || ns.Names[strings.ToLower(r.kind)][r.name] || ch.inSet(r.kind, ns.Name, r.name) {
			continue
		}
		seen[k] = true
		impact := "pods will not start"
		switch r.kind {
		case "ServiceAccount":
			impact = "the controller cannot create pods"
		case "PersistentVolumeClaim":
			impact = "pods will stay Pending"
		case "Secret":
			if r.where == "imagePullSecrets" {
				impact = "private image pulls may fail"
			}
		}
		msg := fmt.Sprintf("%s %q (%s) does not exist in namespace %q and is not part of this manifest set: %s", r.kind, r.name, r.where, ns.Name, impact)
		sev := rules.Error
		if r.severity == rules.Warning {
			sev = rules.Warning
		}
		ch.addSev("LV006", t.Obj, "", msg, sev)
	}
	if pc := yamlx.String(spec, "priorityClassName"); pc != "" && pc != "system-cluster-critical" && pc != "system-node-critical" && !ch.inSet("PriorityClass", "", pc) {
		pcs, err := ch.c.PriorityClasses()
		if err != nil {
			ch.note("PriorityClasses", err)
		} else if !pcs[pc] {
			ch.add("LV006", t.Obj, "", fmt.Sprintf("PriorityClass %q does not exist: pods will be rejected", pc))
		}
	}
}

// ---- storage ------------------------------------------------------------------

func (ch *checker) storage(o *manifest.Object, ns *cluster.Namespace) {
	var claims []*manifestClaim
	switch o.Kind() {
	case "PersistentVolumeClaim":
		claims = append(claims, &manifestClaim{"", yamlx.Get(o.Root, "spec")})
	case "StatefulSet":
		for _, tpl := range yamlx.Items(yamlx.Path(o.Root, "spec", "volumeClaimTemplates")) {
			claims = append(claims, &manifestClaim{yamlx.String(tpl, "metadata", "name"), yamlx.Get(tpl, "spec")})
		}
	}
	if len(claims) == 0 {
		return
	}
	scs, err := ch.c.StorageClasses()
	if err != nil {
		ch.note("StorageClasses", err)
		return
	}
	for _, cl := range claims {
		where := ""
		if cl.template != "" {
			where = fmt.Sprintf("volumeClaimTemplate %q: ", cl.template)
		}
		sc := yamlx.Get(cl.spec, "storageClassName")
		switch {
		case sc == nil:
			if scs.Default == "" {
				ch.addSev("LV007", o, "", where+"no storageClassName and the cluster has no default StorageClass: the claim will stay Pending unless a matching PV exists", rules.Warning)
			}
		case sc.Value == "":
			// explicitly bound to pre-provisioned PVs
		case !scs.Names[sc.Value] && !ch.inSet("StorageClass", "", sc.Value):
			ch.add("LV007", o, "", fmt.Sprintf("%sStorageClass %q does not exist: the claim will stay Pending", where, sc.Value))
		}
	}
}

type manifestClaim struct {
	template string
	spec     *yaml.Node
}
