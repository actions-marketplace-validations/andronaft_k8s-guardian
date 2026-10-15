package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/baseline"
	"github.com/andronaft/k8s-guardian/internal/config"
	"github.com/andronaft/k8s-guardian/internal/guardian"
)

const workflowPath = ".github/workflows/k8s-guardian.yml"

// initRepo sets k8s-guardian up in a repository in one step: a config
// file, a baseline with today's findings (so CI only fails on new ones) and
// a GitHub Actions workflow. Existing files are never overwritten unless
// --force is given.
func (a *App) initRepo(args []string) (int, error) {
	var f flags
	fset := a.flagSet("init", "init [-f <paths>] [--no-workflow] [--force]")
	f.input(fset)
	noWorkflow := fset.Bool("no-workflow", false, "don't write "+workflowPath)
	force := fset.Bool("force", false, "overwrite existing files")
	pos, err := parse(fset, args)
	if err != nil {
		return ExitError, err
	}
	if err := f.addInputs(pos); err != nil {
		return ExitError, err
	}
	if len(f.files) == 0 {
		f.files = []string{"."}
	}
	for _, p := range f.files {
		if p == "-" {
			return ExitError, errors.New("init needs files or directories, not stdin")
		}
	}

	// An existing config, under any name check looks for, keeps that name:
	// a second file would take precedence over it. --force replaces it, so
	// its settings don't apply to the baseline either.
	cfg, existing := config.Names[0], false
	for _, n := range config.Names {
		if _, err := os.Stat(n); err == nil {
			cfg, existing = n, true
			break
		}
	}
	if existing && *force {
		f.cfg = &config.Config{}
	}

	// Record today's findings with the rules a check will use.
	files, err := f.loadInputs(nil, a.Stderr)
	if err != nil {
		return ExitError, err
	}
	opts, err := f.options()
	if err != nil {
		return ExitError, err
	}
	findings := guardian.Validate(files, opts)
	objects := 0
	for _, file := range files {
		objects += len(file.Objects)
	}
	if objects == 0 {
		return ExitError, fmt.Errorf("no Kubernetes manifests found in %s: pass the directories with -f", strings.Join(f.files, " "))
	}

	wrote := map[string]bool{}
	write := func(path string, content []byte) error {
		if _, err := os.Stat(path); err == nil && !*force {
			fmt.Fprintf(a.Stderr, "kept     %s (exists; --force overwrites it)\n", path)
			return nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
		wrote[path] = true
		fmt.Fprintf(a.Stderr, "wrote    %s\n", path)
		return nil
	}

	base := defaultBaseline
	useBaseline := len(findings) > 0
	if err := write(cfg, []byte(configTemplate(useBaseline))); err != nil {
		return ExitError, err
	}
	if existing && !*force {
		// The existing config decides: record a baseline only where it
		// names one.
		c, err := f.config()
		if err != nil {
			return ExitError, err
		}
		if c.Baseline != "" {
			base = c.Baseline
		} else if useBaseline {
			fmt.Fprintf(a.Stderr, "note     %s names no baseline, so CI fails on today's findings: add \"baseline: %s\" to it and run init again\n", cfg, defaultBaseline)
		}
		useBaseline = useBaseline && c.Baseline != ""
	}
	if useBaseline {
		if _, err := os.Stat(base); err == nil && !*force {
			fmt.Fprintf(a.Stderr, "kept     %s (exists; --force overwrites it)\n", base)
		} else {
			if err := os.MkdirAll(filepath.Dir(base), 0o750); err != nil {
				return ExitError, err
			}
			if err := baseline.Write(base, findings); err != nil {
				return ExitError, err
			}
			wrote[base] = true
			fmt.Fprintf(a.Stderr, "wrote    %s (%d existing finding(s): they won't fail CI, new ones will)\n", base, len(findings))
		}
	}
	if !*noWorkflow {
		if err := write(workflowPath, []byte(workflowTemplate(a.Version, f.files))); err != nil {
			return ExitError, err
		}
	}

	fmt.Fprintf(a.Stderr, "\nChecked %d object(s) in %s: %d finding(s).\n", objects, strings.Join(f.files, " "), len(findings))
	if len(wrote) > 0 {
		var paths []string
		for _, p := range []string{cfg, base, workflowPath} {
			if wrote[p] {
				paths = append(paths, p)
			}
		}
		fmt.Fprintf(a.Stderr, "Next: review the files, then git add %s\n", strings.Join(paths, " "))
	}
	if useBaseline {
		fmt.Fprintf(a.Stderr, "Fix the recorded findings over time (%s check -f %s --fix) and shrink the baseline with --update-baseline.\n", a.Name, strings.Join(f.files, " "))
	}
	return ExitOK, nil
}

func configTemplate(withBaseline bool) string {
	b := "# baseline: " + defaultBaseline + "\n"
	if withBaseline {
		b = "# Findings that existed when k8s-guardian was set up: they don't fail\n" +
			"# the check, new ones do. Shrink it with --update-baseline after fixing.\n" +
			"baseline: " + defaultBaseline + "\n"
	}
	return `# k8s-guardian configuration:
# https://github.com/andronaft/k8s-guardian/blob/main/docs/configuration.md
failOn: error                 # or warning, info
` + b + `# skip: [KG015]               # rule IDs or names to skip everywhere
# severity: {KG014: warning}  # change a rule's severity
# exclude: [vendor]           # paths or patterns skipped when walking directories
# kubeVersion: "1.33"         # render Helm charts for your cluster's version
`
}

var releaseVersion = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

func workflowTemplate(version string, paths []string) string {
	ref := "main # pin to a release tag"
	if releaseVersion.MatchString(version) {
		ref = "v" + strings.TrimPrefix(version, "v")
	}
	return `# Checks Kubernetes manifests on every pull request with k8s-guardian.
# Findings appear as annotations on the diff. .k8s-guardian.yaml (and its
# baseline) applies automatically.
name: k8s-guardian
on:
  pull_request:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  guardrails:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: andronaft/k8s-guardian@` + ref + `
        with:
          path: ` + strings.Join(paths, " ") + `
`
}
