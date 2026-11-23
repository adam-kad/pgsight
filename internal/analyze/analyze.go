// Package analyze inspects a parsed plan and produces optimisation hints.
//
// Every heuristic is a separate Rule with its own tests. Rules that need
// run-time statistics only fire on EXPLAIN ANALYZE plans.
package analyze

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/adam-kad/pgsight/internal/plan"
)

// Thresholds used by the rules.
const (
	// SeqScanMinRowsRead: a filtered Seq Scan reading more rows than this
	// (across all loops) is worth an index check.
	SeqScanMinRowsRead = 100_000
	// EstimateMaxRatio: actual/estimated rows beyond this factor (either
	// way) counts as a misestimate.
	EstimateMaxRatio = 10
	// EstimateMinRows: ignore misestimates where both numbers are smaller
	// than this; estimating 1 row and getting 15 is noise.
	EstimateMinRows = 100
	// NestedLoopMaxLoops: inner side of a Nested Loop executed more times
	// than this.
	NestedLoopMaxLoops = 1000
	// FilterMaxRemovedRatio: a filter removing more than this many rows per
	// row it keeps is inefficient...
	FilterMaxRemovedRatio = 10
	// FilterMinRemoved: ...provided it removed at least this many rows in
	// total.
	FilterMinRemoved = 10_000
	// ImpactPercent: below this share of execution time some hints are
	// downgraded from warning to info.
	ImpactPercent = 10
)

// Severity orders hints by importance.
type Severity int

// Severities, from least to most important.
const (
	Info Severity = iota
	Warning
)

func (s Severity) String() string {
	if s == Warning {
		return "warning"
	}
	return "info"
}

// ParseSeverity parses "info" or "warning".
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(s) {
	case "info":
		return Info, nil
	case "warning", "warn":
		return Warning, nil
	}
	return 0, fmt.Errorf("unknown severity %q (want info or warning)", s)
}

// Hint is one finding about one node.
type Hint struct {
	Rule       string
	Severity   Severity
	Node       *plan.Node
	Message    string // what happened
	Suggestion string // what to try
}

// Rule is a single heuristic.
type Rule struct {
	ID          string
	Description string
	Check       func(c *Context, n *plan.Node) *Hint
}

// Context gives rules access to the whole plan.
type Context struct {
	Plan    *plan.Plan
	parents map[*plan.Node]*plan.Node
}

// Parent returns the node above n, or nil for the root.
func (c *Context) Parent(n *plan.Node) *plan.Node { return c.parents[n] }

// HasAncestor reports whether some node above n satisfies match.
func (c *Context) HasAncestor(n *plan.Node, match func(*plan.Node) bool) bool {
	for p := c.parents[n]; p != nil; p = c.parents[p] {
		if match(p) {
			return true
		}
	}
	return false
}

func newContext(p *plan.Plan) *Context {
	c := &Context{Plan: p, parents: map[*plan.Node]*plan.Node{}}
	for _, n := range p.Nodes() {
		for _, ch := range n.Children {
			c.parents[ch] = n
		}
	}
	return c
}

// Rules lists every heuristic in the order hints are reported for a node.
var Rules = []Rule{
	{"seq-scan-filter", "Seq Scan that filters out most of a large table", checkSeqScanFilter},
	{"row-estimate", "Row count estimate off by more than 10x", checkRowEstimate},
	{"sort-disk", "Sort spilled to disk", checkSortDisk},
	{"nested-loop", "Nested Loop with more than 1000 inner loops", checkNestedLoop},
	{"inefficient-filter", "Filter discards far more rows than it keeps", checkInefficientFilter},
	{"hash-batches", "Hash did not fit in work_mem", checkHashBatches},
}

// Analyze runs all rules over the plan. Hints come back most severe first,
// then in tree order.
func Analyze(p *plan.Plan) []Hint {
	if p == nil || p.Root == nil {
		return nil
	}
	c := newContext(p)
	var hints []Hint
	for _, n := range p.Nodes() {
		for _, r := range Rules {
			if h := r.Check(c, n); h != nil {
				h.Rule, h.Node = r.ID, n
				hints = append(hints, *h)
			}
		}
	}
	sort.SliceStable(hints, func(i, j int) bool { return hints[i].Severity > hints[j].Severity })
	return hints
}

// ByNode groups hints by the node they refer to.
func ByNode(hints []Hint) map[*plan.Node][]Hint {
	m := map[*plan.Node][]Hint{}
	for _, h := range hints {
		m[h.Node] = append(m[h.Node], h)
	}
	return m
}

// executed reports whether n has ANALYZE data and actually ran.
func executed(n *plan.Node) bool {
	return n.Actual != nil && !n.Actual.NeverExecuted()
}

