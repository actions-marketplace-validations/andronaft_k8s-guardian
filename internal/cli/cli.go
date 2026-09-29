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
	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/diff"
	"github.com/andronaft/k8s-guardian/internal/live"
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

func init() {
	report.RegisterRules(live.Rules...)
	report.RegisterRules(diff.Rules...)
}

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
	case "diff":
		code, err = a.diff(args[1:])
	case "cost":
		code, err = a.cost(ctx, args[1:])
	case "interactive", "tui", "review":
		code, err = a.interactive(args[1:])
	case "rule":
		code, err = a.rule(ctx, args[1:])
	case "export":
		code, err = a.export(args[1:])
	case "rules":
		err = a.listRules(args[1:])
	case "mcp", "serve":
		err = (&mcp.Server{Version: a.Version}).Serve(ctx, os.Stdin, a.Stdout)
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
		if a == "-f" || a == "--filename" || a == "--file" || a == "-k" || a == "--kustomize" ||
			strings.HasPrefix(a, "-f=") || strings.HasPrefix(a, "--filename=") || strings.HasPrefix(a, "-k=") {
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

// flags holds every option; each command registers the subset it supports.
type flags struct {
	files, rulePaths, kustomize     multi
	format, failOn, skip, model     string
	fix, ai, stdout, live, interact bool
	namespace, kubeContext          string
	allNamespaces                   bool
}

func (a *App) flagSet(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(a.Stderr, "Usage: %s %s\n\nFlags:\n", a.Name, usage)
		fs.PrintDefaults()
	}
	return fs
}

func (f *flags) input(fs *flag.FlagSet) {
	fs.Var(&f.files, "f", "manifest file, directory, Helm chart or - for stdin (repeatable)")
	fs.Var(&f.files, "filename", "alias for -f")
	fs.Var(&f.files, "file", "alias for -f")
	fs.Var(&f.kustomize, "k", "Kustomize directory to render and check (repeatable; -f on a kustomization dir works too)")
	fs.Var(&f.kustomize, "kustomize", "alias for -k")
}

func (f *flags) output(fs *flag.FlagSet, formats string) {
	fs.StringVar(&f.format, "format", "text", "output format: "+formats)
	fs.StringVar(&f.format, "o", "text", "shorthand for --format")
	fs.StringVar(&f.failOn, "fail-on", "error", "exit with code 1 when findings at or above this severity remain: error, warning, info")
}

func (f *flags) ruleFlags(fs *flag.FlagSet) {
	fs.StringVar(&f.skip, "skip", "", "comma separated rule IDs or names to skip (e.g. KG006,liveness-probe)")
	fs.Var(&f.rulePaths, "rules", "custom rule file or directory (repeatable; "+custom.DefaultDir+" and $K8S_GUARDIAN_RULES are loaded automatically)")
}

func (f *flags) aiFlags(fs *flag.FlagSet, help string) {
	fs.BoolVar(&f.ai, "ai", false, help)
	fs.StringVar(&f.model, "model", "", "Claude model (default $K8S_GUARDIAN_MODEL or "+ai.DefaultModel+")")
}

func (f *flags) cluster(fs *flag.FlagSet) {
	fs.StringVar(&f.namespace, "n", "", "namespace (default: the manifest's namespace, then the kube context's)")
	fs.StringVar(&f.namespace, "namespace", "", "alias for -n")
	fs.StringVar(&f.kubeContext, "context", "", "kubeconfig context")
}

// options builds rule options including custom rules.
func (f *flags) options() (rules.Options, error) {
	opts := rules.NewOptions(f.skip)
	paths := []string{custom.DefaultDir}
	if env := os.Getenv("K8S_GUARDIAN_RULES"); env != "" {
		paths = append(paths, filepath.SplitList(env)...)
	}
	paths = append(paths, f.rulePaths...)
	cr, err := custom.Load(paths)
	if err != nil {
		return opts, fmt.Errorf("custom rules: %w", err)
	}
	opts.Custom = cr
	return opts, nil
}

// addInputs merges positional paths and -k directories into f.files.
func (f *flags) addInputs(pos []string) error {
	f.files = append(f.files, pos...)
	for _, k := range f.kustomize {
		if !manifest.IsKustomization(k) {
			return fmt.Errorf("-k %s: no kustomization.yaml in that directory", k)
		}
		f.files = append(f.files, k)
	}
	return nil
}

