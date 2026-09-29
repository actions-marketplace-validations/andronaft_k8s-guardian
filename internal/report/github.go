package report

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

// WriteGitHub prints GitHub Actions workflow commands so findings show up as
// annotations on the pull request diff.
// https://docs.github.com/actions/reference/workflow-commands-for-github-actions
func WriteGitHub(w io.Writer, fs []rules.Finding) {
	for _, f := range fs {
		level := "notice"
		switch f.Severity {
		case rules.Error:
			level = "error"
		case rules.Warning:
			level = "warning"
		}
		props := []string{}
		if isFile(f.Source) {
			props = append(props, "file="+escapeProperty(f.Source))
			if f.Line > 0 {
				props = append(props, fmt.Sprintf("line=%d", f.Line))
			}
		}
		props = append(props, "title="+escapeProperty(f.RuleID+" "+f.Rule))
		fmt.Fprintf(w, "::%s %s::%s\n", level, strings.Join(props, ","), escapeData(describe(f)))
	}
	s := Summarize(fs)
	fmt.Fprintf(w, "k8s-guardian: %d error(s), %d warning(s), %d info\n", s.Errors, s.Warnings, s.Infos)
}

// isFile reports whether a finding source is a real file in the workspace
// (not stdin, a rendered Helm chart or a cluster object).
func isFile(src string) bool {
	return src != "" && !strings.HasPrefix(src, "<") && !strings.HasPrefix(src, "cluster") && !strings.Contains(src, "(helm template)") && !strings.Contains(src, "(kustomize)")
}

func describe(f rules.Finding) string {
	msg := f.Resource + ": " + f.Message
	if f.Container != "" {
		msg = fmt.Sprintf("%s (container %q): %s", f.Resource, f.Container, f.Message)
	}
	return msg
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// WriteMarkdown renders findings as a Markdown table (job summaries, PR comments).
func WriteMarkdown(w io.Writer, fs []rules.Finding, s Summary) {
	fmt.Fprintln(w, "### 🛡️ k8s-guardian")
	fmt.Fprintln(w)
	if len(fs) == 0 {
		fmt.Fprintln(w, "✅ No issues found.")
		return
	}
	fmt.Fprintf(w, "**%d error(s), %d warning(s), %d info**", s.Errors, s.Warnings, s.Infos)
	if s.Fixable > 0 {
		fmt.Fprintf(w, " · %d auto-fixable with `k8s-guardian check --fix`", s.Fixable)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| | Rule | Resource | Finding | Location |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, f := range fs {
		icon := "ℹ️"
		switch f.Severity {
		case rules.Error:
			icon = "❌"
		case rules.Warning:
			icon = "⚠️"
		}
		msg := f.Message
		if f.Container != "" {
			msg = fmt.Sprintf("container `%s`: %s", f.Container, msg)
		}
		loc := f.Source
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Source, f.Line)
		}
		fmt.Fprintf(w, "| %s | `%s` %s | `%s` | %s | %s |\n", icon, f.RuleID, f.Rule, cell(f.Resource), cell(msg), cell(loc))
	}
}

func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

// WriteGitHubExtras appends a Markdown job summary and step outputs when
// running inside GitHub Actions (GITHUB_STEP_SUMMARY / GITHUB_OUTPUT).
func WriteGitHubExtras(fs []rules.Finding) error {
	s := Summarize(fs)
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		WriteMarkdown(f, fs, s)
		fmt.Fprintln(f)
		if err := f.Close(); err != nil {
			return err
		}
	}
	if path := os.Getenv("GITHUB_OUTPUT"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		fmt.Fprintf(f, "errors=%d\nwarnings=%d\ninfos=%d\nfixable=%d\n", s.Errors, s.Warnings, s.Infos, s.Fixable)
		return f.Close()
	}
	return nil
}
