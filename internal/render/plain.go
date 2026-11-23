package render

import (
	"bufio"
	"io"
	"strings"

	"github.com/adam-kad/pgsight/internal/analyze"
	"github.com/adam-kad/pgsight/internal/plan"
)

// Plain writes the whole plan non-interactively: summary, the full tree
// and the list of hints. Colors come from the theme; with a colorless theme
// the output is plain text suitable for CI logs.
func Plain(w io.Writer, p *plan.Plan, hints []analyze.Hint, t *Theme) error {
	bw := bufio.NewWriter(w)
	line := func(s string) {
		bw.WriteString(strings.TrimRight(s, " "))
		bw.WriteByte('\n')
	}

	warnings, infos := CountHints(hints)
	for _, l := range t.Summary(p, warnings, infos) {
		line(l)
	}
	line("")
	line(t.ColumnHeader(p))

	byNode := analyze.ByNode(hints)
	for _, r := range Flatten(p.Root, nil) {
		line(t.RenderRow(p, r, RowOptions{Hints: len(byNode[r.Node])}))
	}

	if len(hints) > 0 {
		line("")
		line(t.Header.Render("Hints"))
		for _, h := range hints {
			for _, l := range t.HintLines(h) {
				line("  " + l)
			}
		}
	}
	return bw.Flush()
}

// HintLines formats one hint as:
//
//	[warning] Seq Scan on orders
//	          Seq Scan on orders read 400,000 rows and kept 400 (0.1%)
//	          → An index on the filtered columns may help: (amount = 42.5)
func (t *Theme) HintLines(h analyze.Hint) []string {
	tag, style := "[info]   ", t.Info
	if h.Severity == analyze.Warning {
		tag, style = "[warning]", t.Warning
	}
	indent := strings.Repeat(" ", len(tag)+1)
	node := h.Node.Label()
	if h.Node.SubplanName != "" {
		node = h.Node.SubplanName + ": " + node
	}
	return []string{
		style.Render(tag) + " " + t.Title.Render(node),
		indent + h.Message,
		indent + t.Dim.Render("→ ") + h.Suggestion,
	}
}

// CountHints splits hints by severity.
func CountHints(hints []analyze.Hint) (warnings, infos int) {
	for _, h := range hints {
		if h.Severity == analyze.Warning {
			warnings++
		} else {
			infos++
		}
	}
	return warnings, infos
}
