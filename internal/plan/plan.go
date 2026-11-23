// Package plan holds the in-memory model of a PostgreSQL execution plan and
// the parsers that build it from EXPLAIN output.
package plan

// Format is the EXPLAIN output format a plan was parsed from.
type Format int

// Supported input formats.
const (
	FormatUnknown Format = iota
	FormatText
	FormatJSON
)

func (f Format) String() string {
	switch f {
	case FormatText:
		return "text"
	case FormatJSON:
		return "json"
	default:
		return "unknown"
	}
}

// Plan is a parsed EXPLAIN result: the node tree plus the statement-level
// information PostgreSQL prints after it.
type Plan struct {
	Root   *Node
	Format Format

	// Analyzed reports whether the plan came from EXPLAIN ANALYZE, i.e. at
	// least one node carries actual run-time statistics.
	Analyzed bool

	// Metric says whether node Inclusive/Exclusive values are milliseconds
	// or cost units, and Total is the value that counts as 100%.
	Metric Metric
	Total  float64

	// PlanningTime and ExecutionTime are in milliseconds; nil when absent.
	PlanningTime  *float64
	ExecutionTime *float64

	// PlanningBuffers is set when EXPLAIN (BUFFERS) reported buffer usage
	// during planning (PostgreSQL 13+).
	PlanningBuffers *Buffers

	Triggers []Trigger
	JIT      *JIT

	// Properties keeps other statement-level details, such as "Settings"
	// or "Query Identifier", for display.
	Properties []Property

	// Warnings collects non-fatal problems found while parsing, such as
	// extra plans in the input that were ignored.
	Warnings []string
}

// Nodes returns every node of the plan in depth-first pre-order.
func (p *Plan) Nodes() []*Node {
	var out []*Node
	p.Root.Walk(func(n *Node, _ int) bool {
		out = append(out, n)
		return true
	})
	return out
}

// Trigger is the run-time summary of one trigger fired by the statement.
type Trigger struct {
	Name     string
	Relation string
	Time     float64 // total milliseconds across all calls
	Calls    float64
}

// JIT summarises just-in-time compilation for the statement.
type JIT struct {
	Functions int
	Total     float64 // total milliseconds spent on JIT
	// Properties keeps the remaining details (options, timing breakdown)
	// for display.
	Properties []Property
}

// Buffers is the block usage reported by EXPLAIN (BUFFERS).
type Buffers struct {
	SharedHit, SharedRead, SharedDirtied, SharedWritten int64
	LocalHit, LocalRead, LocalDirtied, LocalWritten     int64
	TempRead, TempWritten                               int64
}

// IsZero reports whether no buffer usage was recorded.
func (b Buffers) IsZero() bool { return b == Buffers{} }

// Actual holds the run-time statistics of EXPLAIN ANALYZE for one node.
//
// As in PostgreSQL's output, StartupTime, TotalTime and Rows are averages
// per loop: the totals across all executions are TotalTime*Loops and
// Rows*Loops.
type Actual struct {
	StartupTime float64 // milliseconds
	TotalTime   float64 // milliseconds
	Rows        float64
	Loops       float64
}

// NeverExecuted reports whether the executor never ran this node.
func (a Actual) NeverExecuted() bool { return a.Loops == 0 }

// Property is a node attribute the model has no dedicated field for, kept in
// input order so the details view can still show it.
type Property struct {
	Key   string
	Value string
}

// Node is one step of the execution plan.
type Node struct {
	NodeType string // e.g. "Seq Scan", "Hash Join"

	// ParentRelationship is how the node relates to its parent: "Outer",
	// "Inner", "Subquery", "Member", "InitPlan" or "SubPlan".
	ParentRelationship string
	// SubplanName names an InitPlan, SubPlan or CTE root, e.g. "CTE big"
	// or "InitPlan 1 (returns $0)".
	SubplanName string

	JoinType      string
	Strategy      string
	PartialMode   string
	Operation     string
	ScanDirection string
	ParallelAware bool

	RelationName string
	Schema       string
	Alias        string
	IndexName    string
	CTEName      string
	FunctionName string

	// Planner estimates.
	StartupCost float64
	TotalCost   float64
	PlanRows    float64
	PlanWidth   int

	// Actual is nil when the plan was produced without ANALYZE.
	Actual *Actual

	Filter      string
	JoinFilter  string
	IndexCond   string
	RecheckCond string
	HashCond    string
	MergeCond   string

	RowsRemovedByFilter       float64
	RowsRemovedByJoinFilter   float64
	RowsRemovedByIndexRecheck float64

	SortKey       []string
	GroupKey      []string
	SortMethod    string
	SortSpaceType string // "Memory" or "Disk"
	SortSpaceUsed int64  // kB

	HashBuckets         int
	OriginalHashBuckets int
	HashBatches         int
	OriginalHashBatches int
	PeakMemoryUsage     int64 // kB

	HeapFetches     int64
	ExactHeapBlocks int64
	LossyHeapBlocks int64

	WorkersPlanned  int
	WorkersLaunched int

	Buffers Buffers

	Properties []Property
	Children   []*Node

	// Computed after parsing; see Plan.computeMetrics. Values are in the
	// plan's Metric (milliseconds or cost units).
	Inclusive        float64 // this node and everything below it
	Exclusive        float64 // this node alone
	InclusivePercent float64 // of Plan.Total
	ExclusivePercent float64 // of Plan.Total
}

// Walk visits n and its descendants in depth-first pre-order. depth is 0 for
// n itself. Returning false from fn skips the children of that node.
func (n *Node) Walk(fn func(n *Node, depth int) bool) {
	n.walk(fn, 0)
}

func (n *Node) walk(fn func(*Node, int) bool, depth int) {
	if n == nil || !fn(n, depth) {
		return
	}
	for _, c := range n.Children {
		c.walk(fn, depth+1)
	}
}

// Property returns the value of an extra property by key.
func (n *Node) Property(key string) (string, bool) {
	for _, p := range n.Properties {
		if p.Key == key {
			return p.Value, true
		}
	}
	return "", false
}
