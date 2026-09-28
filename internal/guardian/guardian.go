// Package guardian ties validation, deterministic fixes and AI fixes
// together. It is shared by the CLI and the MCP server.
package guardian

import (
	"context"
	"fmt"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// FixOptions configures Fix.
type FixOptions struct {
	Rules rules.Options
	AI    bool
	Model string
	// AIMinSeverity: remaining findings at or above this severity are sent
	// to Claude.
	AIMinSeverity rules.Severity
}

// FixResult describes what happened to a file.
type FixResult struct {
	File      *manifest.File
	Fixed     int
	AIApplied bool
	AINotes   string
	Remaining []rules.Finding
}

// Validate checks all objects in the files.
func Validate(files []*manifest.File, opts rules.Options) []rules.Finding {
	var objs []*manifest.Object
	for _, f := range files {
		objs = append(objs, f.Objects...)
	}
	return rules.Validate(objs, opts)
}

// Fix remediates a single file: deterministic fixes first, then (optionally)
// Claude for whatever is left. The returned File replaces the input.
func Fix(ctx context.Context, f *manifest.File, opts FixOptions) (*FixResult, error) {
	res := &FixResult{File: f}
	res.Fixed = rules.Fix(f.Objects, opts.Rules)
	res.Remaining = rules.Validate(f.Objects, opts.Rules)
	if !opts.AI {
		return res, nil
	}
	var todo []rules.Finding
	for _, fd := range res.Remaining {
		if fd.Severity >= opts.AIMinSeverity {
			todo = append(todo, fd)
		}
	}
	if len(todo) == 0 {
		return res, nil
	}
	src, err := f.Encode()
	if err != nil {
		return nil, err
	}
	fixed, notes, err := ai.New(opts.Model).Fix(ctx, string(src), todo)
	if err != nil {
		return nil, fmt.Errorf("%s: AI fix: %w", f.Source, err)
	}
	nf, err := manifest.Parse([]byte(fixed), f.Source)
	if err != nil {
		return nil, fmt.Errorf("%s: AI fix produced unparsable YAML: %w", f.Source, err)
	}
	nf.Writable = f.Writable
	// Claude may have undone a deterministic fix; re-apply them.
	res.Fixed += rules.Fix(nf.Objects, opts.Rules)
	res.File = nf
	res.AIApplied = true
	res.AINotes = notes
	res.Remaining = rules.Validate(nf.Objects, opts.Rules)
	return res, nil
}
