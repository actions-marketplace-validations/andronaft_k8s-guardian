// Package cluster reads live cluster state (version, nodes, quotas, limit
// ranges, existing objects) through kubectl. Everything is cached per run.
package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/andronaft/k8s-guardian/internal/k8sname"
	"github.com/andronaft/k8s-guardian/internal/kube"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/quantity"
)

// ErrNotFound is returned by Get when an object does not exist.
var ErrNotFound = errors.New("not found")

// Node is a schedulable node's capacity.
type Node struct {
	Name          string
	Labels        map[string]string
	AllocCPU      string // quantity
	AllocMemory   string // quantity
	Unschedulable bool
}

// Quota is a ResourceQuota with its hard limits and current usage.
type Quota struct {
	Name string
	Hard map[string]string
	Used map[string]string
}

// LimitRangeItem is one entry of a LimitRange (type Container or Pod).
type LimitRangeItem struct {
	LimitRange     string
	Type           string
	Max            map[string]string
	Min            map[string]string
	Default        map[string]string // default limits
	DefaultRequest map[string]string
}

// Namespace aggregates namespaced state.
type Namespace struct {
	Name        string
	Exists      bool
	Quotas      []Quota
	LimitRanges []LimitRangeItem
	// Names holds existing object names by lower-case kind
	// (configmap, secret, serviceaccount, persistentvolumeclaim).
	Names map[string]map[string]bool
}

// StorageClasses lists storage classes and the default one.
type StorageClasses struct {
	Names   map[string]bool
	Default string
}

// Cluster is the read-only view k8s-guardian needs. Implemented by Kubectl
// and by fakes in tests.
type Cluster interface {
	Version() (string, error)
	APIVersions() (map[string]bool, error)
	DefaultNamespace() string
	Nodes() ([]Node, error)
	Namespace(name string) (*Namespace, error)
	StorageClasses() (*StorageClasses, error)
	PriorityClasses() (map[string]bool, error)
	// Get returns a live object (cleaned of server fields) or ErrNotFound.
	Get(kind, apiVersion, namespace, name string) (*manifest.Object, error)
	// PodUsage returns the current CPU/memory usage (metrics-server) of the
	// pods matching selector, averaged per container name, and the number
	// of pods sampled.
	PodUsage(namespace string, selector map[string]string) (map[string]Usage, int, error)
}

// Usage is the observed consumption of a container.
type Usage struct {
	CPU    string // e.g. "12m"
	Memory string // e.g. "48Mi"
}

// Kubectl implements Cluster by shelling out to kubectl.
type Kubectl struct {
	Context string

	mu    sync.Mutex
	cache map[string]any
}

// NewKubectl checks kubectl is available.
func NewKubectl(context string) (*Kubectl, error) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, errors.New("kubectl not found in PATH (needed for --live, diff and audit)")
	}
	return &Kubectl{Context: context, cache: map[string]any{}}, nil
}

func (k *Kubectl) run(args ...string) ([]byte, error) {
	if k.Context != "" {
		args = append(args, "--context="+k.Context)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("kubectl", args...) // #nosec G204 -- no shell; every value is validated by internal/k8sname
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "NotFound") || strings.Contains(msg, "not found") {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, msg)
		}
		return nil, fmt.Errorf("kubectl %s: %s", strings.Join(args, " "), firstLine(msg, err))
	}
	return out, nil
}

func firstLine(msg string, err error) string {
	if msg == "" {
		return err.Error()
	}
	return strings.SplitN(msg, "\n", 2)[0]
}

func cached[T any](k *Kubectl, key string, load func() (T, error)) (T, error) {
	k.mu.Lock()
	if v, ok := k.cache[key]; ok {
		k.mu.Unlock()
		if e, isErr := v.(error); isErr {
			var zero T
			return zero, e
		}
		return v.(T), nil
	}
	k.mu.Unlock()
	v, err := load()
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		k.cache[key] = err
		return v, err
	}
	k.cache[key] = v
	return v, nil
}

func (k *Kubectl) Version() (string, error) {
	return cached(k, "version", func() (string, error) {
		out, err := k.run("version", "-o", "json")
		if err != nil {
			return "", err
		}
		var v struct {
			ServerVersion struct {
				GitVersion string `json:"gitVersion"`
			} `json:"serverVersion"`
		}
		if err := json.Unmarshal(out, &v); err != nil {
			return "", err
		}
		return v.ServerVersion.GitVersion, nil
	})
}

