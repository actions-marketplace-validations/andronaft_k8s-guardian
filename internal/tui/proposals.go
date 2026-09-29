package tui

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// Status of a proposal.
type Status int

const (
	Pending Status = iota
	Accepted
	Skipped
	Edited
	Loading
	Failed
)

// Kind of proposal.
type Kind int

const (
	RuleFix Kind = iota // deterministic fix of one finding
	AIFix               // Claude fix of all remaining findings of an object
	Manual              // no automatic fix available
)

// Proposal is one reviewable change.
type Proposal struct {
	Kind     Kind
	Findings []rules.Finding // one for RuleFix/Manual, several for AIFix
	File     *manifest.File
	Obj      *manifest.Object
	Status   Status
	Err      string

	rule      *rules.Rule
	container string
	init      bool

	aiRoot   *yaml.Node // Claude's proposal
	aiBase   string     // object YAML Claude saw
	aiNotes  string
	editRoot *yaml.Node // user-edited version
}

// Title is shown in the list.
func (p *Proposal) Title() string {
	f := p.Findings[0]
	switch p.Kind {
	case AIFix:
		ids := make([]string, 0, len(p.Findings))
		for _, f := range p.Findings {
			ids = append(ids, f.RuleID)
		}
		return fmt.Sprintf("🤖 %s: %s", p.Obj.Ref(), strings.Join(ids, ","))
	}
	t := f.RuleID + " " + f.Message
	if f.Container != "" {
		t = fmt.Sprintf("%s [%s] %s", f.RuleID, f.Container, f.Message)
	}
	return t
}

// Build creates proposals for all files: one per deterministic fix, then per
// object either a Claude proposal (withAI) or manual items.
func Build(files []*manifest.File, opts rules.Options, withAI bool) []*Proposal {
	var out []*Proposal
	for _, f := range files {
		for _, o := range f.Objects {
			var rest []rules.Finding
			for _, fd := range rules.Validate([]*manifest.Object{o}, opts) {
				r := lookup(opts, fd.RuleID)
				if r != nil && r.Fix != nil {
					p := &Proposal{Kind: RuleFix, Findings: []rules.Finding{fd}, File: f, Obj: o, rule: r, container: fd.Container}
					if t := rules.NewTarget(o); t != nil {
						for _, c := range t.Containers {
							if c.Name == fd.Container {
								p.init = c.Init
							}
						}
					}
					out = append(out, p)
					continue
				}
				rest = append(rest, fd)
			}
			if len(rest) == 0 {
				continue
			}
			if withAI && o.Kind() != "Secret" { // Secrets are never sent to the API
				out = append(out, &Proposal{Kind: AIFix, Findings: rest, File: f, Obj: o})
				continue
			}
			for _, fd := range rest {
				out = append(out, &Proposal{Kind: Manual, Findings: []rules.Finding{fd}, File: f, Obj: o, rule: lookup(opts, fd.RuleID)})
			}
		}
	}
	return out
}

func lookup(opts rules.Options, id string) *rules.Rule {
	for _, r := range opts.Rules() {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Clone deep-copies a node tree.
func Clone(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = Clone(ch)
	}
	return &c
}

// Encode renders a single object.
func Encode(root *yaml.Node) string {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	_ = enc.Encode(root)
	_ = enc.Close()
	return b.String()
}

// applyRule runs the proposal's fix on root. It reports false when the
// finding no longer applies (e.g. fixed by an earlier proposal).
func (p *Proposal) applyRule(root *yaml.Node) bool {
	o := &manifest.Object{Root: root, Source: p.Obj.Source}
	t := rules.NewTarget(o)
	if t == nil {
		return false
	}
	if p.rule.Pod != nil && p.container == "" {
		return p.rule.Pod(t) != "" && p.rule.Fix(t, nil)
	}
	for _, c := range t.Containers {
		if c.Name == p.container && c.Init == p.init {
			return p.rule.Container(t, c) != "" && p.rule.Fix(t, c)
		}
	}
	return false
}

// Preview returns the object before and after applying the proposal.
func (p *Proposal) Preview() (before, after string, ok bool) {
	before = Encode(p.Obj.Root)
	switch {
	case p.editRoot != nil:
		return before, Encode(p.editRoot), true
	case p.Kind == RuleFix:
		c := Clone(p.Obj.Root)
		if !p.applyRule(c) {
			return before, before, false
		}
		return before, Encode(c), true
	case p.Kind == AIFix && p.aiRoot != nil:
		return before, Encode(p.aiRoot), true
	}
	return before, before, false
}

// Proposed returns the root that accepting would install.
func (p *Proposal) proposed() *yaml.Node {
	switch {
	case p.editRoot != nil:
		return p.editRoot
	case p.Kind == RuleFix:
		c := Clone(p.Obj.Root)
		p.applyRule(c)
		return c
	case p.Kind == AIFix:
		return p.aiRoot
	}
	return nil
}

// Accept installs the proposal into the manifest (in place, so the File's
// document tree is updated).
func (p *Proposal) Accept() bool {
	root := p.proposed()
	if root == nil {
		return false
	}
	*p.Obj.Root = *Clone(root)
	if p.editRoot != nil {
		p.Status = Edited
	} else {
		p.Status = Accepted
	}
	return true
}

// NeedsAI reports whether the Claude proposal must be (re)fetched.
func (p *Proposal) NeedsAI() bool {
	return p.Kind == AIFix && p.Status != Loading && (p.aiRoot == nil || p.aiBase != Encode(p.Obj.Root)) && p.Status != Accepted && p.Status != Edited
}

// FetchAI asks Claude for a fix of the object in its current state.
func FetchAI(ctx context.Context, client *ai.Client, p *Proposal) (root *yaml.Node, base, notes string, err error) {
	base = Encode(p.Obj.Root)
	fixed, notes, err := client.Fix(ctx, base, p.Findings)
	if err != nil {
		return nil, base, "", err
	}
	f, err := manifest.Parse([]byte(fixed), p.Obj.Source)
	if err != nil {
		return nil, base, "", err
	}
	if len(f.Objects) != 1 {
		return nil, base, "", fmt.Errorf("expected exactly one Kubernetes object in the answer from Claude, got %d", len(f.Objects))
	}
	got := f.Objects[0]
	if got.Kind() != p.Obj.Kind() || got.Name() != p.Obj.Name() || got.Namespace() != p.Obj.Namespace() {
		return nil, base, "", fmt.Errorf("the answer from Claude describes %s, not %s; not applied", got.Ref(), p.Obj.Ref())
	}
	return got.Root, base, notes, nil
}
