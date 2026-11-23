package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/adam-kad/pgsight/internal/plan"
)

// Field is one "key: value" line of the node details panel.
type Field struct {
	Key   string
	Value string
}

// Details lists everything known about a node, most useful first.
func Details(p *plan.Plan, n *plan.Node) []Field {
	var f []Field
	add := func(k, v string) {
		if v != "" {
			f = append(f, Field{k, v})
		}
	}
	num := func(k string, v float64) {
		if v != 0 {
			add(k, comma(v))
		}
	}

	add("Node", n.Label())
	add("Subplan", n.SubplanName)

	share := func(v, pct float64) string { return Value(p, v) + " (" + Percent(pct) + ")" }
	if p.Metric == plan.MetricTime {
		add("Self time", share(n.Exclusive, n.ExclusivePercent))
		add("Total time", share(n.Inclusive, n.InclusivePercent))
	} else {
		add("Self cost", share(n.Exclusive, n.ExclusivePercent))
		add("Total cost", share(n.Inclusive, n.InclusivePercent))
	}

	if a := n.Actual; a != nil {
		if a.NeverExecuted() {
			add("Executed", "never")
		} else {
			rows := comma(a.Rows) + " per loop"
			if a.Loops > 1 {
				rows += " × " + comma(a.Loops) + " loops = " + comma(a.TotalRows())
			}
			add("Rows", rows)
			if a.TotalTime > 0 {
				add("Actual time", fmt.Sprintf("%s..%s per loop", trimFloat(a.StartupTime), Duration(a.TotalTime)))
			}
		}
	}
	add("Estimated rows", comma(n.PlanRows))
	if n.TotalCost > 0 {
		add("Cost", fmt.Sprintf("%s..%s", trimFloat(n.StartupCost), trimFloat(n.TotalCost)))
	}
	if n.PlanWidth > 0 {
		add("Width", strconv.Itoa(n.PlanWidth)+" bytes")
	}

	add("Join type", nonInner(n.JoinType))
	add("Index Cond", n.IndexCond)
	add("Recheck Cond", n.RecheckCond)
	add("Hash Cond", n.HashCond)
	add("Merge Cond", n.MergeCond)
	add("Join Filter", n.JoinFilter)
	num("Rows Removed by Join Filter", n.RowsRemovedByJoinFilter)
	add("Filter", n.Filter)
	num("Rows Removed by Filter", n.RowsRemovedByFilter)
	num("Rows Removed by Recheck", n.RowsRemovedByIndexRecheck)

	add("Sort Key", strings.Join(n.SortKey, ", "))
	if n.SortMethod != "" {
		m := n.SortMethod
		if n.SortSpaceType != "" {
			m += fmt.Sprintf(" (%s: %s)", n.SortSpaceType, Kilobytes(n.SortSpaceUsed))
		}
		add("Sort Method", m)
	}
	add("Group Key", strings.Join(n.GroupKey, ", "))

	if n.HashBuckets > 0 {
		add("Buckets", withOriginal(n.HashBuckets, n.OriginalHashBuckets))
	}
	if n.HashBatches > 0 {
		add("Batches", withOriginal(n.HashBatches, n.OriginalHashBatches))
	}
	if n.PeakMemoryUsage > 0 {
		add("Memory", Kilobytes(n.PeakMemoryUsage))
	}
	if n.ExactHeapBlocks+n.LossyHeapBlocks > 0 {
		add("Heap Blocks", fmt.Sprintf("exact %s, lossy %s", comma(float64(n.ExactHeapBlocks)), comma(float64(n.LossyHeapBlocks))))
	}
	if n.HeapFetches > 0 {
		add("Heap Fetches", comma(float64(n.HeapFetches)))
	}
	if n.WorkersPlanned > 0 || n.WorkersLaunched > 0 {
		add("Workers", fmt.Sprintf("%d planned, %d launched", n.WorkersPlanned, n.WorkersLaunched))
	}
	add("Buffers", BuffersString(n.Buffers))

	for _, prop := range n.Properties {
		add(prop.Key, firstNonEmpty(prop.Value, "—"))
	}
	return f
}

// BuffersString formats buffer usage like PostgreSQL does:
// "shared hit=10 read=2, temp read=5 written=5".
func BuffersString(b plan.Buffers) string {
	var groups []string
	group := func(name string, pairs ...any) {
		var parts []string
		for i := 0; i < len(pairs); i += 2 {
			if v := pairs[i+1].(int64); v != 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", pairs[i], v))
			}
		}
		if len(parts) > 0 {
			groups = append(groups, name+" "+strings.Join(parts, " "))
		}
	}
	group("shared", "hit", b.SharedHit, "read", b.SharedRead, "dirtied", b.SharedDirtied, "written", b.SharedWritten)
	group("local", "hit", b.LocalHit, "read", b.LocalRead, "dirtied", b.LocalDirtied, "written", b.LocalWritten)
	group("temp", "read", b.TempRead, "written", b.TempWritten)
	return strings.Join(groups, ", ")
}

func withOriginal(cur, orig int) string {
	if orig > 0 && orig != cur {
		return fmt.Sprintf("%d (originally %d)", cur, orig)
	}
	return strconv.Itoa(cur)
}

func nonInner(jt string) string {
	if jt == "Inner" {
		return ""
	}
	return jt
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// trimFloat prints a float without trailing zeros: 7598.00 → "7598", 0.42 → "0.42".
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// comma formats a number with thousands separators.
func comma(v float64) string {
	if v != float64(int64(v)) {
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	s := strconv.FormatInt(int64(v), 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
