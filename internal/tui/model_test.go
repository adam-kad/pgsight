package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/adam-kad/pgsight/internal/analyze"
	"github.com/adam-kad/pgsight/internal/plan"
	"github.com/adam-kad/pgsight/internal/render"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func newModel(t *testing.T, file string, width, height int) Model {
	t.Helper()
	data, err := os.ReadFile("../../testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.ParseString(string(data))
	if err != nil {
		t.Fatal(err)
	}
	m := New(p, analyze.Analyze(p), render.NewTheme(&bytes.Buffer{}, false))
	return send(m, tea.WindowSizeMsg{Width: width, Height: height})
}

func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func keys(s ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range s {
		switch k {
		case "up":
			out = append(out, tea.KeyMsg{Type: tea.KeyUp})
		case "down":
			out = append(out, tea.KeyMsg{Type: tea.KeyDown})
		case "left":
			out = append(out, tea.KeyMsg{Type: tea.KeyLeft})
		case "right":
			out = append(out, tea.KeyMsg{Type: tea.KeyRight})
		case "enter":
			out = append(out, tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			out = append(out, tea.KeyMsg{Type: tea.KeyEsc})
		case "space":
			out = append(out, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		case "end":
			out = append(out, tea.KeyMsg{Type: tea.KeyEnd})
		default:
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		}
	}
	return out
}

func TestNavigation(t *testing.T) {
	m := newModel(t, "cte_subplans.analyze.txt", 120, 40)
	if m.Selected() != m.plan.Root {
		t.Fatal("cursor should start at the root")
	}
	m = send(m, keys("up")...) // already at the top
	if m.cursor != 0 {
		t.Errorf("cursor = %d after up at top", m.cursor)
	}
	m = send(m, keys("down", "down")...)
	if got := m.Selected().NodeType; got != "Index Scan" {
		t.Errorf("selected %s, want Index Scan", got)
	}
	m = send(m, keys("left")...) // leaf: go to parent
	if m.Selected().SubplanName != "CTE big" {
		t.Errorf("left on a leaf should select the parent, got %s", m.Selected().Label())
	}
	m = send(m, keys("left")...) // expanded: collapse
	if len(m.rows) != 10 || !m.rows[m.cursor].Collapsed {
		t.Errorf("rows = %d, collapsed = %v", len(m.rows), m.rows[m.cursor].Collapsed)
	}
	m = send(m, keys("right")...) // collapsed: expand
	if len(m.rows) != 11 {
		t.Errorf("rows after expand = %d", len(m.rows))
	}
	m = send(m, keys("right")...) // expanded: first child
	if m.Selected().NodeType != "Index Scan" {
		t.Errorf("right should move to the first child, got %s", m.Selected().Label())
	}
	m = send(m, keys("end")...)
	if m.cursor != len(m.rows)-1 {
		t.Errorf("end: cursor = %d", m.cursor)
	}
	m = send(m, keys("down")...)
	if m.cursor != len(m.rows)-1 {
		t.Errorf("cursor moved past the end: %d", m.cursor)
	}
	m = send(m, keys("g")...)
	if m.cursor != 0 {
		t.Errorf("g: cursor = %d", m.cursor)
	}
}

func TestToggleKeepsSelection(t *testing.T) {
	m := newModel(t, "cte_subplans.analyze.txt", 120, 40)
	m = send(m, keys("space")...)
	if len(m.rows) != 1 || m.Selected() != m.plan.Root {
		t.Fatalf("collapsing the root should leave one row, got %d", len(m.rows))
	}
	if !strings.Contains(ansi.Strip(m.View()), "▸ Hash Join (+10)") {
		t.Errorf("collapsed root not shown:\n%s", ansi.Strip(m.View()))
	}
	m = send(m, keys("space")...)
	if len(m.rows) != 11 {
		t.Errorf("rows = %d after expanding", len(m.rows))
	}
}

func TestNextHint(t *testing.T) {
	m := newModel(t, "hash_batches.analyze.txt", 120, 40)
	m = send(m, keys("space", "n")...) // collapse root, then jump to the first hint
	if m.Selected().NodeType != "Seq Scan" || len(m.rows) != 4 {
		t.Errorf("selected %s with %d rows", m.Selected().Label(), len(m.rows))
	}
	m = send(m, keys("n")...)
	if m.Selected().NodeType != "Hash" {
		t.Errorf("second hint: %s", m.Selected().Label())
	}
	m = send(m, keys("n")...) // wraps around
	if m.Selected().NodeType != "Seq Scan" {
		t.Errorf("wrap: %s", m.Selected().Label())
	}
}

func TestView(t *testing.T) {
	m := newModel(t, "hash_batches.analyze.txt", 100, 24)
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Errorf("view has %d lines, want 24", len(lines))
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > 100 {
			t.Errorf("line %d is %d cells wide: %q", i, w, l)
		}
	}
	for _, want := range []string{"Execution", "2 warnings", "SHARE", "Hash Join", "└─ ▾ Hash 💡", "quit"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestDetailsPanel(t *testing.T) {
	m := newModel(t, "hash_batches.analyze.txt", 100, 30)
	m = send(m, keys("n", "n", "enter")...)
	view := ansi.Strip(m.View())
	for _, want := range []string{"── Hash ", "[warning]", "8 batches", "Batches", "Buffers", "temp written=146"} {
		if !strings.Contains(view, want) {
			t.Errorf("details lack %q:\n%s", want, view)
		}
	}
	if n := len(strings.Split(view, "\n")); n != 30 {
		t.Errorf("view has %d lines, want 30", n)
	}
	m = send(m, keys("esc")...)
	if strings.Contains(ansi.Strip(m.View()), "temp written=146") {
		t.Error("esc should close the details panel")
	}
}

func TestScrolling(t *testing.T) {
	m := newModel(t, "cte_subplans.analyze.txt", 100, 10) // room for only a few rows
	h := m.treeHeight()
	if h >= len(m.rows) {
		t.Skipf("tree height %d fits all rows", h)
	}
	m = send(m, keys("end")...)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Bitmap Index Scan") || strings.Contains(view, "Hash Join  ") {
		t.Errorf("view not scrolled to the end:\n%s", view)
	}
	if m.offset != len(m.rows)-h {
		t.Errorf("offset = %d, want %d", m.offset, len(m.rows)-h)
	}
}

func TestHelpAndQuit(t *testing.T) {
	m := newModel(t, "limit.analyze.txt", 120, 30)
	m = send(m, keys("?")...)
	if !strings.Contains(ansi.Strip(m.View()), "page down") {
		t.Error("? should show the full help")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should quit")
	}
}

func TestTinyTerminal(t *testing.T) {
	m := newModel(t, "cte_subplans.analyze.txt", 20, 3)
	m = send(m, keys("enter", "down", "down")...)
	_ = m.View() // must not panic
	if m.treeHeight() < 1 {
		t.Errorf("tree height = %d", m.treeHeight())
	}
}
