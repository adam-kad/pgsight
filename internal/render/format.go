// Package render draws a parsed plan as text. Plain renders the whole plan
// for pipes and CI; the TUI reuses the row and detail helpers.
package render

import (
	"math"
	"strconv"
	"strings"

	"github.com/adam-kad/pgsight/internal/plan"
)

// Duration formats milliseconds with precision that suits the magnitude.
func Duration(ms float64) string {
	switch {
	case ms == 0:
		return "0 ms"
	case ms < 1:
		return strconv.FormatFloat(ms, 'f', 3, 64) + " ms"
	case ms < 10:
		return strconv.FormatFloat(ms, 'f', 2, 64) + " ms"
	case ms < 1000:
		return strconv.FormatFloat(ms, 'f', 1, 64) + " ms"
	case ms < 60_000:
		return strconv.FormatFloat(ms/1000, 'f', 2, 64) + " s"
	default:
		return strconv.FormatFloat(ms/60_000, 'f', 1, 64) + " min"
	}
}

// Compact formats a count briefly: 950, 1.2k, 107k, 3.4M.
func Compact(v float64) string {
	switch {
	case v < 10 && v != math.Trunc(v):
		return strconv.FormatFloat(v, 'f', 2, 64)
	case v < 1000:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case v < 10_000:
		return strconv.FormatFloat(v/1000, 'f', 1, 64) + "k"
	case v < 1_000_000:
		return strconv.FormatFloat(v/1000, 'f', 0, 64) + "k"
	case v < 10_000_000:
		return strconv.FormatFloat(v/1_000_000, 'f', 1, 64) + "M"
	case v < 1_000_000_000:
		return strconv.FormatFloat(v/1_000_000, 'f', 0, 64) + "M"
	default:
		return strconv.FormatFloat(v/1_000_000_000, 'f', 1, 64) + "G"
	}
}

// Cost formats a planner cost.
func Cost(v float64) string {
	if v < 10_000 {
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	return Compact(v)
}

// Value formats a node metric in the plan's unit.
func Value(p *plan.Plan, v float64) string {
	if p.Metric == plan.MetricCost {
		return Cost(v)
	}
	return Duration(v)
}

// Percent formats a share with one decimal.
func Percent(pct float64) string {
	return strconv.FormatFloat(pct, 'f', 1, 64) + "%"
}

// Bar draws pct (0–100) as a bar of width cells using eighth blocks.
func Bar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	pct = math.Max(0, math.Min(100, pct))
	eighths := int(math.Round(pct / 100 * float64(width) * 8))
	if pct > 0 && eighths == 0 {
		eighths = 1 // make any non-zero share visible
	}
	full := eighths / 8
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	if rem := eighths % 8; rem > 0 {
		b.WriteRune([]rune("▏▎▍▌▋▊▉")[rem-1])
		full++
	}
	b.WriteString(strings.Repeat(" ", width-full))
	return b.String()
}

// Kilobytes formats a size given in kB.
func Kilobytes(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return strconv.FormatFloat(float64(kb)/1024/1024, 'f', 1, 64) + " GB"
	case kb >= 1024:
		return strconv.FormatFloat(float64(kb)/1024, 'f', 1, 64) + " MB"
	default:
		return strconv.FormatInt(kb, 10) + " kB"
	}
}

// RowsSummary describes a node's row counts, e.g. "1.0k rows (est 12) ×5000".
func RowsSummary(n *plan.Node) string {
	if n.Actual == nil {
		return "est " + rowsWord(n.PlanRows)
	}
	if n.Actual.NeverExecuted() {
		return "never executed"
	}
	s := rowsWord(n.Actual.Rows)
	if n.Actual.Rows != n.PlanRows {
		s += " (est " + Compact(n.PlanRows) + ")"
	}
	if n.Actual.Loops > 1 {
		s += " ×" + Compact(n.Actual.Loops)
	}
	return s
}

func rowsWord(v float64) string {
	if v == 1 {
		return "1 row"
	}
	return Compact(v) + " rows"
}
