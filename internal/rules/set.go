package rules

import (
	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Set is the manifest set that resource rules look at. Lookups that rules
// repeat for every object are built once, on first use, so a run stays
// linear in the number of objects.
type Set struct {
	Objects []*manifest.Object

	workloads map[string][]workload       // namespace -> workloads, in manifest order
	hpas      map[string]*manifest.Object // namespace, kind, name of the scale target -> first HPA
}

type workload struct {
	target *Target
	labels *yaml.Node // pod template labels
}

// NewSet wraps the objects of one run.
func NewSet(objs []*manifest.Object) *Set { return &Set{Objects: objs} }

// workloadsIn returns the objects with a pod template in namespace ns.
func (s *Set) workloadsIn(ns string) []workload {
	if s.workloads == nil {
		s.workloads = map[string][]workload{}
		for _, o := range s.Objects {
			if t := NewTarget(o); t != nil {
				s.workloads[o.Namespace()] = append(s.workloads[o.Namespace()], workload{t, PodTemplateLabels(o)})
			}
		}
	}
	return s.workloads[ns]
}

// hpaFor returns the HorizontalPodAutoscaler that scales o, if any.
func (s *Set) hpaFor(o *manifest.Object) *manifest.Object {
	key := func(ns, kind, name string) string { return ns + "\x00" + kind + "\x00" + name }
	if s.hpas == nil {
		s.hpas = map[string]*manifest.Object{}
		for _, h := range s.Objects {
			if h.Kind() != "HorizontalPodAutoscaler" {
				continue
			}
			k := key(h.Namespace(), yamlx.String(h.Root, "spec", "scaleTargetRef", "kind"), yamlx.String(h.Root, "spec", "scaleTargetRef", "name"))
			if _, ok := s.hpas[k]; !ok {
				s.hpas[k] = h
			}
		}
	}
	return s.hpas[key(o.Namespace(), o.Kind(), o.Name())]
}
