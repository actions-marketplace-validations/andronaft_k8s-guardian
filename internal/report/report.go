// Package report renders findings as text, JSON or SARIF.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andronaft/k8s-guardian/internal/rules"
)

// Summary counts findings by severity.
type Summary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
	Fixable  int `json:"fixable"`
	Unsafe   int `json:"unsafeFixable,omitempty"`
	Fixed    int `json:"fixed,omitempty"`
}

// Summarize counts findings.
func Summarize(fs []rules.Finding) Summary {
	var s Summary
	for _, f := range fs {
		switch f.Severity {
		case rules.Error:
			s.Errors++
		case rules.Warning:
			s.Warnings++
		default:
			s.Infos++
		}
		if f.Fixable {
			s.Fixable++
		}
		if f.UnsafeFix {
			s.Unsafe++
		}
	}
	return s
}

// Write renders findings in the given format ("text", "json", "sarif").
func Write(w io.Writer, format string, fs []rules.Finding, s Summary, fixMode bool) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if fs == nil {
			fs = []rules.Finding{}
		}
		return enc.Encode(struct {
			Findings []rules.Finding `json:"findings"`
			Summary  Summary         `json:"summary"`
		}{fs, s})
	case "sarif":
		return writeSARIF(w, fs)
	case "github":
		WriteGitHub(w, fs)
		return nil
	case "markdown", "md":
		WriteMarkdown(w, fs, s)
		return nil
	case "text", "":
		writeText(w, fs, s, fixMode)
		return nil
	}
	return fmt.Errorf("unknown output format %q (use text, json, sarif, github or markdown)", format)
}

var color = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if !color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func icon(sev rules.Severity) string {
	switch sev {
	case rules.Error:
		return paint("31", "✖ error  ")
	case rules.Warning:
		return paint("33", "⚠ warning")
	}
	return paint("36", "ℹ info   ")
}

// Text renders findings as plain text (no colors); used by the MCP server.
func Text(fs []rules.Finding) string {
	var b strings.Builder
	prev := color
	color = false
	writeText(&b, fs, Summarize(fs), false)
	color = prev
	return b.String()
}

func writeText(w io.Writer, fs []rules.Finding, s Summary, fixMode bool) {
	if len(fs) == 0 {
		if s.Fixed > 0 {
			fmt.Fprintf(w, "%s applied %d fix(es); all checks pass\n", paint("32", "✔"), s.Fixed)
		} else {
			fmt.Fprintln(w, paint("32", "✔ no issues found"))
		}
		return
	}
	var group string
	for _, f := range fs {
		g := f.Source + "  " + f.Resource
		if g != group {
			if group != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, paint("1", g))
			group = g
		}
		where := ""
		if f.Container != "" {
			where = fmt.Sprintf("container %q: ", f.Container)
		}
		extra := ""
		if f.Fixable {
			extra = paint("32", " [fixable]")
		} else if f.UnsafeFix {
			extra = paint("33", " [unsafe fix]")
		}
		if f.Line > 0 {
			extra += paint("2", fmt.Sprintf(" (line %d)", f.Line))
		}
		fmt.Fprintf(w, "  %s %s  %s%s%s\n", icon(f.Severity), paint("2", f.RuleID), where, f.Message, extra)
	}
	fmt.Fprintln(w)
	msg := fmt.Sprintf("%d error(s), %d warning(s), %d info", s.Errors, s.Warnings, s.Infos)
	if s.Fixed > 0 {
		msg = fmt.Sprintf("applied %d fix(es); remaining: %s", s.Fixed, msg)
	}
	fmt.Fprintln(w, msg)
	if !fixMode && s.Fixable > 0 {
		fmt.Fprintf(w, "%d issue(s) can be fixed automatically with --fix (add --ai to let Claude fix the rest)\n", s.Fixable)
	}
	if s.Unsafe > 0 {
		fmt.Fprintf(w, "%d more with --fix --unsafe-fixes: these can change how the workload runs (root images, writable filesystems, resources), so review them\n", s.Unsafe)
	}
}

var extraRules []*rules.Rule

// RegisterRules adds non built-in rules (live, diff) to SARIF output.
func RegisterRules(rs ...*rules.Rule) { extraRules = append(extraRules, rs...) }

func writeSARIF(w io.Writer, fs []rules.Finding) error {
	type msg struct {
		Text string `json:"text"`
	}
	var sarifRules []map[string]any
	for _, r := range append(append([]*rules.Rule{}, rules.All...), extraRules...) {
		sarifRules = append(sarifRules, map[string]any{
			"id":               r.ID,
			"name":             r.Name,
			"shortDescription": msg{r.Description},
			"defaultConfiguration": map[string]string{
				"level": sarifLevel(r.Severity),
			},
		})
	}
	results := []map[string]any{}
	for _, f := range fs {
		m := f.Resource + ": " + f.Message
		if f.Container != "" {
			m = fmt.Sprintf("%s (container %q): %s", f.Resource, f.Container, f.Message)
		}
		line := f.Line
		if line < 1 {
			line = 1
		}
		results = append(results, map[string]any{
			"ruleId":  f.RuleID,
			"level":   sarifLevel(f.Severity),
			"message": msg{m},
			"locations": []any{map[string]any{
				"physicalLocation": map[string]any{
					"artifactLocation": map[string]string{"uri": f.Source},
					"region":           map[string]int{"startLine": line},
				},
			}},
		})
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool": map[string]any{"driver": map[string]any{
				"name":           "k8s-guardian",
				"informationUri": "https://github.com/andronaft/k8s-guardian",
				"rules":          sarifRules,
			}},
			"results": results,
		}},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func sarifLevel(s rules.Severity) string {
	switch s {
	case rules.Error:
		return "error"
	case rules.Warning:
		return "warning"
	}
	return "note"
}

// Paint applies an ANSI color code when stdout is a terminal.
func Paint(code, s string) string { return paint(code, s) }

// SeverityLabel is the colored severity label used in text output.
func SeverityLabel(sev rules.Severity) string { return icon(sev) }
