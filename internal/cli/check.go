package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/guardian"
	"github.com/andronaft/k8s-guardian/internal/kube"
	"github.com/andronaft/k8s-guardian/internal/live"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/report"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

func (a *App) check(ctx context.Context, args []string) (int, error) {
	var f flags
	fs := a.flagSet("check", "check -f <file|dir|chart|-> [--fix [--ai]] [--live [-n ns]] [flags]")
	f.input(fs)
	f.output(fs, "text, json, sarif, github, markdown")
	f.ruleFlags(fs)
	f.aiFlags(fs, "with --fix: let Claude fix findings that have no deterministic fix (probes, image tags, custom rules, ...)")
	f.cluster(fs)
	fs.BoolVar(&f.fix, "fix", false, "auto-fix findings (deterministic fixes; add --ai for Claude-powered fixes)")
	fs.BoolVar(&f.stdout, "stdout", false, "with --fix: print fixed YAML to stdout instead of rewriting files")
	fs.BoolVar(&f.live, "live", false, "also validate against the live cluster (quotas, nodes, LimitRanges, references, CRDs)")
	fs.BoolVar(&f.interact, "interactive", false, "review fixes in the interactive TUI (same as the interactive command)")
	fs.BoolVar(&f.interact, "i", false, "shorthand for --interactive")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	if err := f.addInputs(pos); err != nil {
		return ExitError, err
	}
	if f.interact {
		return a.runInteractive(&f)
	}
	failOn, err := rules.ParseSeverity(f.failOn)
	if err != nil {
		return ExitError, err
	}
	if f.ai && !f.fix {
		return ExitError, errors.New("--ai requires --fix (or use the interactive command)")
	}
	opts, err := f.options()
	if err != nil {
		return ExitError, err
	}
	files, err := loadFiles(f.files)
	if err != nil {
		fs.Usage()
		return ExitError, err
	}
	var cl cluster.Cluster
	if f.live {
		k, err := cluster.NewKubectl(f.kubeContext)
		if err != nil {
			return ExitError, err
		}
		cl = k
	}
	return a.process(ctx, files, &f, opts, failOn, f.stdout, cl)
}

func (a *App) audit(ctx context.Context, args []string) (int, error) {
	var f flags
	fs := a.flagSet("audit", "audit <resource>[/<name>] [-n ns] [--fix [--ai]] [flags]\n\nExamples:\n  audit deployment/my-app -n prod\n  audit deployments,statefulsets -A")
	f.output(fs, "text, json, sarif, github, markdown")
	f.ruleFlags(fs)
	f.aiFlags(fs, "with --fix: let Claude fix findings that have no deterministic fix")
	f.cluster(fs)
	fs.BoolVar(&f.allNamespaces, "A", false, "all namespaces")
	fs.BoolVar(&f.allNamespaces, "all-namespaces", false, "all namespaces")
	fs.BoolVar(&f.fix, "fix", false, "print fixed YAML to stdout (review, then pipe into kubectl apply -f -)")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	if len(pos) == 0 {
		fs.Usage()
		return ExitError, errors.New("no resource given")
	}
	failOn, err := rules.ParseSeverity(f.failOn)
	if err != nil {
		return ExitError, err
	}
	if f.ai && !f.fix {
		return ExitError, errors.New("--ai requires --fix")
	}
	opts, err := f.options()
	if err != nil {
		return ExitError, err
	}
	file, err := kube.Get(pos, f.namespace, f.kubeContext, f.allNamespaces)
	if err != nil {
		return ExitError, err
	}
	// Live resources can't be rewritten in place; fixed YAML goes to stdout.
	return a.process(ctx, []*manifest.File{file}, &f, opts, failOn, true, nil)
}

// liveFindings runs the cluster-aware checks and prints notes to stderr.
func (a *App) liveFindings(cl cluster.Cluster, objs []*manifest.Object, f *flags, opts rules.Options) []rules.Finding {
	if cl == nil {
		return nil
	}
	res := live.Check(cl, objs, f.namespace, opts)
	for _, n := range res.Notes {
		fmt.Fprintf(a.Stderr, "note: %s\n", n)
	}
	return res.Findings
}

func (a *App) process(ctx context.Context, files []*manifest.File, f *flags, opts rules.Options, failOn rules.Severity, toStdout bool, cl cluster.Cluster) (int, error) {
	if !f.fix {
		fs := guardian.Validate(files, opts)
		fs = append(fs, a.liveFindings(cl, objects(files), f, opts)...)
		if err := report.Write(a.Stdout, f.format, fs, report.Summarize(fs), false); err != nil {
			return ExitError, err
		}
		if err := githubExtras(f.format, fs); err != nil {
			return ExitError, err
		}
		return exitCode(fs, failOn), nil
	}

	// In fix mode fixed YAML may go to stdout, so the report goes to stderr.
	var remaining []rules.Finding
	fixed := 0
	var fixedYAML [][]byte
	for _, file := range files {
		if f.ai {
			fmt.Fprintf(a.Stderr, "🤖 asking Claude (%s) to fix %s ...\n", ai.Model(f.model), file.Source)
		}
		res, err := guardian.Fix(ctx, file, guardian.FixOptions{Rules: opts, AI: f.ai, Model: f.model, AIMinSeverity: rules.Warning})
		if err != nil {
			return ExitError, err
		}
		fixed += res.Fixed
		remaining = append(remaining, res.Remaining...)
		remaining = append(remaining, a.liveFindings(cl, res.File.Objects, f, opts)...)
		if res.AINotes != "" {
			fmt.Fprintf(a.Stderr, "\n%s — Claude's changes:\n%s\n\n", file.Source, res.AINotes)
		}
		out, err := res.File.Encode()
		if err != nil {
			return ExitError, err
		}
		changed := res.Fixed > 0 || res.AIApplied
		switch {
		case toStdout || !file.Writable || file.Source == "<stdin>":
			if !file.Writable && !toStdout && file.Source != "<stdin>" {
				fmt.Fprintf(a.Stderr, "note: %s cannot be rewritten in place (rendered Helm chart); printing fixed YAML\n", file.Source)
			}
			fixedYAML = append(fixedYAML, out)
		case changed:
			if err := os.WriteFile(file.Source, out, 0o644); err != nil {
				return ExitError, err
			}
			fmt.Fprintf(a.Stderr, "✏️  fixed %s\n", file.Source)
		}
	}
	for i, y := range fixedYAML {
		if i > 0 {
			fmt.Fprintln(a.Stdout, "---")
		}
		a.Stdout.Write(y)
	}
	s := report.Summarize(remaining)
	s.Fixed = fixed
	out := a.Stderr
	if f.format == "github" && len(fixedYAML) == 0 {
		out = a.Stdout // the runner reads workflow commands from stdout
	}
	if err := report.Write(out, f.format, remaining, s, true); err != nil {
		return ExitError, err
	}
	if err := githubExtras(f.format, remaining); err != nil {
		return ExitError, err
	}
	return exitCode(remaining, failOn), nil
}

// githubExtras writes the job summary and step outputs for --format github.
func githubExtras(format string, fs []rules.Finding) error {
	if format != "github" {
		return nil
	}
	return report.WriteGitHubExtras(fs)
}