func (k *Kubectl) APIVersions() (map[string]bool, error) {
	return cached(k, "api-versions", func() (map[string]bool, error) {
		out, err := k.run("api-versions")
		if err != nil {
			return nil, err
		}
		m := map[string]bool{}
		for _, l := range strings.Fields(string(out)) {
			m[l] = true
		}
		return m, nil
	})
}

func (k *Kubectl) DefaultNamespace() string {
	ns, _ := cached(k, "default-ns", func() (string, error) {
		out, err := k.run("config", "view", "--minify", "-o", "jsonpath={..namespace}")
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return "default", nil
		}
		return strings.TrimSpace(string(out)), nil
	})
	return ns
}

type list[T any] struct {
	Items []T `json:"items"`
}

type meta struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

func (k *Kubectl) Nodes() ([]Node, error) {
	return cached(k, "nodes", func() ([]Node, error) {
		out, err := k.run("get", "nodes", "-o", "json")
		if err != nil {
			return nil, err
		}
		var l list[struct {
			Metadata meta `json:"metadata"`
			Spec     struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Allocatable map[string]string `json:"allocatable"`
			} `json:"status"`
		}]
		if err := json.Unmarshal(out, &l); err != nil {
			return nil, err
		}
		var nodes []Node
		for _, n := range l.Items {
			nodes = append(nodes, Node{Name: n.Metadata.Name, Labels: n.Metadata.Labels,
				AllocCPU: n.Status.Allocatable["cpu"], AllocMemory: n.Status.Allocatable["memory"],
				Unschedulable: n.Spec.Unschedulable})
		}
		return nodes, nil
	})
}

func (k *Kubectl) Namespace(name string) (*Namespace, error) {
	if err := k8sname.ValidNamespace(name); err != nil {
		return nil, err
	}
	return cached(k, "ns/"+name, func() (*Namespace, error) {
		ns := &Namespace{Name: name, Names: map[string]map[string]bool{}}
		if _, err := k.run("get", "namespace/"+name, "-o", "name"); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ns, nil
			}
			return nil, err
		}
		ns.Exists = true

		out, err := k.run("get", "resourcequota", "--namespace="+name, "-o", "json")
		if err != nil {
			return nil, err
		}
		var ql list[struct {
			Metadata meta `json:"metadata"`
			Status   struct {
				Hard map[string]string `json:"hard"`
				Used map[string]string `json:"used"`
			} `json:"status"`
		}]
		if err := json.Unmarshal(out, &ql); err != nil {
			return nil, err
		}
		for _, q := range ql.Items {
			ns.Quotas = append(ns.Quotas, Quota{Name: q.Metadata.Name, Hard: q.Status.Hard, Used: q.Status.Used})
		}

		out, err = k.run("get", "limitrange", "--namespace="+name, "-o", "json")
		if err != nil {
			return nil, err
		}
		var ll list[struct {
			Metadata meta `json:"metadata"`
			Spec     struct {
				Limits []struct {
					Type           string            `json:"type"`
					Max            map[string]string `json:"max"`
					Min            map[string]string `json:"min"`
					Default        map[string]string `json:"default"`
					DefaultRequest map[string]string `json:"defaultRequest"`
				} `json:"limits"`
			} `json:"spec"`
		}]
		if err := json.Unmarshal(out, &ll); err != nil {
			return nil, err
		}
		for _, lr := range ll.Items {
			for _, it := range lr.Spec.Limits {
				ns.LimitRanges = append(ns.LimitRanges, LimitRangeItem{LimitRange: lr.Metadata.Name, Type: it.Type,
					Max: it.Max, Min: it.Min, Default: it.Default, DefaultRequest: it.DefaultRequest})
			}
		}

		out, err = k.run("get", "configmaps,secrets,serviceaccounts,persistentvolumeclaims", "--namespace="+name, "-o", "name")
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Fields(string(out)) {
			kind, n, ok := strings.Cut(l, "/")
			if !ok {
				continue
			}
			kind = strings.SplitN(kind, ".", 2)[0]
			if ns.Names[kind] == nil {
				ns.Names[kind] = map[string]bool{}
			}
			ns.Names[kind][n] = true
		}
		return ns, nil
	})
}

