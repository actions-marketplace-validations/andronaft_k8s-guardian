package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/custom"
	"github.com/andronaft/k8s-guardian/internal/export"
)

func (a *App) export(args []string) (int, error) {
	if len(args) == 0 || (args[0] != "vap" && args[0] != "kyverno") {
		fmt.Fprintf(a.Stderr, "Usage: %s export vap|kyverno [--builtin all|none|KG001,...] [--rules path] [--action Deny|Warn|Audit] [-o file]\n", a.Name)
		return ExitError, errors.New("export target must be vap (ValidatingAdmissionPolicy) or kyverno (Kyverno ClusterPolicy)")
	}
	target := args[0]
	var f flags
	var builtin, action, out, exclude string
	fs := a.flagSet("export "+target, "export "+target+" [flags]")
	fs.Var(&f.rulePaths, "rules", "custom rule file or directory (repeatable; "+custom.DefaultDir+" and $K8S_GUARDIAN_RULES are loaded automatically)")
	fs.StringVar(&builtin, "builtin", "all", "built-in rules to export: all, none or a comma separated list ("+strings.Join(export.BuiltinIDs(), ",")+")")
	fs.StringVar(&action, "action", "", "override the action for every policy: Deny, Warn or Audit (default: error→Deny, warning→Warn, info→Audit)")
	fs.StringVar(&exclude, "exclude-namespaces", "kube-system", "comma separated namespaces the policies never apply to")
	fs.StringVar(&out, "o", "", "write to this file instead of stdout")
	if _, err := parse(fs, args[1:]); err != nil {
		return ExitError, err
	}
	switch strings.ToLower(action) {
	case "":
	case "deny", "warn", "audit":
		action = strings.ToUpper(action[:1]) + strings.ToLower(action[1:])
	default:
		return ExitError, fmt.Errorf("--action must be Deny, Warn or Audit")
	}
	var ids []string
	switch strings.ToLower(builtin) {
	case "all":
		ids = export.BuiltinIDs()
	case "none", "":
	default:
		for _, id := range strings.Split(builtin, ",") {
			ids = append(ids, strings.TrimSpace(id))
		}
	}
	paths := []string{custom.DefaultDir}
	if env := os.Getenv("K8S_GUARDIAN_RULES"); env != "" {
		paths = append(paths, filepath.SplitList(env)...)
	}
	docs, _, err := custom.LoadDocuments(append(paths, f.rulePaths...))
	if err != nil {
		return ExitError, err
	}
	var excl []string
	for _, n := range strings.Split(exclude, ",") {
		if n = strings.TrimSpace(n); n != "" {
			excl = append(excl, n)
		}
	}
	render := export.VAP
	if target == "kyverno" {
		render = export.Kyverno
	}
	res, err := render(ids, docs, export.Options{Action: action, ExcludeNamespaces: excl})
	if err != nil {
		return ExitError, err
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(a.Stderr, "skipped %s\n", s)
	}
	if out == "" {
		a.Stdout.Write(res.YAML)
	} else if err := os.WriteFile(out, res.YAML, 0o644); err != nil {
		return ExitError, err
	}
	if target == "kyverno" {
		fmt.Fprintf(a.Stderr, "exported %d Kyverno ClusterPolicy object(s)\n", res.Count)
	} else {
		fmt.Fprintf(a.Stderr, "exported %d ValidatingAdmissionPolicy object(s) (+ bindings)\n", res.Count)
	}
	return ExitOK, nil
}
