package rules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// RemovedAPI describes an API version that is no longer served.
type RemovedAPI struct {
	RemovedIn   string // Kubernetes minor version, e.g. "1.25"
	Replacement string
}

// removedAPIs lists apiVersion/kind pairs removed from Kubernetes.
// Kind "*" matches every kind of that group version.
var removedAPIs = map[string]RemovedAPI{
	"extensions/v1beta1/Deployment":               {"1.16", "apps/v1"},
	"extensions/v1beta1/DaemonSet":                {"1.16", "apps/v1"},
	"extensions/v1beta1/ReplicaSet":               {"1.16", "apps/v1"},
	"extensions/v1beta1/NetworkPolicy":            {"1.16", "networking.k8s.io/v1"},
	"extensions/v1beta1/PodSecurityPolicy":        {"1.16", "Pod Security Admission"},
	"extensions/v1beta1/Ingress":                  {"1.22", "networking.k8s.io/v1"},
	"apps/v1beta1/*":                              {"1.16", "apps/v1"},
	"apps/v1beta2/*":                              {"1.16", "apps/v1"},
	"networking.k8s.io/v1beta1/Ingress":           {"1.22", "networking.k8s.io/v1"},
	"networking.k8s.io/v1beta1/IngressClass":      {"1.22", "networking.k8s.io/v1"},
	"rbac.authorization.k8s.io/v1beta1/*":         {"1.22", "rbac.authorization.k8s.io/v1"},
	"apiextensions.k8s.io/v1beta1/*":              {"1.22", "apiextensions.k8s.io/v1"},
	"admissionregistration.k8s.io/v1beta1/*":      {"1.22", "admissionregistration.k8s.io/v1"},
	"apiregistration.k8s.io/v1beta1/*":            {"1.22", "apiregistration.k8s.io/v1"},
	"certificates.k8s.io/v1beta1/*":               {"1.22", "certificates.k8s.io/v1"},
	"coordination.k8s.io/v1beta1/*":               {"1.22", "coordination.k8s.io/v1"},
	"scheduling.k8s.io/v1beta1/*":                 {"1.22", "scheduling.k8s.io/v1"},
	"storage.k8s.io/v1beta1/CSIDriver":            {"1.22", "storage.k8s.io/v1"},
	"storage.k8s.io/v1beta1/CSINode":              {"1.22", "storage.k8s.io/v1"},
	"storage.k8s.io/v1beta1/StorageClass":         {"1.22", "storage.k8s.io/v1"},
	"storage.k8s.io/v1beta1/VolumeAttachment":     {"1.22", "storage.k8s.io/v1"},
	"storage.k8s.io/v1beta1/CSIStorageCapacity":   {"1.27", "storage.k8s.io/v1"},
	"batch/v1beta1/CronJob":                       {"1.25", "batch/v1"},
	"policy/v1beta1/PodDisruptionBudget":          {"1.25", "policy/v1"},
	"policy/v1beta1/PodSecurityPolicy":            {"1.25", "Pod Security Admission"},
	"discovery.k8s.io/v1beta1/EndpointSlice":      {"1.25", "discovery.k8s.io/v1"},
	"events.k8s.io/v1beta1/Event":                 {"1.25", "events.k8s.io/v1"},
	"node.k8s.io/v1beta1/RuntimeClass":            {"1.25", "node.k8s.io/v1"},
	"autoscaling/v2beta1/HorizontalPodAutoscaler": {"1.25", "autoscaling/v2"},
	"autoscaling/v2beta2/HorizontalPodAutoscaler": {"1.26", "autoscaling/v2"},
	"flowcontrol.apiserver.k8s.io/v1beta1/*":      {"1.26", "flowcontrol.apiserver.k8s.io/v1"},
	"flowcontrol.apiserver.k8s.io/v1beta2/*":      {"1.29", "flowcontrol.apiserver.k8s.io/v1"},
	"flowcontrol.apiserver.k8s.io/v1beta3/*":      {"1.32", "flowcontrol.apiserver.k8s.io/v1"},
	"resource.k8s.io/v1alpha2/*":                  {"1.31", "resource.k8s.io/v1beta1"},
}

// LookupRemovedAPI reports whether apiVersion/kind has been removed.
func LookupRemovedAPI(apiVersion, kind string) (RemovedAPI, bool) {
	if r, ok := removedAPIs[apiVersion+"/"+kind]; ok {
		return r, true
	}
	r, ok := removedAPIs[apiVersion+"/*"]
	return r, ok
}

// MinorVersion converts "1.25" or "v1.29.3" to 25 / 29.
func MinorVersion(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	return n
}

