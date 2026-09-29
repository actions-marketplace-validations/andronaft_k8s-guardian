package cluster

import (
	"github.com/andronaft/k8s-guardian/internal/manifest"
)

// Fake is an in-memory Cluster for tests and demos.
type Fake struct {
	GitVersion string
	APIs       map[string]bool
	NodeList   []Node
	Namespaces map[string]*Namespace
	SCs        *StorageClasses
	PCs        map[string]bool
	Objects    map[string]*manifest.Object // "Kind/namespace/name"
}

func (f *Fake) Version() (string, error)              { return f.GitVersion, nil }
func (f *Fake) APIVersions() (map[string]bool, error) { return f.APIs, nil }
func (f *Fake) DefaultNamespace() string              { return "default" }
func (f *Fake) Nodes() ([]Node, error)                { return f.NodeList, nil }
func (f *Fake) PriorityClasses() (map[string]bool, error) {
	return f.PCs, nil
}
func (f *Fake) StorageClasses() (*StorageClasses, error) {
	if f.SCs == nil {
		return &StorageClasses{Names: map[string]bool{}}, nil
	}
	return f.SCs, nil
}
func (f *Fake) Namespace(name string) (*Namespace, error) {
	if ns, ok := f.Namespaces[name]; ok {
		return ns, nil
	}
	return &Namespace{Name: name}, nil
}
func (f *Fake) Get(kind, _, namespace, name string) (*manifest.Object, error) {
	if o, ok := f.Objects[kind+"/"+namespace+"/"+name]; ok {
		return o, nil
	}
	return nil, ErrNotFound
}
