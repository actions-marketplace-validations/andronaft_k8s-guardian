// Package rules implements the k8s-guardian policy checks and their
// deterministic auto-fixes.
package rules

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

// Severity of a finding.
type Severity int

const (
	Info Severity = iota
	Warning
	Error
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

func (s Severity) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// ParseSeverity converts "error", "warning" or "info".
func ParseSeverity(v string) (Severity, error) {
	switch strings.ToLower(v) {
	case "error":
		return Error, nil
	case "warning", "warn":
		return Warning, nil
	case "info":
		return Info, nil
	}
	return Info, fmt.Errorf("unknown severity %q (use error, warning or info)", v)
}

// Finding is a single policy violation.
type Finding struct {
	RuleID    string   `json:"ruleId"`
	Rule      string   `json:"rule"`
	Severity  Severity `json:"severity"`
	Message   string   `json:"message"`
	Source    string   `json:"source"`
	Resource  string   `json:"resource"`
	Namespace string   `json:"namespace,omitempty"`
	Container string   `json:"container,omitempty"`
	Line      int      `json:"line,omitempty"`
	Fixable   bool     `json:"fixable"`
	// UnsafeFix marks findings whose deterministic fix runs only with
	// --unsafe-fixes because it can change how the workload behaves.
	UnsafeFix bool `json:"unsafeFix,omitempty"`
}

// Container is a (init) container inside a pod spec.
type Container struct {
	Node *yaml.Node
	Name string
	Init bool
}

// Target is a workload resource with a pod spec.
type Target struct {
	Obj        *manifest.Object
	PodSpec    *yaml.Node
	Containers []*Container
}

// SecurityContext returns the container's securityContext (may be nil).
func (c *Container) SecurityContext() *yaml.Node { return yamlx.Get(c.Node, "securityContext") }

// EnsureSecurityContext returns the container's securityContext, creating it.
func (c *Container) EnsureSecurityContext() *yaml.Node {
	return yamlx.EnsureMap(c.Node, "securityContext")
}

// PodSecurityContext returns spec.securityContext of the pod (may be nil).
func (t *Target) PodSecurityContext() *yaml.Node { return yamlx.Get(t.PodSpec, "securityContext") }

// IsBatch reports whether the workload runs to completion: a Job, a CronJob,
// or a bare Pod that is not restarted (e.g. a Helm test hook).
func (t *Target) IsBatch() bool {
	switch t.Obj.Kind() {
	case "Job", "CronJob":
		return true
	case "Pod":
		p := yamlx.String(t.PodSpec, "restartPolicy")
		return p == "Never" || p == "OnFailure"
	}
	return false
}

// Rule is a single policy.
type Rule struct {
	ID          string
	Name        string
	Severity    Severity
	Description string
	// Container returns a violation message for a container, or "".
	Container func(t *Target, c *Container) string
	// Pod returns a violation message for the pod spec / workload, or "".
	Pod func(t *Target) string
	// Resource checks any object (not only workloads). all holds every
	// object of the current run so rules can cross-reference resources.
	Resource func(o *manifest.Object, all []*manifest.Object) []string
	// Custom marks rules loaded from user rule files.
	Custom bool
	// Fix applies a deterministic remediation. c is nil for pod-level rules.
	// Rules without Fix can only be fixed with --ai.
	Fix func(t *Target, c *Container) bool
	// Unsafe marks a Fix that can break a working workload (a root image
	// that can no longer start, an app that writes to its filesystem,
	// guessed resource values). It is applied only with Options.UnsafeFixes.
	Unsafe bool
}

// AutoFix reports whether --fix applies the rule's fix under opts.
func (r *Rule) AutoFix(opts Options) bool {
	return r.Fix != nil && (!r.Unsafe || opts.UnsafeFixes)
}

// podSpecPaths maps workload kinds to the path of their pod spec.
var podSpecPaths = map[string][]string{
	"Pod":                   {"spec"},
	"Deployment":            {"spec", "template", "spec"},
	"StatefulSet":           {"spec", "template", "spec"},
	"DaemonSet":             {"spec", "template", "spec"},
	"ReplicaSet":            {"spec", "template", "spec"},
	"ReplicationController": {"spec", "template", "spec"},
	"Job":                   {"spec", "template", "spec"},
	"CronJob":               {"spec", "jobTemplate", "spec", "template", "spec"},
}

// NewTarget returns the workload view of o, or nil if o has no pod spec.
func NewTarget(o *manifest.Object) *Target {
	path, ok := podSpecPaths[o.Kind()]
	if !ok {
		return nil
	}
	spec := yamlx.Path(o.Root, path...)
	if spec == nil || spec.Kind != yaml.MappingNode {
		return nil
	}
	t := &Target{Obj: o, PodSpec: spec}
	for _, key := range []string{"initContainers", "containers"} {
		seq := yamlx.Get(spec, key)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, n := range seq.Content {
			if n.Kind == yaml.MappingNode {
				t.Containers = append(t.Containers, &Container{Node: n, Name: yamlx.String(n, "name"), Init: key == "initContainers"})
			}
		}
	}
	return t
}
