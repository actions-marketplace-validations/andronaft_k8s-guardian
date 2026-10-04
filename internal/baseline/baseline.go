// Package baseline records known findings so a check only fails on new
// ones. That lets a team adopt k8s-guardian in an existing repository and
// fix the backlog over time.
package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/andronaft/k8s-guardian/internal/fsutil"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// Entry identifies a finding. Line numbers are left out on purpose: code
// moving up or down must not turn a known finding into a new one.
type Entry struct {
	Rule      string `json:"rule"`
	Source    string `json:"source"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Container string `json:"container,omitempty"`
	Message   string `json:"message"`
}

type file struct {
	Version  int     `json:"version"`
	Findings []Entry `json:"findings"`
}

func entry(f rules.Finding) Entry {
	return Entry{
		Rule:      f.RuleID,
		Source:    filepath.ToSlash(filepath.Clean(f.Source)),
		Resource:  f.Resource,
		Namespace: f.Namespace,
		Container: f.Container,
		Message:   f.Message,
	}
}

// Write stores findings as the new baseline, sorted so the file diffs well.
func Write(path string, fs []rules.Finding) error {
	b := file{Version: 1, Findings: []Entry{}}
	for _, f := range fs {
		b.Findings = append(b.Findings, entry(f))
	}
	sort.Slice(b.Findings, func(i, j int) bool {
		x, y := b.Findings[i], b.Findings[j]
		if x.Source != y.Source {
			return x.Source < y.Source
		}
		if x.Resource != y.Resource {
			return x.Resource < y.Resource
		}
		if x.Rule != y.Rule {
			return x.Rule < y.Rule
		}
		if x.Container != y.Container {
			return x.Container < y.Container
		}
		return x.Message < y.Message
	})
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, append(data, '\n'))
}

// Filter returns the findings that are not in the baseline at path and the
// number of known ones it removed. A finding recorded n times hides at most
// n occurrences.
func Filter(path string, fs []rules.Finding) ([]rules.Finding, int, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the user's baseline file
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, fmt.Errorf("baseline %s does not exist; create it with --update-baseline", path)
		}
		return nil, 0, err
	}
	var b file
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, 0, fmt.Errorf("baseline %s: %w", path, err)
	}
	if b.Version != 1 {
		return nil, 0, fmt.Errorf("baseline %s: unsupported version %d", path, b.Version)
	}
	known := map[Entry]int{}
	for _, e := range b.Findings {
		known[e]++
	}
	var out []rules.Finding
	hidden := 0
	for _, f := range fs {
		e := entry(f)
		if known[e] > 0 {
			known[e]--
			hidden++
			continue
		}
		out = append(out, f)
	}
	return out, hidden, nil
}
