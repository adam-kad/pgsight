package plan

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// TestMetricsInvariants: for every analyzed plan, exclusive times are
// non-negative and add up to the root's inclusive time (within rounding of
// the values EXPLAIN prints), and no percentage exceeds 100.
func TestMetricsInvariants(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*.analyze*"))
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			p := parseFile(t, name)
			if p.Metric != MetricTime {
				t.Fatalf("Metric = %v, want time", p.Metric)
			}
			sum := 0.0
			for _, n := range p.Nodes() {
				if n.Exclusive < 0 || n.ExclusivePercent < 0 || n.InclusivePercent > 100 {
					t.Errorf("%s: exclusive %v (%v%%), inclusive %v%%", n.Label(), n.Exclusive, n.ExclusivePercent, n.InclusivePercent)
				}
				if n.Exclusive > n.Inclusive+1e-9 {
					t.Errorf("%s: exclusive %v > inclusive %v", n.Label(), n.Exclusive, n.Inclusive)
				}
				sum += n.Exclusive
			}
			if tol := 0.02*p.Root.Inclusive + 0.01; !near(sum, p.Root.Inclusive, tol) {
				t.Errorf("sum of exclusive = %.3f, root inclusive = %.3f", sum, p.Root.Inclusive)
			}
			if p.Total != *p.ExecutionTime {
				t.Errorf("Total = %v, want Execution Time %v", p.Total, *p.ExecutionTime)
			}
		})
	}
}

func TestMetricsLoops(t *testing.T) {
	p := parseFile(t, "nested_loop.analyze.txt")
	inner := p.Root.Children[1]
	want := inner.Actual.TotalTime * 5000
	if inner.Actual.Loops != 5000 || !near(inner.Inclusive, want, 1e-9) {
		t.Errorf("inner inclusive = %v, want %v", inner.Inclusive, want)
	}
	if inner.Inclusive <= p.Root.Children[0].Inclusive {
		t.Error("5000 index scans should cost more than the single outer seq scan")
	}
}

func TestMetricsParallel(t *testing.T) {
	p := parseFile(t, "parallel_agg.analyze.txt")
	scan := findNode(t, p, byType("Seq Scan"))
	// Two processes (leader + 1 worker) each spent ~TotalTime in the scan
	// concurrently: wall-clock is TotalTime, not TotalTime * loops.
	if !near(scan.Inclusive, scan.Actual.TotalTime, 1e-9) {
		t.Errorf("parallel scan inclusive = %v, want %v", scan.Inclusive, scan.Actual.TotalTime)
	}
	gather := findNode(t, p, byType("Gather Merge"))
	if gather.Exclusive > gather.Inclusive*0.5 {
		t.Errorf("Gather Merge exclusive %v of %v: workers' time leaked into it", gather.Exclusive, gather.Inclusive)
	}
}

func TestMetricsCTE(t *testing.T) {
	p := parseFile(t, "cte_subplans.analyze.txt")
	cte := findNode(t, p, func(n *Node) bool { return n.SubplanName == "CTE big" })
	scan := findNode(t, p, byType("CTE Scan"))
	want := scan.Inclusive - cte.Inclusive
	if !near(scan.Exclusive, max(0, want), 1e-9) {
		t.Errorf("CTE Scan exclusive = %v, want %v", scan.Exclusive, want)
	}
	// The root must not lose the CTE's time twice.
	if p.Root.Exclusive <= 0 {
		t.Errorf("root exclusive = %v", p.Root.Exclusive)
	}
	// The CTE's own subtree keeps its time.
	if cte.ExclusivePercent+cte.Children[0].ExclusivePercent < 50 {
		t.Errorf("CTE subtree share = %v%%", cte.ExclusivePercent+cte.Children[0].ExclusivePercent)
	}
}

func TestMetricsNeverExecuted(t *testing.T) {
	p := parseFile(t, "never_executed.analyze.txt")
	for _, n := range p.Nodes() {
		if n.Actual.NeverExecuted() && (n.Inclusive != 0 || n.Exclusive != 0) {
			t.Errorf("%s: never executed but inclusive=%v exclusive=%v", n.Label(), n.Inclusive, n.Exclusive)
		}
	}
}

func TestMetricsCostFallback(t *testing.T) {
	for _, name := range []string{"join_estimate.plan.txt", "join_estimate.plan.json"} {
		p := parseFile(t, name)
		if p.Metric != MetricCost || p.Total != p.Root.TotalCost {
			t.Errorf("%s: Metric = %v, Total = %v", name, p.Metric, p.Total)
		}
		if !near(p.Root.InclusivePercent, 100, 1e-9) {
			t.Errorf("%s: root inclusive = %v%%", name, p.Root.InclusivePercent)
		}
	}

	// ANALYZE with TIMING OFF has rows but no times: fall back to cost.
	in := strings.Join([]string{
		"Limit  (cost=0.00..10.00 rows=1 width=4) (actual rows=1 loops=1)",
		"  ->  Seq Scan on t  (cost=0.00..8.00 rows=1 width=4) (actual rows=1 loops=1)",
		"Execution Time: 0.5 ms",
	}, "\n")
	p, err := ParseString(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Metric != MetricCost || !near(p.Root.ExclusivePercent, 20, 1e-9) {
		t.Errorf("Metric = %v, root exclusive = %v%%", p.Metric, p.Root.ExclusivePercent)
	}
}
