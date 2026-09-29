// Package custom implements user-defined guardrail rules written in a small
// declarative YAML format (see docs/custom-rules.md). Rules can be written by
// hand or generated from natural language with `k8s-guardian rule create`.
package custom

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/yamlx"
)

const (
	APIVersion = "k8s-guardian.io/v1"
	Kind       = "Rule"
	// DefaultDir is loaded automatically when it exists in the working directory.
	DefaultDir = ".k8s-guardian/rules"
)

// Document is the on-disk representation of a custom rule.
type Document struct {
	APIVersion string   `yaml:"apiVersion" json:"-"`
	Kind       string   `yaml:"kind" json:"-"`
	Metadata   Metadata `yaml:"metadata" json:"-"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name string `yaml:"name"`
}

// Spec defines what a rule checks.
type Spec struct {
	ID          string      `yaml:"id" json:"id"`
	Severity    string      `yaml:"severity" json:"severity"`
	Description string      `yaml:"description" json:"description"`
	Message     string      `yaml:"message,omitempty" json:"message"`
	Match       Match       `yaml:"match" json:"match"`
	When        []Condition `yaml:"when,omitempty" json:"when"`
	Assert      []Condition `yaml:"assert" json:"assert"`
}

// Match selects the objects and the scope that paths are relative to.
type Match struct {
	Kinds []string `yaml:"kinds,omitempty" json:"kinds"`
	// Scope: "resource" (default, paths relative to the object),
	// "pod" (relative to the pod spec) or "container" (each container).
	Scope string `yaml:"scope,omitempty" json:"scope"`
}

// Condition is a single assertion on a path.
type Condition struct {
	Path   string   `yaml:"path" json:"path"`
	Op     string   `yaml:"op" json:"op"`
	Value  string   `yaml:"value,omitempty" json:"value"`
	Values []string `yaml:"values,omitempty" json:"values"`
}

// Ops lists the supported operators.
var Ops = []string{"exists", "notExists", "equals", "notEquals", "in", "notIn", "matches", "notMatches", "gt", "gte", "lt", "lte"}

type compiledCond struct {
	Condition
	path []segment
	re   *regexp.Regexp
	num  float64
}

type segment struct {
	key   string
	index int  // >=0 for [N]
	all   bool // [*]
}

// parsePath parses `a.b['c.d'][0].e[*]`.
func parsePath(p string) ([]segment, error) {
	var segs []segment
	i := 0
	readKey := func() string {
		start := i
		for i < len(p) && p[i] != '.' && p[i] != '[' {
			i++
		}
		return p[start:i]
	}
	for i < len(p) {
		switch p[i] {
		case '.':
			i++
		case '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unclosed [ in path %q", p)
			}
			inner := strings.Trim(p[i+1:i+end], `'"`)
			i += end + 1
			switch n, err := strconv.Atoi(inner); {
			case inner == "*":
				segs = append(segs, segment{all: true, index: -1})
			case err == nil:
				segs = append(segs, segment{index: n})
			default:
				segs = append(segs, segment{key: inner, index: -1})
			}
		default:
			segs = append(segs, segment{key: readKey(), index: -1})
		}
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	return segs, nil
}

func resolve(n *yaml.Node, segs []segment) []*yaml.Node {
	cur := []*yaml.Node{n}
	for _, s := range segs {
		var next []*yaml.Node
		for _, c := range cur {
			switch {
			case s.all:
				next = append(next, yamlx.Items(c)...)
			case s.index >= 0:
				if items := yamlx.Items(c); s.index < len(items) {
					next = append(next, items[s.index])
				}
			default:
				if v := yamlx.Get(c, s.key); v != nil && v.Tag != "!!null" {
					next = append(next, v)
				}
			}
		}
		cur = next
	}
	return cur
}

func compileCond(c Condition) (*compiledCond, error) {
	cc := &compiledCond{Condition: c}
	var err error
	if cc.path, err = parsePath(c.Path); err != nil {
		return nil, err
	}
	switch c.Op {
	case "exists", "notExists":
	case "equals", "notEquals":
	case "in", "notIn":
		if len(c.Values) == 0 {
			return nil, fmt.Errorf("op %s on %q needs values", c.Op, c.Path)
		}
	case "matches", "notMatches":
		if cc.re, err = regexp.Compile(c.Value); err != nil {
			return nil, fmt.Errorf("invalid regex for %q: %w", c.Path, err)
		}
	case "gt", "gte", "lt", "lte":
		if cc.num, err = quantity.Parse(c.Value); err != nil {
			return nil, fmt.Errorf("op %s on %q needs a numeric/quantity value: %w", c.Op, c.Path, err)
		}
	default:
		return nil, fmt.Errorf("unknown op %q (supported: %s)", c.Op, strings.Join(Ops, ", "))
	}
	return cc, nil
}

func scalar(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	b, _ := yaml.Marshal(n)
	return strings.TrimSpace(string(b))
}