// impact downgrades a warning to info for nodes that take a small share of
// the run time.
func impact(c *Context, n *plan.Node) Severity {
	if c.Plan.Metric == plan.MetricTime && n.ExclusivePercent < ImpactPercent {
		return Info
	}
	return Warning
}

// --- seq-scan-filter --------------------------------------------------------

func seqScanRowsRead(n *plan.Node) float64 {
	return (n.Actual.Rows + n.RowsRemovedByFilter) * n.Actual.Loops
}

// isLargeFilteredSeqScan: a Seq Scan with a filter that read many rows and
// threw most of them away. If the filter keeps most rows an index would not
// help, so those scans are left alone.
func isLargeFilteredSeqScan(n *plan.Node) bool {
	return n.NodeType == "Seq Scan" && n.Filter != "" && executed(n) &&
		seqScanRowsRead(n) > SeqScanMinRowsRead &&
		n.RowsRemovedByFilter > n.Actual.Rows
}

func checkSeqScanFilter(_ *Context, n *plan.Node) *Hint {
	if !isLargeFilteredSeqScan(n) {
		return nil
	}
	read := seqScanRowsRead(n)
	kept := n.Actual.TotalRows()
	return &Hint{
		Severity: Warning,
		Message: fmt.Sprintf("Seq Scan on %s read %s rows and kept %s (%s)",
			n.RelationName, count(read), count(kept), percent(kept, read)),
		Suggestion: "An index on the filtered columns may let PostgreSQL skip the rest: " + n.Filter,
	}
}

// --- row-estimate -----------------------------------------------------------

// estimateRatio returns actual/estimated rows per loop, or 0 when the
// numbers are too small to judge.
func estimateRatio(n *plan.Node) float64 {
	if !executed(n) {
		return 0
	}
	act, est := n.Actual.Rows, n.PlanRows
	if math.Max(act, est) < EstimateMinRows {
		return 0
	}
	if est < 1 {
		est = 1
	}
	if act == 0 {
		return math.Inf(-1) // "estimated N, got none": treat as overestimate
	}
	return act / est
}

func misestimated(n *plan.Node) bool {
	r := estimateRatio(n)
	return r > EstimateMaxRatio || (r != 0 && r < 1.0/EstimateMaxRatio)
}

func checkRowEstimate(c *Context, n *plan.Node) *Hint {
	if !misestimated(n) {
		return nil
	}
	r := estimateRatio(n)
	under := r > 1
	// Below a Limit the executor stops early, so "fewer rows than
	// estimated" is expected there.
	if !under && c.HasAncestor(n, func(a *plan.Node) bool { return a.NodeType == "Limit" }) {
		return nil
	}
	// Report where the error starts, not every node it propagates to.
	for _, ch := range n.Children {
		if misestimated(ch) {
			return nil
		}
	}

	var msg string
	switch {
	case under:
		msg = fmt.Sprintf("Planner expected %s rows but got %s (%sx underestimate)",
			count(n.PlanRows), count(n.Actual.Rows), ratio(r))
	case n.Actual.Rows == 0:
		msg = fmt.Sprintf("Planner expected %s rows but got none", count(n.PlanRows))
	default:
		msg = fmt.Sprintf("Planner expected %s rows but got %s (%sx overestimate)",
			count(n.PlanRows), count(n.Actual.Rows), ratio(n.PlanRows/n.Actual.Rows))
	}
	if n.Actual.Loops > 1 {
		msg += " per loop"
	}

	sugg := "Statistics may be stale: run ANALYZE on the tables involved."
	switch {
	case n.NodeType == "Aggregate" && len(n.GroupKey) > 0:
		sugg = "The planner misjudged the number of groups. ANALYZE the tables involved; " +
			"CREATE STATISTICS (ndistinct) on " + strings.Join(n.GroupKey, ", ") + " can help when several columns are grouped."
	case n.RelationName != "":
		sugg = fmt.Sprintf("Statistics may be stale: run ANALYZE %s.", n.RelationName)
		if n.Filter != "" && strings.Contains(n.Filter, " AND ") {
			sugg += " If the filtered columns are correlated, CREATE STATISTICS can help the planner."
		}
	}
	return &Hint{Severity: Warning, Message: msg, Suggestion: sugg}
}

// --- sort-disk --------------------------------------------------------------

