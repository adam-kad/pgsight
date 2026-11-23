package plan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testdataDir = "../../testdata"

func parseFile(t *testing.T, name string) *Plan {
	t.Helper()
	f, err := os.Open(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return p
}

// findNode returns the first node in pre-order that matches.
func findNode(t *testing.T, p *Plan, match func(*Node) bool) *Node {
	t.Helper()
	for _, n := range p.Nodes() {
		if match(n) {
			return n
		}
	}
	t.Fatal("no matching node")
	return nil
}

func byType(typ string) func(*Node) bool {
	return func(n *Node) bool { return n.NodeType == typ }
}

// TestParseJSONTestdata checks invariants that must hold for every JSON plan
// in testdata/.
func TestParseJSONTestdata(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(testdataDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no JSON plans in testdata")
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			p := parseFile(t, name)
			if p.Format != FormatJSON {
				t.Errorf("Format = %v, want json", p.Format)
			}
			if p.Root == nil {
				t.Fatal("Root is nil")
			}
			analyzed := strings.Contains(name, ".analyze.")
			if p.Analyzed != analyzed {
				t.Errorf("Analyzed = %v, want %v", p.Analyzed, analyzed)
			}
			if analyzed && p.ExecutionTime == nil {
				t.Error("ExecutionTime is nil for an ANALYZE plan")
			}
			// Plain EXPLAIN prints no summary unless SUMMARY is requested.
			if analyzed && p.PlanningTime == nil {
				t.Error("PlanningTime is nil for an ANALYZE plan")
			}
			if len(p.Warnings) > 0 {
				t.Errorf("unexpected warnings: %v", p.Warnings)
			}
			for _, n := range p.Nodes() {
				if n.NodeType == "" {
					t.Error("node without NodeType")
				}
				if n.TotalCost < n.StartupCost {
					t.Errorf("%s: total cost %v < startup cost %v", n.NodeType, n.TotalCost, n.StartupCost)
				}
				if analyzed != (n.Actual != nil) {
					t.Errorf("%s: Actual = %v, want present = %v", n.NodeType, n.Actual, analyzed)
				}
			}
		})
	}
}

func TestParseJSONSeqScan(t *testing.T) {
	p := parseFile(t, "seqscan_filter.analyze.json")
	n := p.Root
	if n.NodeType != "Seq Scan" || n.RelationName != "orders" || n.Alias != "orders" {
		t.Errorf("root = %q on %q as %q", n.NodeType, n.RelationName, n.Alias)
	}
	if n.Filter != "(amount = 42.5)" {
		t.Errorf("Filter = %q", n.Filter)
	}
	if n.RowsRemovedByFilter != 399600 {
		t.Errorf("RowsRemovedByFilter = %v", n.RowsRemovedByFilter)
	}
	if n.Actual.Rows != 400 || n.Actual.Loops != 1 {
		t.Errorf("Actual = %+v", *n.Actual)
	}
	if n.Buffers.SharedHit != 2598 {
		t.Errorf("Buffers.SharedHit = %d", n.Buffers.SharedHit)
	}
	if *p.ExecutionTime != 25.804 || *p.PlanningTime != 0.036 {
		t.Errorf("times = %v / %v", *p.PlanningTime, *p.ExecutionTime)
	}
	if p.PlanningBuffers != nil {
		t.Errorf("PlanningBuffers = %+v, want nil (all zero)", *p.PlanningBuffers)
	}
	if _, ok := n.Property("Async Capable"); !ok {
		t.Error("unknown keys should be kept as properties")
	}
}

func TestParseJSONHashBatches(t *testing.T) {
	p := parseFile(t, "hash_batches.analyze.json")
	if p.Root.NodeType != "Hash Join" || p.Root.JoinType != "Inner" {
		t.Errorf("root = %q (%q)", p.Root.NodeType, p.Root.JoinType)
	}
	if len(p.Root.Children) != 2 {
		t.Fatalf("root has %d children, want 2", len(p.Root.Children))
	}
	h := p.Root.Children[1]
	if h.NodeType != "Hash" || h.ParentRelationship != "Inner" {
		t.Errorf("second child = %q (%q)", h.NodeType, h.ParentRelationship)
	}
	if h.HashBatches != 8 || h.Buffers.TempWritten != 146 {
		t.Errorf("HashBatches = %d, TempWritten = %d", h.HashBatches, h.Buffers.TempWritten)
	}
	if p.PlanningBuffers == nil || p.PlanningBuffers.SharedHit != 14 {
		t.Errorf("PlanningBuffers = %+v", p.PlanningBuffers)
	}
}