func loadFiles(paths []string) ([]*manifest.File, error) {
	if len(paths) == 0 {
		return nil, errors.New("no input: pass -f <file|dir|-> or -k <kustomize dir>")
	}
	var loaded []*manifest.File
	for _, p := range paths {
		fl, err := manifest.Load(p)
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, fl...)
	}
	return loaded, nil
}

func objects(files []*manifest.File) []*manifest.Object {
	var objs []*manifest.Object
	for _, f := range files {
		objs = append(objs, f.Objects...)
	}
	return objs
}

func exitCode(fs []rules.Finding, failOn rules.Severity) int {
	for _, f := range fs {
		if f.Severity >= failOn {
			return ExitFindings
		}
	}
	return ExitOK
}

func (a *App) listRules(args []string) error {
	var f flags
	fs := a.flagSet("rules", "rules [--rules <path>]")
	f.ruleFlags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	opts, err := f.options()
	if err != nil {
		return err
	}
	section := func(title string, rs []*rules.Rule, fixCol bool) {
		if len(rs) == 0 {
			return
		}
		fmt.Fprintf(a.Stdout, "\n%s\n", title)
		for _, r := range rs {
			fix := ""
			if fixCol {
				fix = "ai"
				if r.Fix != nil {
					fix = "auto"
				}
			}
			fmt.Fprintf(a.Stdout, "  %-7s %-32s %-8s %-5s %s\n", r.ID, r.Name, r.Severity, fix, r.Description)
		}
	}
	fmt.Fprintf(a.Stdout, "  %-7s %-32s %-8s %-5s %s\n", "ID", "NAME", "SEVERITY", "FIX", "DESCRIPTION")
	section("Built-in rules (check):", rules.All, true)
	section("Custom rules ("+custom.DefaultDir+", --rules):", opts.Custom, true)
	section("Live cluster rules (check --live):", live.Rules, false)
	section("Change rules (diff):", diff.Rules, false)
	fmt.Fprintf(a.Stdout, "\nSkip rules per resource with the annotation %s: \"KG006,liveness-probe\"\n", rules.IgnoreAnnotation)
	return nil
}

func (a *App) usage() {
	fmt.Fprintf(a.Stdout, `%[1]s %[2]s — AI-powered Kubernetes guardrails

Validate:
  %[1]s check -f <file|dir|chart|->         Validate manifests (files, directories, Helm charts, stdin)
  %[1]s check -k overlays/prod              Validate a Kustomize overlay
  %[1]s check -f app.yaml --live -n prod    ...and against the real cluster: quotas, node capacity,
                                            LimitRanges, missing ConfigMaps/Secrets, CRDs, StorageClasses
  %[1]s audit deployment/my-app -n prod     Validate live cluster resources

Fix:
  %[1]s check -f app.yaml --fix             Auto-fix in place (resources, securityContext, seccomp, ...)
  %[1]s check -f app.yaml --fix --ai        Let Claude fix the rest (probes, image tags, custom rules)
  %[1]s interactive -f app.yaml [--ai]      Review every fix in a TUI: [y] accept [n] skip [e] edit

Before you deploy:
  %[1]s diff -f app.yaml -n prod            Compare with the cluster and flag breaking changes
                                            (immutable selectors, removed APIs, downtime, port changes)
  %[1]s cost -f app.yaml [--usage] [--ai]   Estimate $/month; compare with real usage (metrics-server);
                                            Claude right-sizes requests and shows savings

Policies & integrations:
  %[1]s rule create "<policy in plain English>"   Generate a custom rule with Claude
  %[1]s export vap > policies.yaml          Enforce built-in + custom rules in the cluster itself
                                            (ValidatingAdmissionPolicy / CEL, no Kyverno or OPA needed)
  %[1]s rules                               List built-in, custom, live and diff rules
  %[1]s mcp                                 Run as an MCP server (stdio) for Claude Code / Cursor

Shorthands (kubectl plugin style):
  %[1]s -f app.yaml                         same as "check -f app.yaml"
  %[1]s deployment/my-app -n prod           same as "audit deployment/my-app -n prod"

Common flags: --format text|json|sarif|github|markdown  --fail-on error|warning|info  --skip KG006,KG015  --rules <path>
Exit codes: 0 ok, 1 findings at/above --fail-on, 2 usage or runtime error.
Environment: ANTHROPIC_API_KEY (for --ai), K8S_GUARDIAN_MODEL (default %[3]s),
             K8S_GUARDIAN_RULES, K8S_GUARDIAN_CPU_HOUR / K8S_GUARDIAN_GIB_HOUR (cost).
Run "%[1]s <command> -h" for command flags.
`, a.Name, a.Version, ai.DefaultModel)
}