// holds reports whether the condition is satisfied at node n. Comparisons and
// regex matches on a missing path are considered satisfied except for
// "matches", "equals" and "in"; combine with "exists" to require a field.
func (c *compiledCond) holds(n *yaml.Node) bool {
	nodes := resolve(n, c.path)
	switch c.Op {
	case "exists":
		return len(nodes) > 0
	case "notExists":
		return len(nodes) == 0
	case "matches", "equals", "in":
		if len(nodes) == 0 {
			return false
		}
	}
	for _, v := range nodes {
		s := scalar(v)
		ok := true
		switch c.Op {
		case "equals":
			ok = s == c.Value
		case "notEquals":
			ok = s != c.Value
		case "in", "notIn":
			found := false
			for _, want := range c.Values {
				if s == want {
					found = true
				}
			}
			ok = found == (c.Op == "in")
		case "matches":
			ok = c.re.MatchString(s)
		case "notMatches":
			ok = !c.re.MatchString(s)
		case "gt", "gte", "lt", "lte":
			x, err := quantity.Parse(s)
			if err != nil {
				ok = false
				break
			}
			ok = map[string]bool{"gt": x > c.num, "gte": x >= c.num, "lt": x < c.num, "lte": x <= c.num}[c.Op]
		}
		if !ok {
			return false
		}
	}
	return true
}

func (c *compiledCond) String() string {
	switch {
	case c.Op == "exists" || c.Op == "notExists":
		return c.Path + " " + c.Op
	case len(c.Values) > 0:
		return fmt.Sprintf("%s %s [%s]", c.Path, c.Op, strings.Join(c.Values, ", "))
	}
	return fmt.Sprintf("%s %s %q", c.Path, c.Op, c.Value)
}

// Compile validates a document and turns it into an engine rule.
func Compile(d Document) (*rules.Rule, error) {
	s := d.Spec
	name := d.Metadata.Name
	if name == "" {
		return nil, errors.New("metadata.name is required")
	}
	if s.ID == "" {
		return nil, fmt.Errorf("rule %s: spec.id is required", name)
	}
	if strings.HasPrefix(strings.ToUpper(s.ID), "KG") {
		return nil, fmt.Errorf("rule %s: id %s is reserved for built-in rules", name, s.ID)
	}
	sev, err := rules.ParseSeverity(s.Severity)
	if err != nil {
		return nil, fmt.Errorf("rule %s: %w", name, err)
	}
	if len(s.Assert) == 0 {
		return nil, fmt.Errorf("rule %s: spec.assert must contain at least one condition", name)
	}
	compile := func(cs []Condition) ([]*compiledCond, error) {
		var out []*compiledCond
		for _, c := range cs {
			cc, err := compileCond(c)
			if err != nil {
				return nil, fmt.Errorf("rule %s: %w", name, err)
			}
			out = append(out, cc)
		}
		return out, nil
	}
	when, err := compile(s.When)
	if err != nil {
		return nil, err
	}
	assert, err := compile(s.Assert)
	if err != nil {
		return nil, err
	}
	kinds := map[string]bool{}
	for _, k := range s.Match.Kinds {
		kinds[strings.ToLower(k)] = true
	}
	kindOK := func(o *manifest.Object) bool { return len(kinds) == 0 || kinds[strings.ToLower(o.Kind())] }
	eval := func(n *yaml.Node) string {
		for _, c := range when {
			if !c.holds(n) {
				return ""
			}
		}
		for _, c := range assert {
			if !c.holds(n) {
				if s.Message != "" {
					return s.Message
				}
				return "violates: " + c.String()
			}
		}
		return ""
	}
	r := &rules.Rule{ID: s.ID, Name: name, Severity: sev, Description: s.Description, Custom: true}
	switch s.Match.Scope {
	case "", "resource":
		r.Resource = func(o *manifest.Object, _ []*manifest.Object) []string {
			if !kindOK(o) {
				return nil
			}
			if msg := eval(o.Root); msg != "" {
				return []string{msg}
			}
			return nil
		}
	case "pod":
		r.Pod = func(t *rules.Target) string {
			if !kindOK(t.Obj) {
				return ""
			}
			return eval(t.PodSpec)
		}
	case "container":
		r.Container = func(t *rules.Target, c *rules.Container) string {
			if !kindOK(t.Obj) {
				return ""
			}
			return eval(c.Node)
		}
	default:
		return nil, fmt.Errorf("rule %s: unknown scope %q (use resource, pod or container)", name, s.Match.Scope)
	}
	return r, nil
}

// Parse decodes a YAML stream of rule documents.
func Parse(data []byte, source string) ([]Document, error) {
	var docs []Document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var d Document
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		if d.Kind == "" && d.Metadata.Name == "" {
			continue
		}
		if d.Kind != Kind || d.APIVersion != APIVersion {
			return nil, fmt.Errorf("%s: expected apiVersion %s, kind %s", source, APIVersion, Kind)
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// Load reads rule files from the given paths (files or directories). A
// missing DefaultDir is silently ignored.
func Load(paths []string) ([]*rules.Rule, error) {
	var out []*rules.Rule
	seen := map[string]string{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if p == DefaultDir && os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		files := []string{p}
		if info.IsDir() {
			files, _ = filepath.Glob(filepath.Join(p, "*.y*ml"))
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			docs, err := Parse(data, f)
			if err != nil {
				return nil, err
			}
			for _, d := range docs {
				r, err := Compile(d)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", f, err)
				}
				if prev, dup := seen[r.ID]; dup {
					return nil, fmt.Errorf("%s: duplicate rule id %s (also in %s)", f, r.ID, prev)
				}
				seen[r.ID] = f
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// Marshal renders a document as YAML.
func Marshal(d Document) ([]byte, error) {
	d.APIVersion, d.Kind = APIVersion, Kind
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
