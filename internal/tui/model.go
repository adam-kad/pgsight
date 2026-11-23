// Package tui is the interactive Bubble Tea view of a plan.
package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/adam-kad/pgsight/internal/analyze"
	"github.com/adam-kad/pgsight/internal/plan"
	"github.com/adam-kad/pgsight/internal/render"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Run shows the plan until the user quits. Keyboard input is read from in,
// which must be a terminal (when the plan arrives on stdin, pass /dev/tty).
func Run(p *plan.Plan, hints []analyze.Hint, theme *render.Theme, in io.Reader, out io.Writer) error {
	prog := tea.NewProgram(New(p, hints, theme),
		tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
	_, err := prog.Run()
	return err
}

// Model is the Bubble Tea model of the plan viewer.
type Model struct {
	plan    *plan.Plan
	hints   []analyze.Hint
	byNode  map[*plan.Node][]analyze.Hint
	parents map[*plan.Node]*plan.Node
	theme   *render.Theme

	collapsed map[*plan.Node]bool
	rows      []render.Row
	cursor    int
	offset    int // index of the first visible row

	width, height int
	details       bool

	keys keyMap
	help help.Model
}

// New builds the model. The view is sized by the first WindowSizeMsg.
func New(p *plan.Plan, hints []analyze.Hint, theme *render.Theme) Model {
	m := Model{
		plan:      p,
		hints:     hints,
		byNode:    analyze.ByNode(hints),
		parents:   map[*plan.Node]*plan.Node{},
		theme:     theme,
		collapsed: map[*plan.Node]bool{},
		keys:      defaultKeys(),
		help:      help.New(),
		width:     100,
		height:    30,
	}
	for _, n := range p.Nodes() {
		for _, c := range n.Children {
			m.parents[c] = n
		}
	}
	m.rows = render.Flatten(p.Root, m.collapsed)
	return m
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Selected returns the node under the cursor.
func (m Model) Selected() *plan.Node { return m.rows[m.cursor].Node }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
	case tea.KeyMsg:
		k := m.keys
		switch {
		case key.Matches(msg, k.Quit):
			return m, tea.Quit
		case key.Matches(msg, k.Up):
			m.cursor--
		case key.Matches(msg, k.Down):
			m.cursor++
		case key.Matches(msg, k.PageUp):
			m.cursor -= m.treeHeight()
		case key.Matches(msg, k.PageDown):
			m.cursor += m.treeHeight()
		case key.Matches(msg, k.Top):
			m.cursor = 0
		case key.Matches(msg, k.Bottom):
			m.cursor = len(m.rows) - 1
		case key.Matches(msg, k.Collapse):
			r := m.rows[m.cursor]
			if r.HasChildren && !r.Collapsed {
				m.setCollapsed(r.Node, true)
			} else if r.Parent >= 0 {
				m.cursor = r.Parent
			}
		case key.Matches(msg, k.Expand):
			r := m.rows[m.cursor]
			if r.Collapsed {
				m.setCollapsed(r.Node, false)
			} else if r.HasChildren {
				m.cursor++
			}
		case key.Matches(msg, k.Toggle):
			if r := m.rows[m.cursor]; r.HasChildren {
				m.setCollapsed(r.Node, !r.Collapsed)
			}
		case key.Matches(msg, k.Details):
			m.details = !m.details
		case key.Matches(msg, k.NextHint):
			m.nextHint()
		case key.Matches(msg, k.Help):
			m.help.ShowAll = !m.help.ShowAll
		case key.Matches(msg, k.Close):
			m.details, m.help.ShowAll = false, false
		}
	}
	m.clamp()
	return m, nil
}

func (m *Model) setCollapsed(n *plan.Node, v bool) {
	if v {
		m.collapsed[n] = true
	} else {
		delete(m.collapsed, n)
	}
	m.rebuild(n)
}

// rebuild recomputes visible rows and puts the cursor on node sel.
func (m *Model) rebuild(sel *plan.Node) {
	m.rows = render.Flatten(m.plan.Root, m.collapsed)
	for i, r := range m.rows {
		if r.Node == sel {
			m.cursor = i
			return
		}
	}
}

