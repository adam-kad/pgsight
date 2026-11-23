package plan

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// shape is the part of a node that must not depend on the input format.
type shape struct {
	NodeType, JoinType, Strategy, PartialMode, Operation, ScanDirection string
	SubplanName, RelationName, Schema, Alias, IndexName, CTEName        string
	FunctionName, Filter, IndexCond, RecheckCond, HashCond              string
	SortMethod, SortSpaceType                                           string
	SortKey, GroupKey                                                   []string
	ParallelAware, Analyzed, NeverExecuted                              bool
	HashBatches, WorkersPlanned, WorkersLaunched, Children              int
	TempWritten                                                         bool
}

func shapeOf(n *Node) shape {
	s := shape{
		NodeType: n.NodeType, JoinType: n.JoinType, Strategy: n.Strategy,
		PartialMode: n.PartialMode, Operation: n.Operation, ScanDirection: n.ScanDirection,
		SubplanName: n.SubplanName, RelationName: n.RelationName, Schema: n.Schema,
		Alias: n.Alias, IndexName: n.IndexName, CTEName: n.CTEName,
		FunctionName: n.FunctionName, Filter: n.Filter, IndexCond: n.IndexCond,
		RecheckCond: n.RecheckCond, HashCond: n.HashCond, SortMethod: n.SortMethod,
		SortSpaceType: n.SortSpaceType, SortKey: n.SortKey, GroupKey: n.GroupKey,
		ParallelAware: n.ParallelAware, Analyzed: n.Actual != nil,
		HashBatches: n.HashBatches, WorkersPlanned: n.WorkersPlanned,
		WorkersLaunched: n.WorkersLaunched, Children: len(n.Children),
		TempWritten: n.Buffers.TempWritten > 0,
	}
	if n.Actual != nil {
		s.NeverExecuted = n.Actual.NeverExecuted()
	}
	return s
}

// TestParseTextMatchesJSON parses each scenario in both formats and checks
// that the trees agree. The two files come from separate EXPLAIN runs, so
// only fields that do not vary between runs are compared.
func TestParseTextMatchesJSON(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*.txt"))
	compared := 0
	for _, txtPath := range files {
		jsonPath := strings.TrimSuffix(txtPath, ".txt") + ".json"
		if _, err := os.Stat(jsonPath); err != nil {
			continue
		}
		compared++
		name := strings.TrimSuffix(filepath.Base(txtPath), ".txt")
		t.Run(name, func(t *testing.T) {
			tp := parseFile(t, filepath.Base(txtPath))
			jp := parseFile(t, filepath.Base(jsonPath))
			if tp.Format != FormatText {
				t.Errorf("Format = %v, want text", tp.Format)
			}
			tn, jn := tp.Nodes(), jp.Nodes()
			if len(tn) != len(jn) {
				t.Fatalf("text has %d nodes, json has %d", len(tn), len(jn))
			}
			for i := range tn {
				ts, js := shapeOf(tn[i]), shapeOf(jn[i])
				if !reflect.DeepEqual(ts, js) {
					t.Errorf("node %d differs:\ntext %+v\njson %+v", i, ts, js)
				}
				if a, b := tn[i].Actual, jn[i].Actual; a != nil && b != nil && a.Loops != b.Loops {
					t.Errorf("node %d (%s): loops %v vs %v", i, tn[i].NodeType, a.Loops, b.Loops)
				}
			}
			if tp.Analyzed != jp.Analyzed ||
				(tp.ExecutionTime == nil) != (jp.ExecutionTime == nil) ||
				(tp.PlanningTime == nil) != (jp.PlanningTime == nil) ||
				len(tp.Triggers) != len(jp.Triggers) {
				t.Errorf("summary differs: text analyzed=%v exec=%v plan=%v triggers=%d; json analyzed=%v exec=%v plan=%v triggers=%d",
					tp.Analyzed, tp.ExecutionTime != nil, tp.PlanningTime != nil, len(tp.Triggers),
					jp.Analyzed, jp.ExecutionTime != nil, jp.PlanningTime != nil, len(jp.Triggers))
			}
			if len(tp.Warnings)+len(jp.Warnings) > 0 {
				t.Errorf("warnings: text %v, json %v", tp.Warnings, jp.Warnings)
			}
		})
	}
	if compared < 10 {
		t.Fatalf("only %d text/json pairs found in testdata", compared)
	}
}

var statsSuffixRe = regexp.MustCompile(`\s+\((cost|actual|never).*$`)

