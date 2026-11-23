package plan

import "strings"

// Metric says what a plan's numbers measure.
type Metric int

const (
	// MetricTime means values are wall-clock milliseconds from EXPLAIN ANALYZE.
	MetricTime Metric = iota
	// MetricCost means values are planner cost units, used when there is no
	// timing data (plain EXPLAIN, or ANALYZE with TIMING OFF).
	MetricCost
)

func (m Metric) String() string {
	if m == MetricCost {
		return "cost"
	}
	return "time"
}

// TotalRows is the number of rows the node produced across all loops.
func (a Actual) TotalRows() float64 { return a.Rows * a.Loops }

// computeMetrics fills Inclusive/Exclusive values and percentages for every
// node, and the plan-wide Metric and Total.
//
// With timing data:
//
//   - A node's inclusive time is its per-loop time × loops: the time spent
//     in the node and everything below it.
//   - Nodes below Gather / Gather Merge run in several processes at once and
//     their loops count the processes, so multiplying by loops would add up
//     parallel time. Their inclusive time is divided by the number of
//     processes (launched workers + leader, at most loops) to approximate
//     wall-clock time.
//   - Exclusive time is inclusive time minus the inclusive time of the
//     children, clamped at zero (rounding in EXPLAIN output can make it
//     slightly negative).
//   - InitPlans and SubPlans run inside their parent node, so they are
//     subtracted like ordinary children. A CTE, however, is executed by the
//     CTE Scan nodes that read it, which already include its time; the CTE
//     is therefore not subtracted from its owner but from those CTE Scans.
//
// Percentages are relative to Execution Time when present, otherwise to the
// root's inclusive value.
//
// Without timing data the same arithmetic runs on total cost, without
// loops, parallel or CTE adjustments. Planner costs are not additive in the
// same way times are (a Nested Loop's cost already multiplies its inner
// side), so exclusive cost is only an approximation.
func (p *Plan) computeMetrics() {
	if p.Root == nil {
		return
	}
	p.Metric = MetricCost
	for _, n := range p.Nodes() {
		if n.Actual != nil && n.Actual.TotalTime > 0 {
			p.Metric = MetricTime
			break
		}
	}

	p.setInclusive(p.Root, 1)

	cteTime := map[string]float64{}
	for _, n := range p.Nodes() {
		children := 0.0
		for _, c := range n.Children {
			if name, ok := cteName(c); ok && p.Metric == MetricTime {
				cteTime[name] += c.Inclusive
				continue
			}
			children += c.Inclusive
		}
		n.Exclusive = max(0, n.Inclusive-children)
	}
	if len(cteTime) > 0 {
		for _, n := range p.Nodes() {
			if n.NodeType != "CTE Scan" {
				continue
			}
			take := min(cteTime[n.CTEName], n.Exclusive)
			n.Exclusive -= take
			cteTime[n.CTEName] -= take
		}
	}

	p.Total = p.Root.Inclusive
	if p.Metric == MetricTime && p.ExecutionTime != nil && *p.ExecutionTime > 0 {
		p.Total = *p.ExecutionTime
	}
	for _, n := range p.Nodes() {
		if p.Total > 0 {
			n.InclusivePercent = min(100, n.Inclusive/p.Total*100)
			n.ExclusivePercent = min(100, n.Exclusive/p.Total*100)
		}
	}
}

func (p *Plan) setInclusive(n *Node, parallelism float64) {
	childParallelism := parallelism
	if n.NodeType == "Gather" || n.NodeType == "Gather Merge" {
		workers := n.WorkersLaunched
		if n.Actual == nil {
			workers = n.WorkersPlanned
		}
		childParallelism = float64(workers + 1)
	}

	switch {
	case p.Metric == MetricCost:
		n.Inclusive = n.TotalCost
	case n.Actual == nil || n.Actual.NeverExecuted():
		n.Inclusive = 0
	default:
		div := max(1, min(parallelism, n.Actual.Loops))
		n.Inclusive = n.Actual.TotalTime * n.Actual.Loops / div
	}

	for _, c := range n.Children {
		p.setInclusive(c, childParallelism)
	}
}

// cteName reports whether n is the root of a CTE subplan ("CTE name").
func cteName(n *Node) (string, bool) {
	return strings.CutPrefix(n.SubplanName, "CTE ")
}
