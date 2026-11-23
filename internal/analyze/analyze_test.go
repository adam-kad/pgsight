package analyze

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adam-kad/pgsight/internal/plan"
)

func load(t *testing.T, name string) *plan.Plan {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.ParseString(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func parse(t *testing.T, lines ...string) *plan.Plan {
	t.Helper()
	p, err := plan.ParseString(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// hintsFor returns the hints one rule produces for a plan.
func hintsFor(p *plan.Plan, rule string) []Hint {
	var out []Hint
	for _, h := range Analyze(p) {
		if h.Rule == rule {
			out = append(out, h)
		}
	}
	return out
}

func expectOne(t *testing.T, hints []Hint, nodeType string, sev Severity, msg ...string) {
	t.Helper()
	if len(hints) != 1 {
		t.Fatalf("got %d hints, want 1: %+v", len(hints), hints)
	}
	h := hints[0]
	if h.Node.NodeType != nodeType || h.Severity != sev {
		t.Errorf("hint on %s (%v), want %s (%v)", h.Node.NodeType, h.Severity, nodeType, sev)
	}
	for _, m := range msg {
		if !strings.Contains(h.Message+" "+h.Suggestion, m) {
			t.Errorf("hint %q / %q does not mention %q", h.Message, h.Suggestion, m)
		}
	}
}

func expectNone(t *testing.T, hints []Hint) {
	t.Helper()
	if len(hints) != 0 {
		t.Errorf("unexpected hints: %+v", hints)
	}
}

func TestSeqScanFilter(t *testing.T) {
	for _, f := range []string{"seqscan_filter.analyze.txt", "seqscan_filter.analyze.json"} {
		expectOne(t, hintsFor(load(t, f), "seq-scan-filter"), "Seq Scan", Warning,
			"read 400,000 rows and kept 400", "(amount = 42.5)")
	}
	// The filter keeps most rows: an index would not help.
	expectNone(t, hintsFor(load(t, "parallel_agg.analyze.txt"), "seq-scan-filter"))
	// Exactly at the threshold (1,000 + 99,000 rows read) does not fire.
	expectNone(t, hintsFor(load(t, "misestimate.analyze.txt"), "seq-scan-filter"))
	// Rows are counted across loops.
	p := parse(t,
		"Seq Scan on t  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..1.0 rows=10 loops=20000)",
		"  Filter: (a = 1)",
		"  Rows Removed by Filter: 90")
	expectOne(t, hintsFor(p, "seq-scan-filter"), "Seq Scan", Warning, "read 2,000,000 rows")
	// Needs ANALYZE.
	expectNone(t, hintsFor(load(t, "join_estimate.plan.txt"), "seq-scan-filter"))
}

func TestRowEstimate(t *testing.T) {
	expectOne(t, hintsFor(load(t, "misestimate.analyze.txt"), "row-estimate"), "Seq Scan", Warning,
		"expected 12 rows but got 1,000", "83x underestimate", "ANALYZE correlated", "CREATE STATISTICS")

	// Overestimate down to zero rows.
	expectOne(t, hintsFor(load(t, "bitmap_misestimate.analyze.txt"), "row-estimate"), "Hash Join", Warning,
		"got none")

	// Small numbers are noise.
	expectNone(t, hintsFor(parse(t,
		"Seq Scan on t  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..1.0 rows=50 loops=1)"), "row-estimate"))

	// Only the node where the error starts is reported.
	p := parse(t,
		"Sort  (cost=1.00..2.00 rows=10 width=4) (actual time=0.1..1.0 rows=5000 loops=1)",
		"  ->  Seq Scan on t  (cost=0.00..1.00 rows=10 width=4) (actual time=0.1..1.0 rows=5000 loops=1)")
	expectOne(t, hintsFor(p, "row-estimate"), "Seq Scan", Warning, "500x underestimate", "ANALYZE t.")

	// Below a Limit, fewer rows than estimated is expected...
	expectNone(t, hintsFor(load(t, "limit.analyze.txt"), "row-estimate"))
	// ...but an underestimate is still reported.
	p = parse(t,
		"Limit  (cost=1.00..2.00 rows=10 width=4) (actual time=0.1..1.0 rows=10 loops=1)",
		"  ->  Sort  (cost=1.00..2.00 rows=10 width=4) (actual time=0.1..1.0 rows=10 loops=1)",
		"        ->  Seq Scan on t  (cost=0.00..1.00 rows=5 width=4) (actual time=0.1..1.0 rows=5000 loops=1)")
	expectOne(t, hintsFor(p, "row-estimate"), "Seq Scan", Warning, "1,000x underestimate")

	// Aggregates get advice about the number of groups.
	p = parse(t,
		"HashAggregate  (cost=1.00..2.00 rows=200 width=4) (actual time=0.1..1.0 rows=7 loops=1)",
		"  Group Key: (g % 7)",
		"  ->  Function Scan on generate_series g  (cost=0.00..1.00 rows=200000 width=4) (actual time=0.1..1.0 rows=200000 loops=1)")
	expectOne(t, hintsFor(p, "row-estimate"), "Aggregate", Warning, "29x overestimate", "number of groups", "(g % 7)")

	// Never-executed nodes and plans without ANALYZE are skipped.
	expectNone(t, hintsFor(load(t, "never_executed.analyze.txt"), "row-estimate"))
	expectNone(t, hintsFor(load(t, "join_estimate.plan.json"), "row-estimate"))
}

func TestSortDisk(t *testing.T) {
	for _, f := range []string{"sort_disk.analyze.txt", "sort_disk.analyze.json"} {
		expectOne(t, hintsFor(load(t, f), "sort-disk"), "Sort", Warning,
			"spilled 13.2 MB to disk", "work_mem", "amount DESC, created_at")
	}
	// In-memory sorts are fine.
	expectNone(t, hintsFor(load(t, "parallel_agg.analyze.txt"), "sort-disk"))
}

func TestNestedLoop(t *testing.T) {
	for _, f := range []string{"nested_loop.analyze.txt", "nested_loop.analyze.json"} {
		expectOne(t, hintsFor(load(t, f), "nested-loop"), "Nested Loop", Warning,
			"ran 5,000 times", "Index Scan using orders_customer_id_idx on orders o")
	}
	// Exactly 1000 loops is allowed; cheap inner sides are only info.
	base := []string{
		"Nested Loop  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..100.0 rows=10 loops=1)",
		"  ->  Seq Scan on a  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..90.0 rows=1000 loops=1)",
	}
	p := parse(t, append(base, "  ->  Index Scan using b_pkey on b  (cost=0.00..1.00 rows=1 width=4) (actual time=0.001..0.001 rows=1 loops=1000)")...)
	expectNone(t, hintsFor(p, "nested-loop"))
	p = parse(t, append(base, "  ->  Index Scan using b_pkey on b  (cost=0.00..1.00 rows=1 width=4) (actual time=0.001..0.001 rows=1 loops=1001)")...)
	expectOne(t, hintsFor(p, "nested-loop"), "Nested Loop", Info, "ran 1,001 times")
	// The inner side never ran.
	expectNone(t, hintsFor(load(t, "never_executed.analyze.txt"), "nested-loop"))
}

func TestInefficientFilter(t *testing.T) {
	expectOne(t, hintsFor(load(t, "never_executed.analyze.txt"), "inefficient-filter"), "Seq Scan", Warning,
		"removed 50,000 rows and kept 0", "(country = 'XX'::text)")
	expectOne(t, hintsFor(load(t, "misestimate.analyze.txt"), "inefficient-filter"), "Seq Scan", Warning,
		"removed 99,000 rows and kept 1,000")
	// Covered by seq-scan-filter already.
	expectNone(t, hintsFor(load(t, "seqscan_filter.analyze.txt"), "inefficient-filter"))
	// Removes 9 rows per kept row: below the ratio.
	expectNone(t, hintsFor(load(t, "nested_loop.analyze.txt"), "inefficient-filter"))
	// Too few rows in total to matter.
	expectNone(t, hintsFor(parse(t,
		"Index Scan using i on t  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..1.0 rows=1 loops=1)",
		"  Filter: (b = 1)",
		"  Rows Removed by Filter: 9000"), "inefficient-filter"))
	// A cheap node gets an info hint.
	p := parse(t,
		"Hash Join  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..100.0 rows=1 loops=1)",
		"  ->  Seq Scan on big  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..95.0 rows=1 loops=1)",
		"  ->  Hash  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..1.0 rows=1 loops=1)",
		"        ->  Index Scan using i on t  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..1.0 rows=1 loops=1)",
		"              Filter: (b = 1)",
		"              Rows Removed by Filter: 20000")
	expectOne(t, hintsFor(p, "inefficient-filter"), "Index Scan", Info)
}

func TestHashBatches(t *testing.T) {
	for _, f := range []string{"hash_batches.analyze.txt", "hash_batches.analyze.json"} {
		expectOne(t, hintsFor(load(t, f), "hash-batches"), "Hash", Warning, "8 batches", "work_mem", "375 kB")
	}
	expectNone(t, hintsFor(load(t, "bitmap_misestimate.analyze.txt"), "hash-batches"))
	p := parse(t,
		"Hash  (cost=0.00..1.00 rows=1 width=4) (actual time=0.1..1.0 rows=1 loops=1)",
		"  Buckets: 1024 (originally 1024)  Batches: 4 (originally 1)  Memory Usage: 4000kB")
	expectOne(t, hintsFor(p, "hash-batches"), "Hash", Warning, "4 batches", "(planned 1)", "3.9 MB")
}

func TestAnalyzeOrderAndEmpty(t *testing.T) {
	if hints := Analyze(nil); hints != nil {
		t.Errorf("Analyze(nil) = %v", hints)
	}
	// No ANALYZE data: no hints at all.
	expectNone(t, Analyze(load(t, "join_estimate.plan.txt")))

	hints := Analyze(load(t, "misestimate.analyze.txt"))
	for i := 1; i < len(hints); i++ {
		if hints[i].Severity > hints[i-1].Severity {
			t.Errorf("hints not sorted by severity: %+v", hints)
		}
	}
	if len(ByNode(hints)[load(t, "misestimate.analyze.txt").Root]) != 0 {
		t.Error("ByNode keyed by a different plan's node")
	}
}

func TestParseSeverity(t *testing.T) {
	for in, want := range map[string]Severity{"info": Info, "WARNING": Warning, "warn": Warning} {
		if got, err := ParseSeverity(in); err != nil || got != want {
			t.Errorf("ParseSeverity(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseSeverity("fatal"); err == nil {
		t.Error("ParseSeverity(fatal) should fail")
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", 2.5: "2.50"} {
		if got := count(in); got != want {
			t.Errorf("count(%v) = %q, want %q", in, got, want)
		}
	}
	if got := percent(0, 10); got != "0%" {
		t.Errorf("percent(0) = %q", got)
	}
	if got := percent(1, 10000); got != "<0.1%" {
		t.Errorf("percent(tiny) = %q", got)
	}
}