func checkSortDisk(_ *Context, n *plan.Node) *Hint {
	if !strings.HasPrefix(n.SortMethod, "external") {
		return nil
	}
	msg := fmt.Sprintf("Sort spilled to disk (%s)", n.SortMethod)
	if n.SortSpaceType == "Disk" && n.SortSpaceUsed > 0 {
		msg = fmt.Sprintf("Sort spilled %s to disk (%s)", kilobytes(n.SortSpaceUsed), n.SortMethod)
	}
	sugg := "Increase work_mem for this query so the sort fits in memory"
	if len(n.SortKey) > 0 {
		sugg += ", or add an index that returns rows already ordered by " + strings.Join(n.SortKey, ", ")
	}
	return &Hint{Severity: Warning, Message: msg, Suggestion: sugg + "."}
}

// --- nested-loop ------------------------------------------------------------

// innerChild returns the inner side of a join: its second regular child
// (InitPlans and SubPlans are not join inputs).
func innerChild(n *plan.Node) *plan.Node {
	var regular []*plan.Node
	for _, c := range n.Children {
		if c.ParentRelationship != "InitPlan" && c.ParentRelationship != "SubPlan" {
			regular = append(regular, c)
		}
	}
	if len(regular) < 2 {
		return nil
	}
	return regular[1]
}

func checkNestedLoop(c *Context, n *plan.Node) *Hint {
	if n.NodeType != "Nested Loop" {
		return nil
	}
	inner := innerChild(n)
	if inner == nil || !executed(inner) || inner.Actual.Loops <= NestedLoopMaxLoops {
		return nil
	}
	sev := Warning
	if c.Plan.Metric == plan.MetricTime && inner.InclusivePercent < ImpactPercent {
		sev = Info
	}
	return &Hint{
		Severity: sev,
		Message:  fmt.Sprintf("Inner side (%s) ran %s times", inner.Label(), count(inner.Actual.Loops)),
		Suggestion: "Each loop must be cheap, ideally an index lookup. If the outer row count was " +
			"underestimated, a Hash Join may be faster; check the estimates below this node.",
	}
}

// --- inefficient-filter -----------------------------------------------------

func checkInefficientFilter(c *Context, n *plan.Node) *Hint {
	if n.Filter == "" || !executed(n) || isLargeFilteredSeqScan(n) {
		return nil
	}
	removed := n.RowsRemovedByFilter * n.Actual.Loops
	kept := n.Actual.TotalRows()
	if removed < FilterMinRemoved || n.RowsRemovedByFilter <= FilterMaxRemovedRatio*n.Actual.Rows {
		return nil
	}
	return &Hint{
		Severity: impact(c, n),
		Message: fmt.Sprintf("Filter removed %s rows and kept %s (%s of rows read)",
			count(removed), count(kept), percent(kept, removed+kept)),
		Suggestion: "An index matching the filter could avoid reading the discarded rows: " + n.Filter,
	}
}

// --- hash-batches -----------------------------------------------------------

func checkHashBatches(_ *Context, n *plan.Node) *Hint {
	if n.NodeType != "Hash" || n.HashBatches <= 1 {
		return nil
	}
	msg := fmt.Sprintf("Hash table was split into %d batches and spilled to disk", n.HashBatches)
	if n.OriginalHashBatches > 0 && n.OriginalHashBatches != n.HashBatches {
		msg += fmt.Sprintf(" (planned %d)", n.OriginalHashBatches)
	}
	sugg := "Increase work_mem so the hash table fits in memory"
	if n.PeakMemoryUsage > 0 {
		sugg += fmt.Sprintf(" (it used %s per batch)", kilobytes(n.PeakMemoryUsage))
	}
	return &Hint{Severity: Warning, Message: msg, Suggestion: sugg + "."}
}

// --- formatting -------------------------------------------------------------

// count formats a row count with thousands separators: 1234567 → "1,234,567".
func count(v float64) string {
	if v != math.Trunc(v) && v < 10 {
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	s := strconv.FormatInt(int64(math.Round(v)), 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func percent(part, whole float64) string {
	if whole <= 0 {
		return "0%"
	}
	p := part / whole * 100
	switch {
	case p == 0:
		return "0%"
	case p > 0 && p < 0.1:
		return "<0.1%"
	case p < 10:
		return strconv.FormatFloat(p, 'f', 1, 64) + "%"
	default:
		return strconv.FormatFloat(p, 'f', 0, 64) + "%"
	}
}

func ratio(r float64) string {
	if r < 100 {
		return strconv.FormatFloat(r, 'f', 0, 64)
	}
	return count(r)
}

func kilobytes(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return strconv.FormatFloat(float64(kb)/1024/1024, 'f', 1, 64) + " GB"
	case kb >= 1024:
		return strconv.FormatFloat(float64(kb)/1024, 'f', 1, 64) + " MB"
	default:
		return strconv.FormatInt(kb, 10) + " kB"
	}
}
