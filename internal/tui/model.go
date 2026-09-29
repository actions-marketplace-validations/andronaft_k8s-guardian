// Package tui is the interactive fix review UI (`k8s-guardian interactive`):
// findings on the left, the proposed change as a colored diff on the right,
// and [y] accept / [n] skip / [e] edit per fix.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"gopkg.in/yaml.v3"

	"github.com/andronaft/k8s-guardian/internal/ai"
	"github.com/andronaft/k8s-guardian/internal/manifest"
	"github.com/andronaft/k8s-guardian/internal/rules"
)

var (
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	sSel     = lipgloss.NewStyle().Bold(true).Reverse(true)
	sDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	sAdd     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	sDel     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	sErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	sWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	sInfo    = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	sBorder  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	sHelpKey = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
)

// Model is the Bubble Tea model.
type Model struct {
	Proposals []*Proposal
	cursor    int
	scroll    int
	width     int
	height    int
	ai        *ai.Client
	message   string
	// Save is true when the user quit with "q" (write changes), false on
	// ctrl+c (discard).
	Save bool
	Quit bool
}

// New creates the model. client may be nil when --ai is off.
func New(ps []*Proposal, client *ai.Client) *Model {
	return &Model{Proposals: ps, ai: client, width: 120, height: 40}
}

type aiResult struct {
	p           *Proposal
	root        *yaml.Node
	base, notes string
	err         error
}

type editResult struct {
	p    *Proposal
	path string
	err  error
}

func (m *Model) Init() tea.Cmd { return m.maybeFetch() }

func (m *Model) current() *Proposal {
	if len(m.Proposals) == 0 {
		return nil
	}
	return m.Proposals[m.cursor]
}

func (m *Model) maybeFetch() tea.Cmd {
	p := m.current()
	if p == nil || m.ai == nil || !p.NeedsAI() {
		return nil
	}
	p.Status, p.Err = Loading, ""
	client := m.ai
	return func() tea.Msg {
		root, base, notes, err := FetchAI(context.Background(), client, p)
		return aiResult{p, root, base, notes, err}
	}
}

func (m *Model) move(delta int) tea.Cmd {
	if len(m.Proposals) == 0 {
		return nil
	}
	m.cursor = (m.cursor + delta + len(m.Proposals)) % len(m.Proposals)
	m.scroll = 0
	return m.maybeFetch()
}

// nextPending moves to the next undecided proposal.
func (m *Model) nextPending() tea.Cmd {
	for i := 1; i <= len(m.Proposals); i++ {
		j := (m.cursor + i) % len(m.Proposals)
		if s := m.Proposals[j].Status; s == Pending || s == Failed {
			m.cursor, m.scroll = j, 0
			return m.maybeFetch()
		}
	}
	m.message = "All findings reviewed: press q to save and quit"
	return nil
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case aiResult:
		p := msg.p
		if msg.err != nil {
			p.Status, p.Err = Failed, msg.err.Error()
		} else {
			p.Status, p.aiRoot, p.aiBase, p.aiNotes = Pending, msg.root, msg.base, msg.notes
		}
	case editResult:
		defer os.Remove(msg.path)
		if msg.err != nil {
			m.message = "editor failed: " + msg.err.Error()
			break
		}
		data, err := os.ReadFile(msg.path) // #nosec G304 -- our own temp file
		if first, rest, ok := strings.Cut(string(data), "\n"); ok && strings.Contains(first, "edit and save to apply") {
			data = []byte(rest)
		}
		if err == nil && strings.TrimSpace(string(data)) == "" {
			m.message = "edit cancelled"
			break
		}
		if err == nil {
			var f *manifest.File
			if f, err = manifest.Parse(data, msg.p.Obj.Source); err == nil {
				if len(f.Objects) != 1 {
					err = fmt.Errorf("expected exactly one object, got %d", len(f.Objects))
				} else {
					msg.p.editRoot = f.Objects[0].Root
					msg.p.Accept()
					m.message = "✎ applied your edit"
					return m, m.nextPending()
				}
			}
		}
		m.message = "edit rejected: " + err.Error()
	case tea.KeyMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Model) key(k string) tea.Cmd {
	m.message = ""
	p := m.current()
	switch k {
	case "ctrl+c":
		m.Quit, m.Save = true, false
		return tea.Quit
	case "q", "w":
		m.Quit, m.Save = true, true
		return tea.Quit
	case "down", "j", "tab":
		return m.move(1)
	case "up", "k", "shift+tab":
		return m.move(-1)
	case "pgdown", "ctrl+d", "J":
		m.scroll += 10
	case "pgup", "ctrl+u", "K":
		m.scroll = max(0, m.scroll-10)
	case "y", "enter":
		if p == nil || p.Status == Accepted || p.Status == Edited {
			return nil
		}
		if p.Kind == Manual {
			m.message = "no automatic fix: press e to edit by hand, or restart with --ai"
			return nil
		}
		if p.Status == Loading {
			m.message = "waiting for Claude…"
			return nil
		}
		if _, _, ok := p.Preview(); !ok {
			p.Status = Skipped
			m.message = "already fixed by an earlier change"
			return m.nextPending()
		}
		p.Accept()
		return m.nextPending()
	case "n", "s":
		if p != nil && p.Status != Accepted && p.Status != Edited {
			p.Status = Skipped
		}
		return m.nextPending()
	case "a":
		n := 0
		for _, q := range m.Proposals {
			if q.Kind == RuleFix && q.Status == Pending {
				if _, _, ok := q.Preview(); ok && q.Accept() {
					n++
				} else {
					q.Status = Skipped
				}
			}
		}
		m.message = fmt.Sprintf("accepted %d rule-based fix(es)", n)
		return m.nextPending()
	case "e":
		if p == nil {
			return nil
		}
		return m.edit(p)
	case "r":
		if p != nil && p.Kind == AIFix && m.ai != nil {
			p.aiRoot = nil
			p.Status = Pending
			return m.maybeFetch()
		}
	}
	return nil
}

