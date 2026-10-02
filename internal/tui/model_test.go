package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

func load(t *testing.T) *manifest.File {
	data, err := os.ReadFile("../../examples/insecure-deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := manifest.Parse(data, "insecure.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		m.Update(msg)
	}
}

func TestInteractiveAcceptSkip(t *testing.T) {
	f := load(t)
	ps := Build([]*manifest.File{f}, rules.NewOptions(""), false)
	var fixable, manual int
	for _, p := range ps {
		switch p.Kind {
		case RuleFix:
			fixable++
		case Manual:
			manual++
		}
	}
	// Privileged and host-namespace findings have no deterministic fix.
	if fixable != 5 || manual != 7 {
		t.Fatalf("expected 5 rule fixes and 7 manual items, got %d/%d", fixable, manual)
	}
	m := New(ps, nil)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	if v := m.View(); !strings.Contains(v, "seccompProfile") || !strings.Contains(v, "+ ") {
		t.Errorf("view should show the first finding and a diff:\n%s", v)
	}
	first := ps[0]
	press(m, "y") // accept first
	if first.Status != Accepted {
		t.Fatalf("first proposal should be accepted, got %v", first.Status)
	}
	press(m, "n") // skip second
	if ps[1].Status != Skipped {
		t.Fatalf("second proposal should be skipped")
	}
	press(m, "a") // accept the remaining rule fixes
	for _, p := range ps {
		if p.Kind == RuleFix && p.Status == Pending {
			t.Errorf("rule fix still pending: %s", p.Title())
		}
	}
	press(m, "q")
	if !m.Save || len(m.Changed()) != 1 {
		t.Fatalf("expected save with one changed file")
	}
	out, _ := f.Encode()
	s := string(out)
	if !strings.Contains(s, "type: RuntimeDefault") {
		t.Error("accepted fix not applied")
	}
	if !strings.Contains(s, "# TODO pin me") {
		t.Error("comments must be preserved")
	}
	// The skipped fix must not be applied.
	skipped := ps[1].Findings[0]
	remaining := rules.Validate(f.Objects, rules.NewOptions(""))
	found := false
	for _, r := range remaining {
		if r.RuleID == skipped.RuleID && r.Container == skipped.Container {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped finding %s should still be reported", skipped.RuleID)
	}
}

func TestLineDiff(t *testing.T) {
	d := LineDiff("a\nb\nc\n", "a\nx\nc\n")
	var ops string
	for _, l := range d {
		ops += string(l.Op)
	}
	if ops != " -+ " {
		t.Errorf("unexpected diff ops %q", ops)
	}
}
