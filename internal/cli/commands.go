package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/cluster"
	"github.com/andronaft/k8s-guardian/internal/cost"
	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/diff"
	"github.com/andronaft/k8s-guardian/internal/quantity"
	"github.com/andronaft/k8s-guardian/internal/report"
	"github.com/andronaft/k8s-guardian/internal/rules"
	"github.com/andronaft/k8s-guardian/internal/tui"
)

// ---- diff ------------------------------------------------------------------

func (a *App) diff(args []string) (int, error) {
	var f flags
	fs := a.flagSet("diff", "diff -f <file|dir|chart> [-n ns] [--context ctx] [flags]")
	f.input(fs)
	f.output(fs, "text, json, sarif")
	f.ruleFlags(fs)
	f.cluster(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	f.files = append(f.files, pos...)
	failOn, err := rules.ParseSeverity(f.failOn)
	if err != nil {
		return ExitError, err
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
	cl, err := cluster.NewKubectl(f.kubeContext)
	if err != nil {
		return ExitError, err
	}
	res := diff.Run(cl, objects(files), f.namespace, opts)
	findings := res.Findings()
	switch f.format {
	case "json":
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return ExitError, err
		}
	case "sarif":
		if err := report.Write(a.Stdout, "sarif", findings, report.Summary{}, false); err != nil {
			return ExitError, err
		}
	case "text", "":
		a.writeDiff(res)
	default:
		return ExitError, fmt.Errorf("unknown output format %q", f.format)
	}
	return exitCode(findings, failOn), nil
}

func (a *App) writeDiff(res *diff.Result) {
	version := res.ClusterVersion
	if version == "" {
		version = "unknown version"
	}
	fmt.Fprintf(a.Stdout, "Comparing local manifests with the cluster (%s)\n\n", version)
	counts := map[string]int{}
	var all []rules.Finding
	for _, o := range res.Objects {
		counts[o.Status]++
		sym, info := "~", fmt.Sprintf("%d change(s)", len(o.Changes))
		switch o.Status {
		case diff.New:
			sym, info = "+", "new (will be created)"
		case diff.Unchanged:
			sym, info = "=", "unchanged"
		case diff.Unknown:
			sym, info = "?", "could not compare: "+o.Error
		}
		ns := ""
		if o.Namespace != "" {
			ns = " [" + o.Namespace + "]"
		}
		fmt.Fprintf(a.Stdout, "%s %s%s  %s\n", sym, o.Resource, ns, info)
		for i, c := range o.Changes {
			if i == 15 {
				fmt.Fprintf(a.Stdout, "    … and %d more\n", len(o.Changes)-15)
				break
			}
			fmt.Fprintf(a.Stdout, "    %s: %s → %s\n", c.Path, c.Old, c.New)
		}
		for _, fd := range o.Findings {
			fmt.Fprintf(a.Stdout, "  %s %s  %s\n", sevLabel(fd.Severity), fd.RuleID, fd.Message)
		}
		all = append(all, o.Findings...)
	}
	s := report.Summarize(all)
	fmt.Fprintf(a.Stdout, "\n%d new, %d changed, %d unchanged — %d breaking/error, %d warning(s), %d info\n",
		counts[diff.New], counts[diff.Changed], counts[diff.Unchanged], s.Errors, s.Warnings, s.Infos)
}

func sevLabel(s rules.Severity) string {
	switch s {
	case rules.Error:
		return "✖ error  "
	case rules.Warning:
		return "⚠ warning"
	}
	return "ℹ info   "
}

// ---- cost ------------------------------------------------------------------

func (a *App) cost(ctx context.Context, args []string) (int, error) {
	var f flags
	var apply bool
	p := cost.DefaultPricing()
	fs := a.flagSet("cost", "cost -f <file|dir|chart|-> [--ai [--apply]] [--cpu-hour 0.0316] [--gib-hour 0.0042] [flags]")
	f.input(fs)
	fs.StringVar(&f.format, "format", "text", "output format: text, json")
	fs.StringVar(&f.format, "o", "text", "shorthand for --format")
	f.aiFlags(fs, "ask Claude to right-size requests for each container and estimate savings")
	fs.BoolVar(&apply, "apply", false, "with --ai: write Claude's recommended requests/limits into the manifests")
	fs.Float64Var(&p.CPUHour, "cpu-hour", p.CPUHour, "price per vCPU-hour in $ (env K8S_GUARDIAN_CPU_HOUR)")
	fs.Float64Var(&p.GiBHour, "gib-hour", p.GiBHour, "price per GiB-hour of memory in $ (env K8S_GUARDIAN_GIB_HOUR)")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	f.files = append(f.files, pos...)
	if apply && !f.ai {
		return ExitError, errors.New("--apply requires --ai")
	}
	files, err := loadFiles(f.files)
	if err != nil {
		fs.Usage()
		return ExitError, err
	}
	ws := cost.Estimate(objects(files), p)
	var advice []cost.Advice
	if f.ai && len(ws) > 0 {
		client := ai.New(f.model)
		fmt.Fprintf(a.Stderr, "🤖 asking Claude (%s) to right-size %d workload(s) ...\n", client.ModelName(), len(ws))
		if advice, err = cost.RightSize(ctx, client, ws, p); err != nil {
			return ExitError, err
		}
	}
	if f.format == "json" {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(struct {
			Pricing   cost.Pricing     `json:"pricing"`
			Workloads []*cost.Workload `json:"workloads"`
			Advice    []cost.Advice    `json:"advice,omitempty"`
		}{p, ws, advice})
	} else {
		a.writeCost(ws, advice, p)
	}
	if err != nil {
		return ExitError, err
	}
	if apply && len(advice) > 0 {
		n := cost.Apply(ws, advice)
		for _, file := range files {
			if !file.Writable || file.Source == "<stdin>" {
				fmt.Fprintf(a.Stderr, "note: %s cannot be rewritten in place\n", file.Source)
				continue
			}
			out, err := file.Encode()
			if err != nil {
				return ExitError, err
			}
			if err := os.WriteFile(file.Source, out, 0o644); err != nil {
				return ExitError, err
			}
		}
		fmt.Fprintf(a.Stderr, "✏️  applied %d recommendation(s)\n", n)
	}
	return ExitOK, nil
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

func (a *App) writeCost(ws []*cost.Workload, advice []cost.Advice, p cost.Pricing) {
	if len(ws) == 0 {
		fmt.Fprintln(a.Stdout, "no workloads found")
		return
	}
	fmt.Fprintf(a.Stdout, "%-40s %-9s %-9s %-9s %s\n", "WORKLOAD", "REPLICAS", "CPU/POD", "MEM/POD", "$/MONTH")
	var totalMin, totalMax float64
	for _, w := range ws {
		name := w.Resource
		if w.Namespace != "" {
			name += " (" + w.Namespace + ")"
		}
		reps := fmt.Sprint(w.MinReplicas)
		price := money(w.MonthlyMin)
		if w.MaxReplicas != w.MinReplicas {
			reps = fmt.Sprintf("%d-%d", w.MinReplicas, w.MaxReplicas)
			price = money(w.MonthlyMin) + "–" + money(w.MonthlyMax)
		}
		fmt.Fprintf(a.Stdout, "%-40s %-9s %-9s %-9s %s\n", name, reps, quantity.FormatCPU(w.PodCPU), quantity.FormatBytes(w.PodMemory), price)
		for _, n := range w.Notes {
			fmt.Fprintf(a.Stdout, "    ↳ %s\n", n)
		}
		totalMin += w.MonthlyMin
		totalMax += w.MonthlyMax
	}
	total := money(totalMin)
	if totalMax != totalMin {
		total += "–" + money(totalMax)
	}
	fmt.Fprintf(a.Stdout, "%-70s %s\n", "TOTAL", total)
	fmt.Fprintf(a.Stdout, "\nPrices: $%.4f per vCPU-hour, $%.4f per GiB-hour, %d h/month, based on requests (override with --cpu-hour/--gib-hour).\n", p.CPUHour, p.GiBHour, cost.HoursPerMonth)

	if len(advice) == 0 {
		return
	}
	fmt.Fprintln(a.Stdout, "\n💡 Claude's right-sizing advice")
	var saved float64
	for _, ad := range advice {
		cur := fmt.Sprintf("cpu %s, memory %s", quantity.FormatCPU(ad.Current.MilliCPU), quantity.FormatBytes(ad.Current.Memory))
		if ad.Current.Missing {
			cur = "no requests"
		}
		verdict := "keep as is"
		switch {
		case ad.Savings > 0.5:
			verdict = "saves ~" + money(ad.Savings) + "/mo"
		case ad.Savings < -0.5:
			verdict = "costs ~" + money(-ad.Savings) + "/mo more (currently under-provisioned)"
		}
		fmt.Fprintf(a.Stdout, "\n  %s container %q (%s)\n", ad.Resource, ad.Container, ad.WorkloadType)
		fmt.Fprintf(a.Stdout, "    %s  →  cpu %s, memory %s (limit %s): %s\n", cur, ad.CPURequest, ad.MemoryRequest, ad.MemoryLimit, verdict)
		fmt.Fprintf(a.Stdout, "    %s\n", ad.Reason)
		saved += ad.Savings
	}
	if totalMin > 0 {
		fmt.Fprintf(a.Stdout, "\nPotential savings: ~%s/month (%.0f%% of the minimum estimate). Apply with --apply.\n", money(saved), saved/totalMin*100)
	}
}

// ---- interactive -------------------------------------------------------------

func (a *App) interactive(args []string) (int, error) {
	var f flags
	fs := a.flagSet("interactive", "interactive -f <file|dir|chart> [--ai] [flags]")
	f.input(fs)
	f.ruleFlags(fs)
	f.aiFlags(fs, "let Claude propose fixes for findings without a deterministic fix")
	fs.BoolVar(&f.stdout, "stdout", false, "print the result to stdout instead of rewriting files")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	f.files = append(f.files, pos...)
	return a.runInteractive(&f)
}

func (a *App) runInteractive(f *flags) (int, error) {
	for _, p := range f.files {
		if p == "-" {
			return ExitError, errors.New("interactive mode needs a terminal: pass files, not stdin")
		}
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return ExitError, errors.New("interactive mode needs a terminal (use check --fix in scripts and CI)")
	}
	opts, err := f.options()
	if err != nil {
		return ExitError, err
	}
	files, err := loadFiles(f.files)
	if err != nil {
		return ExitError, err
	}
	var client *ai.Client
	if f.ai {
		client = ai.New(f.model)
	}
	ps := tui.Build(files, opts, f.ai)
	if len(ps) == 0 {
		fmt.Fprintln(a.Stdout, "✔ no issues found")
		return ExitOK, nil
	}
	m, err := tui.Run(ps, client)
	if err != nil {
		return ExitError, err
	}
	if !m.Save {
		fmt.Fprintln(a.Stderr, "aborted: no files changed")
		return ExitOK, nil
	}
	changed := m.Changed()
	for _, file := range changed {
		out, err := file.Encode()
		if err != nil {
			return ExitError, err
		}
		if f.stdout || !file.Writable {
			a.Stdout.Write(out)
			continue
		}
		if err := os.WriteFile(file.Source, out, 0o644); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Stderr, "✏️  saved %s\n", file.Source)
	}
	counts := map[tui.Status]int{}
	for _, p := range ps {
		counts[p.Status]++
	}
	fmt.Fprintf(a.Stderr, "accepted %d, edited %d, skipped %d, open %d\n", counts[tui.Accepted], counts[tui.Edited], counts[tui.Skipped], counts[tui.Pending]+counts[tui.Failed])
	return ExitOK, nil
}

// ---- rule ------------------------------------------------------------------

func (a *App) rule(ctx context.Context, args []string) (int, error) {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "create", "new", "generate":
		return a.ruleCreate(ctx, args)
	case "list", "ls":
		return ExitOK, a.listRules(args)
	case "validate", "lint":
		var f flags
		fs := a.flagSet("rule validate", "rule validate <file|dir>...")
		pos, err := parse(fs, args)
		if err != nil {
			return ExitError, err
		}
		f.rulePaths = append(f.rulePaths, pos...)
		if len(f.rulePaths) == 0 {
			f.rulePaths = multi{custom.DefaultDir}
		}
		rs, err := custom.Load(f.rulePaths)
		if err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Stdout, "✔ %d custom rule(s) are valid\n", len(rs))
		return ExitOK, nil
	}
	fmt.Fprintf(a.Stderr, `Usage:
  %[1]s rule create "<policy in plain language>" [--out dir] [--dry-run] [-f manifests to test]
  %[1]s rule validate [file|dir]...
  %[1]s rule list

Example:
  %[1]s rule create "Forbid :latest image tags and require a contact email annotation"
`, a.Name)
	return ExitError, errors.New("unknown rule subcommand")
}

