package live

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// placement decides which nodes the scheduler may put a pod on, following
// the scheduler's filters for cordoned nodes, nodeSelector, required node
// affinity and taints. Anything it can't evaluate counts as a match, so a
// node is never excluded by mistake; unknown records what that was.
type placement struct {
	selector    *yaml.Node
	affinity    *yaml.Node // requiredDuringSchedulingIgnoredDuringExecution
	tolerations []*yaml.Node
	implicit    []cluster.Taint // tolerated without a toleration in the spec
	bound       bool            // spec.nodeName: the scheduler is bypassed
	unknown     []string
}

// daemonTolerations are added to every DaemonSet pod by the DaemonSet
// controller (key and effect, operator Exists).
var daemonTolerations = []cluster.Taint{
	{Key: "node.kubernetes.io/not-ready", Effect: "NoExecute"},
	{Key: "node.kubernetes.io/unreachable", Effect: "NoExecute"},
	{Key: "node.kubernetes.io/disk-pressure", Effect: "NoSchedule"},
	{Key: "node.kubernetes.io/memory-pressure", Effect: "NoSchedule"},
	{Key: "node.kubernetes.io/pid-pressure", Effect: "NoSchedule"},
	{Key: "node.kubernetes.io/unschedulable", Effect: "NoSchedule"},
}

// cordon is the taint the scheduler checks for a cordoned node.
var cordon = cluster.Taint{Key: "node.kubernetes.io/unschedulable", Effect: "NoSchedule"}

func newPlacement(t *rules.Target) *placement {
	p := &placement{
		selector:    yamlx.Get(t.PodSpec, "nodeSelector"),
		tolerations: yamlx.Items(yamlx.Get(t.PodSpec, "tolerations")),
		bound:       yamlx.String(t.PodSpec, "nodeName") != "",
	}
	if a := yamlx.Path(t.PodSpec, "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution"); a != nil && a.Kind == yaml.MappingNode {
		p.affinity = a
	}
	if t.Obj.Kind() == "DaemonSet" {
		p.implicit = daemonTolerations
		if yamlx.IsTrue(yamlx.Get(t.PodSpec, "hostNetwork")) {
			p.implicit = append(p.implicit[:len(p.implicit):len(p.implicit)], cluster.Taint{Key: "node.kubernetes.io/network-unavailable", Effect: "NoSchedule"})
		}
	}
	return p
}

// excluded is a node the pod can't be scheduled on, and why.
type excluded struct {
	node   cluster.Node
	reason string // "cordoned", "nodeSelector", "node affinity" or "taint k=v:Effect"
}

// filter splits nodes into those the pod may be scheduled on and the rest.
func (p *placement) filter(nodes []cluster.Node) (candidates []cluster.Node, out []excluded) {
	for _, n := range nodes {
		if r := p.exclude(n); r != "" {
			out = append(out, excluded{n, r})
		} else {
			candidates = append(candidates, n)
		}
	}
	return candidates, out
}

func (p *placement) exclude(n cluster.Node) string {
	if n.Unschedulable && !p.bound && !p.tolerates(cordon) {
		return "cordoned"
	}
	if p.selector != nil {
		for i := 0; i+1 < len(p.selector.Content); i += 2 {
			if v, ok := n.Labels[p.selector.Content[i].Value]; !ok || v != p.selector.Content[i+1].Value {
				return "nodeSelector"
			}
		}
	}
	if p.affinity != nil && !p.affinityMatches(n) {
		return "node affinity"
	}
	for _, t := range n.Taints {
		// The kubelet only enforces NoExecute for pods with a nodeName.
		if (t.Effect == "NoExecute" || t.Effect == "NoSchedule" && !p.bound) && !p.tolerates(t) {
			return "taint " + t.String()
		}
	}
	return ""
}

func (p *placement) note(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	for _, u := range p.unknown {
		if u == s {
			return
		}
	}
	p.unknown = append(p.unknown, s)
}

// tolerates mirrors Toleration.ToleratesTaint in k8s.io/api.
func (p *placement) tolerates(t cluster.Taint) bool {
	for _, i := range p.implicit {
		if i.Key == t.Key && i.Effect == t.Effect {
			return true
		}
	}
	for _, tol := range p.tolerations {
		if e := yamlx.String(tol, "effect"); e != "" && e != t.Effect {
			continue
		}
		if k := yamlx.String(tol, "key"); k != "" && k != t.Key {
			continue
		}
		switch op := yamlx.String(tol, "operator"); op {
		case "", "Equal":
			if yamlx.String(tol, "value") == t.Value {
				return true
			}
		case "Exists":
			return true
		default:
			p.note("toleration operator %q", op)
			return true
		}
	}
	return false
}

// affinityMatches evaluates required node affinity: the terms are ORed,
// the expressions and fields within a term ANDed, and an empty term
// matches nothing.
func (p *placement) affinityMatches(n cluster.Node) bool {
	for _, term := range yamlx.Items(yamlx.Get(p.affinity, "nodeSelectorTerms")) {
		exprs := yamlx.Items(yamlx.Get(term, "matchExpressions"))
		fields := yamlx.Items(yamlx.Get(term, "matchFields"))
		if len(exprs) == 0 && len(fields) == 0 {
			continue
		}
		ok := true
		for _, e := range exprs {
			ok = ok && p.requirement(e, n.Labels)
		}
		for _, f := range fields {
			if k := yamlx.String(f, "key"); k != "metadata.name" {
				p.note("node affinity field %q", k)
				continue
			}
			ok = ok && p.requirement(f, map[string]string{"metadata.name": n.Name})
		}
		if ok {
			return true
		}
	}
	return false
}

// requirement evaluates one NodeSelectorRequirement against labels.
func (p *placement) requirement(e *yaml.Node, labels map[string]string) bool {
	key, op := yamlx.String(e, "key"), yamlx.String(e, "operator")
	var values []string
	for _, v := range yamlx.Items(yamlx.Get(e, "values")) {
		values = append(values, v.Value)
	}
	v, has := labels[key]
	switch op {
	case "In":
		return has && contains(values, v)
	case "NotIn":
		return !has || !contains(values, v)
	case "Exists":
		return has
	case "DoesNotExist":
		return !has
	case "Gt", "Lt":
		var want int64
		var err error
		if len(values) == 1 {
			want, err = strconv.ParseInt(values[0], 10, 64)
		}
		if len(values) != 1 || err != nil {
			p.note("node affinity %s %v", op, values)
			return true
		}
		got, err := strconv.ParseInt(v, 10, 64)
		if !has || err != nil {
			return false
		}
		return op == "Gt" && got > want || op == "Lt" && got < want
	}
	p.note("node affinity operator %q", op)
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// describe groups excluded nodes by reason: "taint gpu=true:NoSchedule:
// n2, n3; cordoned: n4".
func describe(ex []excluded) string {
	var order []string
	names := map[string][]string{}
	for _, e := range ex {
		if _, ok := names[e.reason]; !ok {
			order = append(order, e.reason)
		}
		names[e.reason] = append(names[e.reason], e.node.Name)
	}
	parts := make([]string, len(order))
	for i, r := range order {
		n := names[r]
		list := strings.Join(n[:min(len(n), 3)], ", ")
		if len(n) > 3 {
			list += fmt.Sprintf(" and %d more", len(n)-3)
		}
		parts[i] = r + ": " + list
	}
	return strings.Join(parts, "; ")
}
