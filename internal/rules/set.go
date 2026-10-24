package rules

import (
	"go.yaml.in/yaml/v3"

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
	pdbs      map[string][]pdb            // namespace -> PodDisruptionBudgets, in manifest order
}

// pdbsIn returns the PodDisruptionBudgets in namespace ns.
func (s *Set) pdbsIn(ns string) []pdb {
	if s.pdbs == nil {
		s.pdbs = map[string][]pdb{}
		for _, o := range s.Objects {
			if o.Kind() == "PodDisruptionBudget" {
				s.pdbs[o.Namespace()] = append(s.pdbs[o.Namespace()], pdb{obj: o, selector: yamlx.Path(o.Root, "spec", "selector")})
			}
		}
	}
	return s.pdbs[ns]
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

// HPAFor returns the HorizontalPodAutoscaler in the set that scales o, if
// any.
func (s *Set) HPAFor(o *manifest.Object) *manifest.Object {
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