func (k *Kubectl) StorageClasses() (*StorageClasses, error) {
	return cached(k, "storageclasses", func() (*StorageClasses, error) {
		out, err := k.run("get", "storageclasses", "-o", "json")
		if err != nil {
			return nil, err
		}
		var l list[struct {
			Metadata meta `json:"metadata"`
		}]
		if err := json.Unmarshal(out, &l); err != nil {
			return nil, err
		}
		sc := &StorageClasses{Names: map[string]bool{}}
		for _, it := range l.Items {
			sc.Names[it.Metadata.Name] = true
			if it.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
				sc.Default = it.Metadata.Name
			}
		}
		return sc, nil
	})
}

func (k *Kubectl) PriorityClasses() (map[string]bool, error) {
	return cached(k, "priorityclasses", func() (map[string]bool, error) {
		out, err := k.run("get", "priorityclasses", "-o", "name")
		if err != nil {
			return nil, err
		}
		m := map[string]bool{}
		for _, l := range strings.Fields(string(out)) {
			if _, n, ok := strings.Cut(l, "/"); ok {
				m[n] = true
			}
		}
		return m, nil
	})
}

// resourceArg builds "kind.group" so kubectl resolves the right API group.
func resourceArg(kind, apiVersion string) string {
	arg := strings.ToLower(kind)
	if group, _, ok := strings.Cut(apiVersion, "/"); ok {
		arg += "." + group
	}
	return arg
}

func (k *Kubectl) Get(kind, apiVersion, namespace, name string) (*manifest.Object, error) {
	group, _, _ := strings.Cut(apiVersion, "/")
	if !strings.Contains(apiVersion, "/") {
		group = ""
	}
	if err := k8sname.ValidKind(kind, group); err != nil {
		return nil, err
	}
	if err := k8sname.ValidName(name); err != nil {
		return nil, err
	}
	args := []string{"get", resourceArg(kind, apiVersion) + "/" + name, "-o", "yaml"}
	if namespace != "" {
		if err := k8sname.ValidNamespace(namespace); err != nil {
			return nil, err
		}
		args = append(args, "--namespace="+namespace)
	}
	out, err := k.run(args...)
	if err != nil {
		return nil, err
	}
	f, err := manifest.Parse(out, "cluster")
	if err != nil {
		return nil, err
	}
	if len(f.Objects) == 0 {
		return nil, ErrNotFound
	}
	kube.Clean(f.Objects[0])
	return f.Objects[0], nil
}

// PodUsage runs `kubectl top pods --containers` (requires metrics-server).
func (k *Kubectl) PodUsage(namespace string, selector map[string]string) (map[string]Usage, int, error) {
	if err := k8sname.ValidNamespace(namespace); err != nil {
		return nil, 0, err
	}
	if err := k8sname.ValidSelector(selector); err != nil {
		return nil, 0, err
	}
	var sel []string
	for key, v := range selector {
		sel = append(sel, key+"="+v)
	}
	sort.Strings(sel)
	args := []string{"top", "pods", "--containers", "--no-headers"}
	args = append(args, "--namespace="+namespace)
	if len(sel) > 0 {
		args = append(args, "--selector="+strings.Join(sel, ","))
	}
	out, err := k.run(args...)
	if err != nil {
		return nil, 0, err
	}
	return ParseTop(string(out))
}

// ParseTop parses `kubectl top pods --containers --no-headers` output
// ("POD CONTAINER CPU MEMORY" per line) into per-container averages.
func ParseTop(out string) (map[string]Usage, int, error) {
	type acc struct {
		cpu, mem float64
		n        int
	}
	sums := map[string]*acc{}
	pods := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		cpu, err1 := quantity.Parse(f[2])
		mem, err2 := quantity.Parse(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		pods[f[0]] = true
		a := sums[f[1]]
		if a == nil {
			a = &acc{}
			sums[f[1]] = a
		}
		a.cpu += cpu
		a.mem += mem
		a.n++
	}
	res := map[string]Usage{}
	for name, a := range sums {
		res[name] = Usage{
			CPU:    quantity.FormatCPU(int64(math.Round(a.cpu / float64(a.n) * 1000))),
			Memory: quantity.FormatBytes(int64(math.Round(a.mem / float64(a.n)))),
		}
	}
	return res, len(pods), nil
}