func (m *Model) edit(p *Proposal) tea.Cmd {
	content := Encode(p.Obj.Root)
	if root := p.proposed(); root != nil {
		content = Encode(root)
	}
	f, err := os.CreateTemp("", "k8s-guardian-*.yaml")
	if err != nil {
		m.message = err.Error()
		return nil
	}
	fmt.Fprintf(f, "# %s — edit and save to apply, or empty the file to cancel\n", p.Title())
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(f.Name())
		m.message = fmt.Sprintf("cannot write temp file: %v %v", werr, cerr)
		return nil
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	// The user's own $VISUAL/$EDITOR, run without a shell on our temp file.
	cmd := exec.Command(parts[0], append(parts[1:], f.Name())...) // #nosec G204 G702 -- user-configured editor
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editResult{p, f.Name(), err} })
}

// ---- view ------------------------------------------------------------------

func sevStyle(s rules.Severity) lipgloss.Style {
	switch s {
	case rules.Error:
		return sErr
	case rules.Warning:
		return sWarn
	}
	return sInfo
}

func statusIcon(p *Proposal) string {
	switch p.Status {
	case Accepted:
		return sAdd.Render("✔")
	case Edited:
		return sAdd.Render("✎")
	case Skipped:
		return sDim.Render("✗")
	case Loading:
		return sInfo.Render("…")
	case Failed:
		return sErr.Render("!")
	}
	if p.Kind == Manual {
		return sDim.Render("○")
	}
	return sevStyle(p.Findings[0].Severity).Render("●")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// View renders the UI.
func (m *Model) View() string {
	if m.Quit {
		return ""
	}
	if len(m.Proposals) == 0 {
		return "✔ no issues found. Press q to quit.\n"
	}
	leftW := min(56, m.width*2/5)
	rightW := m.width - leftW - 6
	bodyH := max(5, m.height-5)
	// Content widths inside the border padding.
	lw, rw := leftW-2, rightW-2

	// Left: findings grouped by resource.
	var left []string
	var group string
	start := max(0, m.cursor-bodyH/2)
	for i := start; i < len(m.Proposals) && len(left) < bodyH; i++ {
		p := m.Proposals[i]
		if g := p.Obj.Ref(); g != group {
			left = append(left, sTitle.Render(truncate(g, lw)))
			group = g
		}
		line := statusIcon(p) + " " + truncate(p.Title(), lw-3)
		if i == m.cursor {
			line = sSel.Render(truncate("▶ "+p.Title(), lw-1))
		}
		left = append(left, line)
	}

	// Right: details + diff.
	p := m.current()
	var right []string
	for _, f := range p.Findings {
		right = append(right, sevStyle(f.Severity).Render(strings.ToUpper(f.Severity.String()))+" "+sTitle.Render(f.RuleID+" "+f.Rule))
		msg := f.Message
		if f.Container != "" {
			msg = fmt.Sprintf("container %q: %s", f.Container, msg)
		}
		right = append(right, truncate(msg, rw))
	}
	if p.Kind != AIFix && p.rule != nil {
		right = append(right, sDim.Render(truncate(p.rule.Description, rw)))
	}
	right = append(right, "")
	switch {
	case p.Status == Loading:
		right = append(right, sInfo.Render("🤖 asking Claude ("+m.ai.ModelName()+") for a fix…"))
	case p.Status == Failed:
		right = append(right, sErr.Render("Claude failed: ")+truncate(p.Err, rw-16), sDim.Render("press r to retry"))
	case p.Kind == Manual:
		right = append(right, sDim.Render("No deterministic fix. Press e to edit by hand, or restart with --ai to get a Claude proposal."))
	default:
		before, after, ok := p.Preview()
		if !ok && p.Status == Pending {
			right = append(right, sDim.Render("Nothing to change: already fixed by an earlier change."))
			break
		}
		if p.Status == Accepted || p.Status == Edited {
			right = append(right, sAdd.Render("applied ✔"))
			break
		}
		var diff []string
		for _, l := range Hunks(LineDiff(before, after), 3) {
			text := truncate(string(l.Op)+" "+l.Text, rw)
			switch l.Op {
			case Add:
				text = sAdd.Render(text)
			case Del:
				text = sDel.Render(text)
			default:
				text = sDim.Render(text)
			}
			diff = append(diff, text)
		}
		if p.aiNotes != "" {
			diff = append(diff, "", sTitle.Render("Claude's notes:"))
			for _, l := range strings.Split(p.aiNotes, "\n") {
				diff = append(diff, truncate(l, rw))
			}
		}
		if m.scroll > len(diff)-1 {
			m.scroll = max(0, len(diff)-1)
		}
		right = append(right, diff[m.scroll:]...)
	}
	if len(right) > bodyH {
		right = right[:bodyH]
	}

	panes := lipgloss.JoinHorizontal(lipgloss.Top,
		sBorder.Width(leftW).Height(bodyH).Render(strings.Join(left, "\n")),
		sBorder.Width(rightW).Height(bodyH).Render(strings.Join(right, "\n")),
	)
	done := 0
	for _, q := range m.Proposals {
		if q.Status == Accepted || q.Status == Edited || q.Status == Skipped {
			done++
		}
	}
	help := fmt.Sprintf("%s accept  %s skip  %s edit  %s accept all rule fixes  %s next/prev  %s scroll  %s save & quit  %s abort   %d/%d reviewed",
		sHelpKey.Render("[y]"), sHelpKey.Render("[n]"), sHelpKey.Render("[e]"), sHelpKey.Render("[a]"),
		sHelpKey.Render("[↑↓]"), sHelpKey.Render("[J/K]"), sHelpKey.Render("[q]"), sHelpKey.Render("[ctrl+c]"), done, len(m.Proposals))
	header := sTitle.Render("◆ k8s-guardian interactive fix") + "  " + sDim.Render(m.current().File.Source)
	if m.message != "" {
		header += "  " + sWarn.Render(m.message)
	}
	return header + "\n" + panes + "\n" + help
}

// Run starts the UI and returns the final model.
func Run(ps []*Proposal, client *ai.Client) (*Model, error) {
	m := New(ps, client)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return m, err
}

// Changed returns the files touched by accepted/edited proposals.
func (m *Model) Changed() []*manifest.File {
	seen := map[*manifest.File]bool{}
	var out []*manifest.File
	for _, p := range m.Proposals {
		if (p.Status == Accepted || p.Status == Edited) && !seen[p.File] {
			seen[p.File] = true
			out = append(out, p.File)
		}
	}
	return out
}
