// Package cli implements the k8s-guardian / kubectl-guard command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/guardian"
	"github.com/andronaft/k8s-guardian/internal/kube"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/mcp"
	"github.com/andronaft/k8s-guardian/internal/report"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitFindings = 1
	ExitError    = 2
)

// App holds process-wide settings.
type App struct {
	Name    string // "k8s-guardian" or "kubectl guard"
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
}

// ProgramName derives the display name from argv[0]; a binary named
// kubectl-guard is invoked by kubectl as `kubectl guard`.
func ProgramName(argv0 string) string {
	base := strings.TrimSuffix(filepath.Base(argv0), ".exe")
	if strings.HasPrefix(base, "kubectl-") {
		return "kubectl " + strings.ReplaceAll(strings.TrimPrefix(base, "kubectl-"), "_", "-")
	}
	return "k8s-guardian"
}

// Run executes the CLI and returns the exit code.
func (a *App) Run(args []string) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	code := ExitOK
	switch cmd {
	case "check", "validate", "lint":
		code, err = a.check(ctx, args[1:])
	case "audit":
		code, err = a.audit(ctx, args[1:])
	case "mcp", "serve":
		err = (&mcp.Server{Version: a.Version}).Serve(ctx, os.Stdin, a.Stdout)
	case "rules":
		a.listRules()
	case "version", "--version", "-v":
		fmt.Fprintf(a.Stdout, "%s %s\n", a.Name, a.Version)
	case "", "help", "-h", "--help":
		a.usage()
	default:
		// Shorthand: `kubectl guard -f x.yaml` or `kubectl guard deployment/app`.
		if hasFileFlag(args) {
			code, err = a.check(ctx, args)
		} else if strings.HasPrefix(cmd, "-") && !strings.HasPrefix(cmd, "-n") && cmd != "--namespace" && cmd != "-A" {
			err = fmt.Errorf("unknown flag %q; run `%s help`", cmd, a.Name)
		} else {
			code, err = a.audit(ctx, args)
		}
	}
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(a.Stderr, "%s: %v\n", a.Name, err)
		}
		return ExitError
	}
	return code
}

func hasFileFlag(args []string) bool {
	for _, a := range args {
		if a == "-f" || a == "--filename" || a == "--file" || strings.HasPrefix(a, "-f=") || strings.HasPrefix(a, "--filename=") {
			return true
		}
	}
	return false
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// parse allows flags and positional arguments to be interleaved
// (`audit deployment/x -n prod`).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

type common struct {
	format, failOn, skip, model string
	fix, ai                     bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.format, "format", "text", "output format: text, json, sarif")
	fs.StringVar(&c.format, "o", "text", "shorthand for --format")
	fs.StringVar(&c.failOn, "fail-on", "error", "exit with code 1 when findings at or above this severity remain: error, warning, info")
	fs.StringVar(&c.skip, "skip", "", "comma separated rule IDs or names to skip (e.g. KG006,liveness-probe)")
	fs.BoolVar(&c.fix, "fix", false, "auto-fix findings (deterministic fixes; add --ai for Claude-powered fixes)")
	fs.BoolVar(&c.ai, "ai", false, "with --fix: let Claude fix findings that have no deterministic fix (probes, image tags, ...)")
	fs.StringVar(&c.model, "model", "", "Claude model for --ai (default $K8S_GUARDIAN_MODEL or "+ai.DefaultModel+")")
}