// PodTemplateLabels returns the labels of the pods a workload creates.
func PodTemplateLabels(o *manifest.Object) *yaml.Node {
	path, ok := podSpecPaths[o.Kind()]
	if !ok {
		return nil
	}
	if o.Kind() == "Pod" {
		return yamlx.Path(o.Root, "metadata", "labels")
	}
	// .../template/spec -> .../template/metadata/labels
	meta := append(append([]string{}, path[:len(path)-1]...), "metadata", "labels")
	return yamlx.Path(o.Root, meta...)
}

func labelsMatch(selector, labels *yaml.Node) bool {
	if labels == nil {
		return false
	}
	for i := 0; i+1 < len(selector.Content); i += 2 {
		if yamlx.String(labels, selector.Content[i].Value) != selector.Content[i+1].Value {
			return false
		}
	}
	return true
}

// labelsOverlap reports whether labels carry at least one of the selector's
// key=value pairs.
func labelsOverlap(selector, labels *yaml.Node) bool {
	if labels == nil {
		return false
	}
	for i := 0; i+1 < len(selector.Content); i += 2 {
		if yamlx.String(labels, selector.Content[i].Value) == selector.Content[i+1].Value {
			return true
		}
	}
	return false
}

func formatSelector(sel *yaml.Node) string {
	var kv []string
	for i := 0; i+1 < len(sel.Content); i += 2 {
		kv = append(kv, sel.Content[i].Value+"="+sel.Content[i+1].Value)
	}
	sort.Strings(kv)
	return strings.Join(kv, ",")
}

func init() {
	All = append(All,
		&Rule{
			ID: "KG016", Name: "service-target-port", Severity: Warning,
			Description: "A Service must select the workload it was written for (no near-miss selector labels), and its targetPort must match a declared containerPort.",
			Resource: func(o *manifest.Object, all []*manifest.Object) []string {
				if o.Kind() != "Service" {
					return nil
				}
				sel := yamlx.Path(o.Root, "spec", "selector")
				if sel == nil || sel.Kind != yaml.MappingNode || len(sel.Content) == 0 {
					return nil
				}
				var matched, near []*Target
				for _, w := range all {
					t := NewTarget(w)
					if t == nil || w.Namespace() != o.Namespace() {
						continue
					}
					labels := PodTemplateLabels(w)
					if labelsMatch(sel, labels) {
						matched = append(matched, t)
					} else if labelsOverlap(sel, labels) {
						near = append(near, t)
					}
				}
				if len(matched) == 0 {
					// Pods are often created outside the manifest set (operators,
					// controllers), so only a near miss is reported: a workload that
					// carries some, but not all, of the selector's labels.
					if len(near) > 0 {
						return []string{fmt.Sprintf("selector %s matches no workload; %s carries only some of these labels", formatSelector(sel), refs(near))}
					}
					return nil
				}
				numbers, names := map[string]bool{}, map[string]bool{}
				for _, t := range matched {
					for _, c := range t.Containers {
						for _, p := range yamlx.Items(yamlx.Get(c.Node, "ports")) {
							numbers[yamlx.String(p, "containerPort")] = true
							if n := yamlx.String(p, "name"); n != "" {
								names[n] = true
							}
						}
					}
				}
				var out []string
				for _, p := range yamlx.Items(yamlx.Path(o.Root, "spec", "ports")) {
					target := yamlx.String(p, "targetPort")
					if target == "" {
						target = yamlx.String(p, "port")
					}
					if _, err := strconv.Atoi(target); err == nil {
						// containerPort is informational: a numeric targetPort works
						// without it, so only flag it when ports are declared at all.
						if len(numbers) > 0 && !numbers[target] {
							out = append(out, fmt.Sprintf("targetPort %s does not match any containerPort of %s", target, refs(matched)))
						}
					} else if !names[target] {
						out = append(out, fmt.Sprintf("targetPort %q does not match any named container port of %s", target, refs(matched)))
					}
				}
				return out
			},
		},
		&Rule{
			ID: "KG017", Name: "removed-api-version", Severity: Error,
			Description: "Resources must not use API versions that have been removed from Kubernetes.",
			Resource: func(o *manifest.Object, _ []*manifest.Object) []string {
				if r, ok := LookupRemovedAPI(o.APIVersion(), o.Kind()); ok {
					return []string{fmt.Sprintf("apiVersion %s was removed in Kubernetes %s; use %s", o.APIVersion(), r.RemovedIn, r.Replacement)}
				}
				return nil
			},
		},
	)
}

func refs(ts []*Target) string {
	var s []string
	for _, t := range ts {
		s = append(s, t.Obj.Ref())
	}
	return strings.Join(s, ", ")
}
