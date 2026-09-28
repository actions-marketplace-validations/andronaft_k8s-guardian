package rules

import (
	"sort"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/manifest"
)

// IgnoreAnnotation lists rule IDs or names to skip for a resource,
// e.g. `k8s-guardian.io/ignore: "KG006,liveness-probe"`.
const IgnoreAnnotation = "k8s-guardian.io/ignore"

// Options controls which rules run.
type Options struct {
	Skip map[string]bool // rule IDs/names (lower-case)
}

// NewOptions builds Options from a comma separated skip list.
func NewOptions(skip string) Options {
	o := Options{Skip: map[string]bool{}}
	for _, s := range strings.Split(skip, ",") {
		if s = strings.TrimSpace(s); s != "" {
			o.Skip[strings.ToLower(s)] = true
		}
	}
	return o
}

func (o Options) enabled(r *Rule, obj *manifest.Object) bool {
	if o.Skip[strings.ToLower(r.ID)] || o.Skip[strings.ToLower(r.Name)] {
		return false
	}
	for _, s := range strings.Split(obj.Annotation(IgnoreAnnotation), ",") {
		s = strings.TrimSpace(s)
		if strings.EqualFold(s, r.ID) || strings.EqualFold(s, r.Name) || s == "*" {
			return false
		}
	}
	return true
}

// Validate runs every enabled rule against the objects.
func Validate(objs []*manifest.Object, opts Options) []Finding {
	var out []Finding
	for _, o := range objs {
		t := NewTarget(o)
		if t == nil {
			continue
		}
		for _, r := range All {
			if !opts.enabled(r, o) {
				continue
			}
			if r.Pod != nil {
				if msg := r.Pod(t); msg != "" {
					out = append(out, newFinding(r, t, nil, msg))
				}
			}
			if r.Container != nil {
				for _, c := range t.Containers {
					if msg := r.Container(t, c); msg != "" {
						out = append(out, newFinding(r, t, c, msg))
					}
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// Fix applies every deterministic fix for violated, enabled rules and
// returns the number of changes made.
func Fix(objs []*manifest.Object, opts Options) int {
	n := 0
	for _, o := range objs {
		t := NewTarget(o)
		if t == nil {
			continue
		}
		for _, r := range All {
			if r.Fix == nil || !opts.enabled(r, o) {
				continue
			}
			if r.Pod != nil && r.Pod(t) != "" && r.Fix(t, nil) {
				n++
			}
			if r.Container != nil {
				for _, c := range t.Containers {
					if r.Container(t, c) != "" && r.Fix(t, c) {
						n++
					}
				}
			}
		}
	}
	return n
}

func newFinding(r *Rule, t *Target, c *Container, msg string) Finding {
	f := Finding{
		RuleID:    r.ID,
		Rule:      r.Name,
		Severity:  r.Severity,
		Message:   msg,
		Source:    t.Obj.Source,
		Resource:  t.Obj.Ref(),
		Namespace: t.Obj.Namespace(),
		Line:      t.PodSpec.Line,
		Fixable:   r.Fix != nil,
	}
	if c != nil {
		f.Container = c.Name
		f.Line = c.Node.Line
	}
	return f
}
