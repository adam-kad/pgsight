package render

import (
	"fmt"
	"strings"

	"github.com/adam-kad/pgsight/internal/plan"
	"github.com/charmbracelet/x/ansi"
)

// Row is one line of the flattened tree.
type Row struct {
	Node        *plan.Node
	Depth       int
	Guide       string // tree-drawing characters before the node
	Parent      int    // index of the parent row; -1 for the root
	HasChildren bool
	Collapsed   bool
}

// Flatten lists the visible nodes in display order. Children of nodes in
// collapsed are hidden.
func Flatten(root *plan.Node, collapsed map[*plan.Node]bool) []Row {
	var rows []Row
	var walk func(n *plan.Node, depth, parent int, guide, childPrefix string)
	walk = func(n *plan.Node, depth, parent int, guide, childPrefix string) {
		idx := len(rows)
		rows = append(rows, Row{
			Node: n, Depth: depth, Guide: guide, Parent: parent,
			HasChildren: len(n.Children) > 0, Collapsed: collapsed[n],
		})
		if collapsed[n] {
			return
		}
		for i, c := range n.Children {
			last := i == len(n.Children)-1
			connector, next := "├─ ", "│  "
			if last {
				connector, next = "└─ ", "   "
			}
			walk(c, depth+1, idx, childPrefix+connector, childPrefix+next)
		}
	}
	if root != nil {
		walk(root, 0, -1, "", "")
	}
	return rows
}

// Column widths of the metrics printed before the tree.
const (
	valueWidth   = 9
	percentWidth = 6
	// BarWidth is the number of cells in the share bar.
	BarWidth = 10
	// MetricsWidth is the total width of the metric columns.
	MetricsWidth = valueWidth + 1 + percentWidth + 1 + BarWidth + 2
)

// RowOptions controls how RenderRow draws a row.
type RowOptions struct {
	Markers  bool // show ▾/▸ expand markers (interactive view)
	Width    int  // truncate to this many cells; 0 means no limit
	Selected bool
	Hints    int // number of hints for the node
}

// ColumnHeader is the title line above the rows.
func (t *Theme) ColumnHeader(p *plan.Plan) string {
	self := "SELF"
	if p.Metric == plan.MetricCost {
		self = "SELF COST"
	}
	h := fmt.Sprintf("%*s %*s %-*s  NODE", valueWidth, self, percentWidth, "%", BarWidth, "SHARE")
	return t.Dim.Render(h)
}

// RenderRow draws one tree row: self value, share, bar, then the node.
func (t *Theme) RenderRow(p *plan.Plan, r Row, o RowOptions) string {
	n := r.Node
	never := n.Actual != nil && n.Actual.NeverExecuted()

	value := Value(p, n.Exclusive)
	if never {
		value = "—"
	}
	cols := fmt.Sprintf("%*s %*s %s  ", valueWidth, value, percentWidth, Percent(n.ExclusivePercent), Bar(n.ExclusivePercent, BarWidth))

	marker := ""
	if o.Markers {
		switch {
		case r.HasChildren && r.Collapsed:
			marker = "▸ "
		case r.HasChildren:
			marker = "▾ "
		default:
			marker = "  "
		}
	}
	subplan := ""
	if n.SubplanName != "" {
		subplan = n.SubplanName + ": "
	}
	target := ""
	if tg := n.Target(); tg != "" {
		target = " " + tg
	}
	hint := ""
	if o.Hints > 0 {
		hint = " 💡"
	}
	hidden := ""
	if r.Collapsed {
		hidden = fmt.Sprintf(" (+%d)", countDescendants(n))
	}
	rows := "  " + RowsSummary(n)

	var line string
	if o.Selected {
		plain := cols + r.Guide + marker + subplan + n.Title() + target + hidden + hint + rows
		line = plain
		if o.Width > 0 {
			line = ansi.Truncate(line, o.Width, "…")
			line += strings.Repeat(" ", max(0, o.Width-ansi.StringWidth(line)))
		}
		return t.Selected.Render(line)
	}

	heat := t.Heat(n.ExclusivePercent)
	if never {
		heat = t.Dim
	}
	title := t.Title
	if never {
		title = t.Dim
	}
	line = heat.Render(cols) +
		t.Dim.Render(r.Guide+marker) +
		t.Subplan.Render(subplan) +
		title.Render(n.Title()) + t.Target.Render(target) +
		t.Dim.Render(hidden) + hint +
		t.Dim.Render(rows)
	if o.Width > 0 {
		line = ansi.Truncate(line, o.Width, "…")
	}
	return line
}

func countDescendants(n *plan.Node) int {
	c := 0
	n.Walk(func(*plan.Node, int) bool { c++; return true })
	return c - 1
}

// Summary returns the header lines: what kind of plan this is, total
// timings and a count of hints.
func (t *Theme) Summary(p *plan.Plan, warnings, infos int) []string {
	var lines []string

	kind := "EXPLAIN ANALYZE"
	if !p.Analyzed {
		kind = "EXPLAIN (estimates only)"
	}
	lines = append(lines, t.Header.Render(kind)+t.Dim.Render(" · "+p.Format.String()+" format"))

	var parts []string
	if p.ExecutionTime != nil {
		parts = append(parts, "Execution "+t.Header.Render(Duration(*p.ExecutionTime)))
	}
	if p.PlanningTime != nil {
		parts = append(parts, "Planning "+Duration(*p.PlanningTime))
	}
	if len(p.Triggers) > 0 {
		total := 0.0
		for _, tr := range p.Triggers {
			total += tr.Time
		}
		parts = append(parts, "Triggers "+Duration(total))
	}
	if p.JIT != nil && p.JIT.Total > 0 {
		parts = append(parts, "JIT "+Duration(p.JIT.Total))
	}
	if p.Metric == plan.MetricCost {
		parts = append(parts, "Total cost "+Cost(p.Root.TotalCost))
	}
	if len(parts) > 0 {
		lines = append(lines, strings.Join(parts, t.Dim.Render(" · ")))
	}
	if p.Metric == plan.MetricCost {
		note := "No timing data: shares are of the planner's estimated cost."
		if !p.Analyzed {
			note += " Run EXPLAIN ANALYZE for real timings and hints."
		}
		lines = append(lines, t.Dim.Render(note))
	}

	switch {
	case warnings+infos == 0 && p.Analyzed:
		lines = append(lines, t.Cool.Render("No issues found"))
	case warnings+infos > 0:
		var hs []string
		if warnings > 0 {
			hs = append(hs, t.Warning.Render(plural(warnings, "warning")))
		}
		if infos > 0 {
			hs = append(hs, t.Info.Render(plural(infos, "note")))
		}
		lines = append(lines, "💡 "+strings.Join(hs, ", "))
	}
	for _, w := range p.Warnings {
		lines = append(lines, t.Warning.Render("note: ")+w)
	}
	return lines
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