func TestParseJSONSortDisk(t *testing.T) {
	n := parseFile(t, "sort_disk.analyze.json").Root
	if n.SortMethod != "external merge" || n.SortSpaceType != "Disk" || n.SortSpaceUsed != 13496 {
		t.Errorf("sort = %q %q %d", n.SortMethod, n.SortSpaceType, n.SortSpaceUsed)
	}
	if want := []string{"amount DESC", "created_at"}; strings.Join(n.SortKey, "|") != strings.Join(want, "|") {
		t.Errorf("SortKey = %q, want %q", n.SortKey, want)
	}
}

func TestParseJSONSubplans(t *testing.T) {
	p := parseFile(t, "cte_subplans.analyze.json")
	cte := findNode(t, p, func(n *Node) bool { return n.SubplanName == "CTE big" })
	if cte.ParentRelationship != "InitPlan" || cte.NodeType != "Aggregate" {
		t.Errorf("CTE root = %q (%q)", cte.NodeType, cte.ParentRelationship)
	}
	findNode(t, p, func(n *Node) bool { return n.SubplanName == "InitPlan 2 (returns $1)" })
	sub := findNode(t, p, func(n *Node) bool { return n.ParentRelationship == "SubPlan" })
	if sub.SubplanName != "SubPlan 3" || sub.Actual.Loops != 199 {
		t.Errorf("SubPlan = %q loops=%v", sub.SubplanName, sub.Actual.Loops)
	}
	scan := findNode(t, p, byType("CTE Scan"))
	if scan.CTEName != "big" || scan.Alias != "b" {
		t.Errorf("CTE Scan = %q as %q", scan.CTEName, scan.Alias)
	}
}

func TestParseJSONParallel(t *testing.T) {
	p := parseFile(t, "parallel_agg.analyze.json")
	g := findNode(t, p, byType("Gather Merge"))
	if g.WorkersPlanned != 1 || g.WorkersLaunched != 1 {
		t.Errorf("workers = %d/%d", g.WorkersPlanned, g.WorkersLaunched)
	}
	scan := findNode(t, p, byType("Seq Scan"))
	if !scan.ParallelAware || scan.Actual.Loops != 2 {
		t.Errorf("parallel scan: aware=%v loops=%v", scan.ParallelAware, scan.Actual.Loops)
	}
	sort := findNode(t, p, byType("Sort"))
	if v, ok := sort.Property("Worker 0"); !ok || !strings.Contains(v, "Sort Method: quicksort") {
		t.Errorf("Worker 0 property = %q, %v", v, ok)
	}
}

func TestParseJSONTriggers(t *testing.T) {
	p := parseFile(t, "update_trigger.analyze.json")
	if p.Root.NodeType != "ModifyTable" || p.Root.Operation != "Update" {
		t.Errorf("root = %q %q", p.Root.NodeType, p.Root.Operation)
	}
	if len(p.Triggers) != 1 {
		t.Fatalf("Triggers = %+v", p.Triggers)
	}
	tr := p.Triggers[0]
	if tr.Name != "orders_audit" || tr.Relation != "orders" || tr.Calls != 500 || tr.Time != 0.777 {
		t.Errorf("trigger = %+v", tr)
	}
}

func TestParseJSONEstimateOnly(t *testing.T) {
	p := parseFile(t, "join_estimate.plan.json")
	if p.Analyzed || p.ExecutionTime != nil {
		t.Errorf("Analyzed = %v, ExecutionTime = %v", p.Analyzed, p.ExecutionTime)
	}
	if p.Root.NodeType != "Sort" || p.Root.TotalCost <= 0 || p.Root.PlanRows != 5 {
		t.Errorf("root = %q cost=%v rows=%v", p.Root.NodeType, p.Root.TotalCost, p.Root.PlanRows)
	}
	if got := len(p.Nodes()); got != 6 {
		t.Errorf("node count = %d, want 6", got)
	}
}