func (a *App) ruleCreate(ctx context.Context, args []string) (int, error) {
	var f flags
	var out string
	var dryRun, force bool
	fs := a.flagSet("rule create", `rule create "<policy>" [--out dir] [--dry-run] [-f manifests]`)
	f.input(fs)
	f.ruleFlags(fs)
	fs.StringVar(&f.model, "model", "", "Claude model (default $K8S_GUARDIAN_MODEL or "+ai.DefaultModel+")")
	fs.StringVar(&out, "out", custom.DefaultDir, "directory to write the rule files to")
	fs.BoolVar(&dryRun, "dry-run", false, "print the rules without writing them")
	fs.BoolVar(&force, "force", false, "overwrite existing rule files")
	pos, err := parse(fs, args)
	if err != nil {
		return ExitError, err
	}
	policy := strings.TrimSpace(strings.Join(pos, " "))
	if policy == "" {
		fs.Usage()
		return ExitError, errors.New("describe the policy, e.g. rule create \"require a team label on every Deployment\"")
	}
	opts, err := f.options()
	if err != nil {
		return ExitError, err
	}
	var taken []string
	for _, r := range opts.Custom {
		taken = append(taken, r.ID)
	}
	client := ai.New(f.model)
	fmt.Fprintf(a.Stderr, "🤖 asking Claude (%s) to write the rule(s) ...\n", client.ModelName())
	docs, err := client.GenerateRules(ctx, policy, taken)
	if err != nil {
		return ExitError, err
	}
	var compiled []*rules.Rule
	for _, d := range docs {
		y, err := custom.Marshal(d)
		if err != nil {
			return ExitError, err
		}
		r, _ := custom.Compile(d)
		compiled = append(compiled, r)
		fmt.Fprintf(a.Stdout, "---\n%s", y)
		if dryRun {
			continue
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return ExitError, err
		}
		path := filepath.Join(out, d.Metadata.Name+".yaml")
		if _, err := os.Stat(path); err == nil && !force {
			return ExitError, fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
		if err := os.WriteFile(path, y, 0o644); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Stderr, "✏️  wrote %s (%s)\n", path, d.Spec.ID)
	}
	if len(f.files) > 0 {
		files, err := loadFiles(f.files)
		if err != nil {
			return ExitError, err
		}
		// Run only the new rules against the sample manifests.
		test := rules.Options{Skip: map[string]bool{}, Custom: compiled}
		for _, r := range rules.All {
			test.Skip[strings.ToLower(r.ID)] = true
		}
		fs := rules.Validate(objects(files), test)
		fmt.Fprintln(a.Stderr, "\nTesting the new rule(s):")
		if err := report.Write(a.Stderr, "text", fs, report.Summarize(fs), true); err != nil {
			return ExitError, err
		}
	}
	return ExitOK, nil
}