// TestLabelRoundTrip checks that Node.Label reproduces each node line of the
// text plans exactly, so the tree view shows names users recognise.
func TestLabelRoundTrip(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*.txt"))
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			p := parseFile(t, name)
			var want []string
			for i, line := range clean(readFile(t, name)).lines {
				l := strings.TrimSpace(line)
				if i == 0 || strings.HasPrefix(l, "->") {
					l = strings.TrimSpace(strings.TrimPrefix(l, "->"))
					want = append(want, statsSuffixRe.ReplaceAllString(l, ""))
				}
			}
			var got []string
			for _, n := range p.Nodes() {
				got = append(got, n.Label())
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("labels:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseTextSubplans(t *testing.T) {
	p := parseFile(t, "cte_subplans.analyze.txt")
	root := p.Root
	var kinds []string
	for _, c := range root.Children {
		kinds = append(kinds, c.ParentRelationship+"|"+c.SubplanName+"|"+c.NodeType)
	}
	want := []string{
		"InitPlan|CTE big|Aggregate",
		"InitPlan|InitPlan 2 (returns $1)|Aggregate",
		"||CTE Scan",
		"||Hash",
		"SubPlan|SubPlan 3|Aggregate",
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("root children:\n got %q\nwant %q", kinds, want)
	}
	if p.PlanningBuffers == nil || p.PlanningBuffers.SharedHit == 0 {
		t.Errorf("PlanningBuffers = %+v", p.PlanningBuffers)
	}
}

func TestParseTextDetails(t *testing.T) {
	p := parseFile(t, "hash_batches.analyze.txt")
	root := p.Root
	if root.Buffers.SharedHit != 3004 || root.Buffers.TempRead != 522 || root.Buffers.TempWritten != 522 {
		t.Errorf("root buffers = %+v", root.Buffers)
	}
	h := root.Children[1]
	if h.HashBuckets != 16384 || h.HashBatches != 8 || h.OriginalHashBatches != 8 || h.PeakMemoryUsage == 0 {
		t.Errorf("hash = buckets %d batches %d/%d mem %d", h.HashBuckets, h.HashBatches, h.OriginalHashBatches, h.PeakMemoryUsage)
	}

	s := parseFile(t, "sort_disk.analyze.txt").Root
	if s.SortSpaceUsed != 13496 {
		t.Errorf("SortSpaceUsed = %d", s.SortSpaceUsed)
	}

	b := findNode(t, parseFile(t, "bitmap_misestimate.analyze.txt"), byType("Bitmap Heap Scan"))
	if b.ExactHeapBlocks == 0 {
		t.Errorf("ExactHeapBlocks = %d", b.ExactHeapBlocks)
	}

	v := parseFile(t, "verbose.analyze.txt")
	if _, ok := v.Root.Property("Output"); !ok {
		t.Error("Output property missing")
	}
	if len(v.Properties) != 1 || v.Properties[0].Key != "Settings" {
		t.Errorf("plan properties = %+v", v.Properties)
	}
	if agg, _ := v.Root.Property("HashAgg Batches"); agg != "1" {
		t.Errorf("HashAgg Batches = %q", agg)
	}

	n := findNode(t, parseFile(t, "never_executed.analyze.txt"), byType("Bitmap Heap Scan"))
	if n.Actual == nil || !n.Actual.NeverExecuted() {
		t.Errorf("Actual = %+v, want never executed", n.Actual)
	}
}

func TestParseTextCostsOff(t *testing.T) {
	p := parseFile(t, "join_estimate.costs_off.plan.txt")
	if len(p.Nodes()) != 6 || p.Analyzed || p.Root.TotalCost != 0 {
		t.Errorf("nodes=%d analyzed=%v cost=%v", len(p.Nodes()), p.Analyzed, p.Root.TotalCost)
	}
	j := findNode(t, p, byType("Hash Join"))
	if j.HashCond != "(o.customer_id = c.id)" {
		t.Errorf("HashCond = %q", j.HashCond)
	}
}

func TestParseTextInline(t *testing.T) {
	t.Run("timing off", func(t *testing.T) {
		p, err := ParseString("Seq Scan on t  (cost=0.00..1.00 rows=10 width=4) (actual rows=7 loops=3)")
		if err != nil {
			t.Fatal(err)
		}
		if a := p.Root.Actual; a == nil || a.Rows != 7 || a.Loops != 3 || a.TotalTime != 0 {
			t.Errorf("Actual = %+v", a)
		}
	})
	t.Run("JIT and old-style summary", func(t *testing.T) {
		in := `Result  (cost=0.00..0.01 rows=1 width=4) (actual time=0.001..0.001 rows=1 loops=1)
Planning time: 0.5 ms
JIT:
  Functions: 2
  Options: Inlining false, Optimization false, Expressions true, Deforming true
  Timing: Generation 0.2 ms, Inlining 0.0 ms, Optimization 0.1 ms, Emission 1.3 ms, Total 1.6 ms
Execution time: 2.0 ms`
		p, err := ParseString(in)
		if err != nil {
			t.Fatal(err)
		}
		if p.PlanningTime == nil || *p.PlanningTime != 0.5 || p.ExecutionTime == nil || *p.ExecutionTime != 2 {
			t.Errorf("times = %v %v", p.PlanningTime, p.ExecutionTime)
		}
		if p.JIT == nil || p.JIT.Functions != 2 || p.JIT.Total != 1.6 || len(p.JIT.Properties) != 2 {
			t.Errorf("JIT = %+v", p.JIT)
		}
	})
	t.Run("quoted rows from a GUI client", func(t *testing.T) {
		in := "\"Limit  (cost=0.42..0.81 rows=10 width=23)\"\n\"  ->  Index Scan using \"\"Orders_pkey\"\" on orders  (cost=0.42..14031.59 rows=360564 width=23)\"\n"
		p, err := ParseString(in)
		if err != nil {
			t.Fatal(err)
		}
		if c := p.Root.Children; len(c) != 1 || c[0].IndexName != "Orders_pkey" {
			t.Fatalf("children = %+v", c)
		}
	})
	t.Run("joins, aggregates and modify nodes", func(t *testing.T) {
		in := `Insert on public.t  (cost=1.00..2.00 rows=1 width=1)
  ->  Nested Loop Left Join  (cost=1.00..2.00 rows=1 width=1)
        ->  Hash Right Anti Join  (cost=1.00..2.00 rows=1 width=1)
              ->  Finalize HashAggregate  (cost=1.00..2.00 rows=1 width=1)
                    ->  Parallel Index Only Scan Backward using "My Idx" on "My Table" x  (cost=1.00..2.00 rows=1 width=1)
              ->  Function Scan on generate_series g  (cost=1.00..2.00 rows=1 width=1)
        ->  HashSetOp Except All  (cost=1.00..2.00 rows=1 width=1)
              ->  Custom Scan (Citus Adaptive)  (cost=1.00..2.00 rows=1 width=1)`
		p, err := ParseString(in)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range p.Nodes() {
			got = append(got, n.NodeType+"/"+n.JoinType+n.Strategy+n.Operation+"/"+n.Label())
		}
		want := []string{
			"ModifyTable/Insert/Insert on public.t",
			"Nested Loop/Left/Nested Loop Left Join",
			"Hash Join/Right Anti/Hash Right Anti Join",
			"Aggregate/Hashed/Finalize HashAggregate",
			"Index Only Scan//Parallel Index Only Scan Backward using My Idx on My Table x",
			"Function Scan//Function Scan on generate_series g",
			"SetOp/Hashed/HashSetOp Except All",
			"Custom Scan//Custom Scan (Citus Adaptive)",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("nodes:\n got %q\nwant %q", got, want)
		}
		if p.Root.Schema != "public" {
			t.Errorf("Schema = %q", p.Root.Schema)
		}
	})
	t.Run("two plans", func(t *testing.T) {
		in := "Result  (cost=0.00..0.01 rows=1 width=4)\nPlanning Time: 0.1 ms\nResult  (cost=0.00..0.01 rows=1 width=4)\n"
		p, err := ParseString(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Warnings) != 1 || len(p.Root.Children) != 0 {
			t.Errorf("warnings = %v, children = %d", p.Warnings, len(p.Root.Children))
		}
	})
}

func TestParseTextErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		line int
		msg  string
	}{
		{"not a plan", "hello world\nfoo", 1, "expected a plan node"},
		{"summary first", "Planning Time: 0.1 ms", 1, "expected a plan node"},
		{"bad line under psql header", " QUERY PLAN\n----\n SELECT 1\n(1 row)", 3, "expected a plan node"},
		{"empty arrow", "Result  (cost=0.00..0.01 rows=1 width=4)\n  ->  ", 2, `after "->"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseString(tt.in)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %v, want *ParseError", err)
			}
			if pe.Line != tt.line || !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("error = %q (line %d), want line %d mentioning %q", err, pe.Line, tt.line, tt.msg)
			}
		})
	}
}