func (a *App) check(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var files multi
	var c common
	var toStdout bool
	fs.Var(&files, "f", "manifest file, directory, Helm chart or - for stdin (repeatable)")
	fs.Var(&files, "filename", "alias for -f")
	fs.Var(&files, "file", "alias for -f")
	fs.BoolVar(&toStdout, "stdout", false, "with --fix: print fixed YAML to stdout instead of rewriting files")
	c.register(fs)
	fs.Usage = func() {
		fmt.Fprintf(a.Stderr, "Usage: %s check -f <file|dir|chart|-> [--fix [--ai]] [flags]\n\nFlags:\n", a.Name)
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	files = append(files, pos...)
	if len(files) == 0 {
		fs.Usage()
		return ExitError, errors.New("no input: pass -f <file|dir|->")
	}
	failOn, err := rules.ParseSeverity(c.failOn)
	if err != nil {
		return ExitError, err
	}
	if c.ai && !c.fix {
		return ExitError, errors.New("--ai requires --fix")
	}
	var loaded []*manifest.File
	for _, p := range files {
		fl, err := manifest.Load(p)
		if err != nil {
			return ExitError, err
		}
		loaded = append(loaded, fl...)
	}
	return a.process(ctx, loaded, c, failOn, toStdout)
}

func (a *App) audit(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	var ns, kctx string
	var all bool
	var c common
	fs.StringVar(&ns, "n", "", "namespace")
	fs.StringVar(&ns, "namespace", "", "namespace")
	fs.StringVar(&kctx, "context", "", "kubeconfig context")
	fs.BoolVar(&all, "A", false, "all namespaces")
	fs.BoolVar(&all, "all-namespaces", false, "all namespaces")
	c.register(fs)
	fs.Usage = func() {
		fmt.Fprintf(a.Stderr, "Usage: %s audit <resource>[/<name>] [-n ns] [--fix [--ai]] [flags]\n\nExamples:\n  %[1]s audit deployment/my-app -n prod\n  %[1]s audit deployments,statefulsets -A\n\nFlags:\n", a.Name)
		fs.PrintDefaults()
	}
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	if len(pos) == 0 {
		fs.Usage()
		return ExitError, errors.New("no resource given")
	}
	failOn, err := rules.ParseSeverity(c.failOn)
	if err != nil {
		return ExitError, err
	}
	if c.ai && !c.fix {
		return ExitError, errors.New("--ai requires --fix")
	}
	f, err := kube.Get(pos, ns, kctx, all)
	if err != nil {
		return ExitError, err
	}
	// Live resources can't be rewritten in place; fixed YAML goes to stdout
	// so it can be reviewed and piped into `kubectl apply -f -`.
	return a.process(ctx, []*manifest.File{f}, c, failOn, true)
}

func (a *App) process(ctx context.Context, files []*manifest.File, c common, failOn rules.Severity, toStdout bool) (int, error) {
	opts := rules.NewOptions(c.skip)
	if !c.fix {
		fs := guardian.Validate(files, opts)
		if err := report.Write(a.Stdout, c.format, fs, report.Summarize(fs), false); err != nil {
			return ExitError, err
		}
		return exitCode(fs, failOn), nil
	}

	// In fix mode fixed YAML may go to stdout, so the report goes to stderr.
	var remaining []rules.Finding
	fixed := 0
	var fixedYAML [][]byte
	for _, f := range files {
		if c.ai {
			fmt.Fprintf(a.Stderr, "🤖 asking Claude (%s) to fix %s ...\n", ai.Model(c.model), f.Source)
		}
		res, err := guardian.Fix(ctx, f, guardian.FixOptions{Rules: opts, AI: c.ai, Model: c.model, AIMinSeverity: rules.Warning})
		if err != nil {
			return ExitError, err
		}
		fixed += res.Fixed
		remaining = append(remaining, res.Remaining...)
		if res.AINotes != "" {
			fmt.Fprintf(a.Stderr, "\n%s — Claude's changes:\n%s\n\n", f.Source, res.AINotes)
		}
		out, err := res.File.Encode()
		if err != nil {
			return ExitError, err
		}
		changed := res.Fixed > 0 || res.AIApplied
		switch {
		case toStdout || !f.Writable || f.Source == "<stdin>":
			if !f.Writable && !toStdout && f.Source != "<stdin>" {
				fmt.Fprintf(a.Stderr, "note: %s cannot be rewritten in place (rendered Helm chart); printing fixed YAML\n", f.Source)
			}
			fixedYAML = append(fixedYAML, out)
		case changed:
			if err := os.WriteFile(f.Source, out, 0o644); err != nil {
				return ExitError, err
			}
			fmt.Fprintf(a.Stderr, "✏️  fixed %s\n", f.Source)
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
	if err := report.Write(a.Stderr, c.format, remaining, s, true); err != nil {
		return ExitError, err
	}
	return exitCode(remaining, failOn), nil
}

func exitCode(fs []rules.Finding, failOn rules.Severity) int {
	for _, f := range fs {
		if f.Severity >= failOn {
			return ExitFindings
		}
	}
	return ExitOK
}

func (a *App) listRules() {
	fmt.Fprintf(a.Stdout, "%-6s %-32s %-8s %-6s %s\n", "ID", "NAME", "SEVERITY", "FIX", "DESCRIPTION")
	for _, r := range rules.All {
		fix := "ai"
		if r.Fix != nil {
			fix = "auto"
		}
		fmt.Fprintf(a.Stdout, "%-6s %-32s %-8s %-6s %s\n", r.ID, r.Name, r.Severity, fix, r.Description)
	}
	fmt.Fprintf(a.Stdout, "\nSkip rules per resource with the annotation %s: \"KG006,liveness-probe\"\n", rules.IgnoreAnnotation)
}

func (a *App) usage() {
	fmt.Fprintf(a.Stdout, `%[1]s %[2]s — AI-powered Kubernetes guardrails

Usage:
  %[1]s check -f <file|dir|chart|->   Validate manifests (files, directories, Helm charts, stdin)
  %[1]s check -f app.yaml --fix       Auto-fix in place (resources, securityContext, seccomp, ...)
  %[1]s check -f app.yaml --fix --ai  Let Claude fix the rest (probes, image tags, ...)
  %[1]s audit deployment/my-app -n prod   Validate a live cluster resource via kubectl
  %[1]s mcp                           Run as an MCP server (stdio) for Claude Code / Cursor
  %[1]s rules                         List all rules
  %[1]s version

Shorthands:
  %[1]s -f app.yaml                   same as "check -f app.yaml"
  %[1]s deployment/my-app -n prod     same as "audit deployment/my-app -n prod"

Common flags: --format text|json|sarif  --fail-on error|warning|info  --skip KG006,KG015
Exit codes: 0 ok, 1 findings at/above --fail-on, 2 usage or runtime error.
Environment: ANTHROPIC_API_KEY (for --ai), K8S_GUARDIAN_MODEL (default %[3]s).
`, a.Name, a.Version, ai.DefaultModel)
}