func TestParseJSONWrappedAndBareAgree(t *testing.T) {
	wrapped := parseFile(t, "seqscan_filter.analyze.json")
	bare := parseFile(t, "seqscan_filter.analyze.bare.json")
	if wrapped.Root.NodeType != bare.Root.NodeType || wrapped.Root.Filter != bare.Root.Filter ||
		wrapped.Root.RowsRemovedByFilter != bare.Root.RowsRemovedByFilter {
		t.Errorf("wrapped %+v\n    bare %+v", wrapped.Root, bare.Root)
	}
}

func TestParseJSONVariants(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		root     string
		warnings int
	}{
		{"object instead of array", `{"Plan": {"Node Type": "Result"}}`, "Result", 0},
		{"bare node", `{"Node Type": "Result", "Total Cost": 0.01}`, "Result", 0},
		{"two plans in one array", `[{"Plan": {"Node Type": "Result"}}, {"Plan": {"Node Type": "Limit"}}]`, "Result", 1},
		{"two plans one after another", "[{\"Plan\": {\"Node Type\": \"Result\"}}]\n[{\"Plan\": {\"Node Type\": \"Limit\"}}]", "Result", 1},
		{"numbers as strings", `[{"Plan": {"Node Type": "Result", "Total Cost": "1.5"}}]`, "Result", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseString(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Root.NodeType != tt.root {
				t.Errorf("root = %q, want %q", p.Root.NodeType, tt.root)
			}
			if len(p.Warnings) != tt.warnings {
				t.Errorf("warnings = %v, want %d", p.Warnings, tt.warnings)
			}
		})
	}
}

func TestParseJSONJIT(t *testing.T) {
	in := `[{"Plan": {"Node Type": "Result"}, "JIT": {"Functions": 4,
	"Options": {"Inlining": false, "Optimization": false},
	"Timing": {"Generation": 0.5, "Inlining": 0.0, "Total": 7.25}},
	"Execution Time": 9.1}]`
	p, err := ParseString(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.JIT == nil || p.JIT.Functions != 4 || p.JIT.Total != 7.25 {
		t.Fatalf("JIT = %+v", p.JIT)
	}
	if len(p.JIT.Properties) != 2 || p.JIT.Properties[0].Value != "Inlining: false, Optimization: false" {
		t.Errorf("JIT properties = %+v", p.JIT.Properties)
	}
}

func TestParseJSONErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		line int
		msg  string
	}{
		{"truncated", "[\n  {\n    \"Plan\": {\n", 3, "truncated"},
		{"syntax error", "[\n  {\"Plan\": {\"Node Type\": \"Result\",, }}\n]", 2, "unexpected character"},
		{"syntax error under psql header", " QUERY PLAN\n----\n [ +\n   { +\n   ]\n(1 row)", 5, "unexpected character"},
		{"empty array", "[]", 0, "empty"},
		{"no plan key", `[{"Query": 1}]`, 0, `no "Plan" key`},
		{"scalar", `[42]`, 0, "expected an object"},
		{"node without type", `{"Plan": {"Plans": [{"Total Cost": 1}], "Node Type": "Result"}}`, 0, `Plan → Plans[0]: node has no "Node Type"`},
		{"wrong number type", `{"Plan": {"Node Type": "Result", "Total Cost": true}}`, 0, `"Total Cost" must be a number, got a boolean`},
		{"wrong plans type", `{"Plan": {"Node Type": "Result", "Plans": {}}}`, 0, `"Plans" must be an array`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseString(tt.in)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("error = %v, want *ParseError", err)
			}
			if pe.Line != tt.line {
				t.Errorf("Line = %d, want %d (%v)", pe.Line, tt.line, err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("error %q does not mention %q", err, tt.msg)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*"))
	for _, path := range files {
		if data, err := os.ReadFile(path); err == nil {
			f.Add(string(data))
		}
	}
	f.Add(`[{"Plan": {"Node Type": "Result", "Plans": [{"Node Type": "Limit"}]}}]`)
	f.Fuzz(func(t *testing.T, in string) {
		p, err := ParseString(in)
		if err == nil && p.Root == nil {
			t.Fatal("nil Root without an error")
		}
	})
}

func TestParseJSONTooDeep(t *testing.T) {
	in := strings.Repeat("[", 100000)
	_, err := ParseString(in)
	if err == nil || !strings.Contains(err.Error(), "max depth") {
		t.Fatalf("error = %v", err)
	}
}