// nextHint moves to the next node (in tree order, wrapping around) that has
// hints, unfolding its ancestors if needed.
func (m *Model) nextHint() {
	if len(m.hints) == 0 {
		return
	}
	all := m.plan.Nodes()
	cur := 0
	for i, n := range all {
		if n == m.Selected() {
			cur = i
		}
	}
	for step := 1; step <= len(all); step++ {
		n := all[(cur+step)%len(all)]
		if len(m.byNode[n]) == 0 {
			continue
		}
		for p := m.parents[n]; p != nil; p = m.parents[p] {
			delete(m.collapsed, p)
		}
		m.rebuild(n)
		return
	}
}

func (m *Model) clamp() {
	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
	h := m.treeHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, len(m.rows)-h))
}

// Layout: summary, column header, tree, optional details panel, help.

func (m Model) summary() []string {
	w, i := render.CountHints(m.hints)
	return m.theme.Summary(m.plan, w, i)
}

func (m Model) footer() string {
	lines := strings.Split(m.help.View(m.keys), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join(lines, "\n")
}

// available is the number of lines shared by the tree and the details panel.
func (m Model) available() int {
	return m.height - len(m.summary()) - 1 - lineCount(m.footer())
}

// detailsHeight gives the panel the room its content needs while leaving
// the tree at least a third of the space (or all its rows, if fewer).
func (m Model) detailsHeight() int {
	if !m.details {
		return 0
	}
	avail := m.available()
	minTree := min(len(m.rows), max(3, avail/3))
	want := len(m.detailContent())
	return max(0, min(want, avail-minTree))
}

func (m Model) treeHeight() int {
	return max(1, m.available()-m.detailsHeight())
}

// View implements tea.Model.
func (m Model) View() string {
	var b strings.Builder
	for _, l := range m.summary() {
		b.WriteString(ansi.Truncate(l, m.width, "…") + "\n")
	}
	b.WriteString(ansi.Truncate(m.theme.ColumnHeader(m.plan), m.width, "…") + "\n")

	h := m.treeHeight()
	for i := m.offset; i < m.offset+h; i++ {
		if i < len(m.rows) {
			r := m.rows[i]
			b.WriteString(m.theme.RenderRow(m.plan, r, render.RowOptions{
				Markers: true, Width: m.width, Selected: i == m.cursor, Hints: len(m.byNode[r.Node]),
			}))
		}
		b.WriteByte('\n')
	}

	if h := m.detailsHeight(); h > 0 {
		for _, l := range fit(m.detailContent(), h, m.theme) {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString(m.footer())
	return b.String()
}

// detailContent renders the details panel for the selected node.
func (m Model) detailContent() []string {
	t := m.theme
	n := m.Selected()
	title := " " + n.Label() + " "
	rule := t.Border.Render("──") + t.Header.Render(ansi.Truncate(title, max(0, m.width-4), "…"))
	rule += t.Border.Render(strings.Repeat("─", max(0, m.width-ansi.StringWidth(rule))))
	lines := []string{rule}

	for _, h := range m.byNode[n] {
		for _, l := range t.HintLines(h) {
			lines = append(lines, wrap(l, m.width)...)
		}
	}

	fields := render.Details(m.plan, n)[1:] // the title already names the node
	keyWidth := 0
	for _, f := range fields {
		keyWidth = max(keyWidth, len(f.Key))
	}
	keyWidth = min(keyWidth, 28)
	for _, f := range fields {
		k := t.Key.Render(fmt.Sprintf("%-*s", keyWidth, f.Key))
		lines = append(lines, wrap(k+"  "+f.Value, m.width)...)
	}
	return lines
}

// fit cuts lines to exactly height, noting how many were left out.
func fit(lines []string, height int, t *render.Theme) []string {
	if len(lines) > height {
		more := len(lines) - height + 1
		lines = append(lines[:height-1:height-1], t.Dim.Render(fmt.Sprintf("… %d more lines (make the window taller)", more)))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func wrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	return strings.Split(ansi.Wrap(s, width, " ,"), "\n")
}

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }
