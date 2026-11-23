package plan

import "strings"

// knownNodeTypes lists node names as PostgreSQL prints them in text format
// (after "Parallel "/"Async "/"Partial "/"Finalize " prefixes are removed).
// It is used to recognise plan lines that carry no "(cost=...)" suffix,
// i.e. output of EXPLAIN (COSTS OFF).
var knownNodeTypes = map[string]bool{
	"Result": true, "ProjectSet": true, "Insert": true, "Update": true,
	"Delete": true, "Merge": true, "Append": true, "Merge Append": true,
	"Recursive Union": true, "BitmapAnd": true, "BitmapOr": true,
	"Nested Loop": true, "Merge Join": true, "Hash Join": true,
	"Seq Scan": true, "Sample Scan": true, "Gather": true, "Gather Merge": true,
	"Index Scan": true, "Index Only Scan": true, "Bitmap Index Scan": true,
	"Bitmap Heap Scan": true, "Tid Scan": true, "Tid Range Scan": true,
	"Subquery Scan": true, "Function Scan": true, "Table Function Scan": true,
	"Values Scan": true, "CTE Scan": true, "Named Tuplestore Scan": true,
	"WorkTable Scan": true, "Foreign Scan": true, "Custom Scan": true,
	"Materialize": true, "Memoize": true, "Sort": true, "Incremental Sort": true,
	"Group": true, "Aggregate": true, "GroupAggregate": true,
	"HashAggregate": true, "MixedAggregate": true, "WindowAgg": true,
	"Unique": true, "SetOp": true, "HashSetOp": true, "LockRows": true,
	"Limit": true, "Hash": true,
}

var aggregateStrategies = map[string]string{
	"Aggregate":      "Plain",
	"GroupAggregate": "Sorted",
	"HashAggregate":  "Hashed",
	"MixedAggregate": "Mixed",
}

var joinTypes = []string{"Right Anti", "Right Semi", "Left", "Right", "Full", "Semi", "Anti"}

// applyTextName fills a node from the name part of a text-format plan line,
// e.g. "Parallel Index Scan Backward using idx on public.orders o", using the
// same conventions as the JSON format (NodeType "Aggregate" + Strategy
// "Hashed" for "HashAggregate", NodeType "ModifyTable" + Operation "Update"
// for "Update on t", and so on).
func applyTextName(n *Node, full string) {
	name, using, target := splitTarget(full)

	for {
		switch {
		case strings.HasPrefix(name, "Parallel "):
			n.ParallelAware = true
			name = strings.TrimPrefix(name, "Parallel ")
			continue
		case strings.HasPrefix(name, "Async "):
			n.Properties = append(n.Properties, Property{Key: "Async Capable", Value: "true"})
			name = strings.TrimPrefix(name, "Async ")
			continue
		}
		break
	}

	for _, mode := range []string{"Partial", "Finalize"} {
		if rest, ok := strings.CutPrefix(name, mode+" "); ok {
			if _, isAgg := aggregateStrategies[rest]; isAgg {
				n.PartialMode = mode
				name = rest
			}
		}
	}

	switch {
	case aggregateStrategies[name] != "":
		n.NodeType = "Aggregate"
		n.Strategy = aggregateStrategies[name]
		if n.PartialMode == "" {
			n.PartialMode = "Simple"
		}
	case isJoinName(name):
		n.NodeType, n.JoinType = splitJoin(name)
	case name == "Insert" || name == "Update" || name == "Delete" || name == "Merge":
		n.NodeType = "ModifyTable"
		n.Operation = name
	case strings.HasPrefix(name, "SetOp ") || strings.HasPrefix(name, "HashSetOp "):
		strategy, command, _ := strings.Cut(name, " ")
		n.NodeType = "SetOp"
		n.Strategy = "Sorted"
		if strategy == "HashSetOp" {
			n.Strategy = "Hashed"
		}
		n.Properties = append(n.Properties, Property{Key: "Command", Value: command})
	case strings.HasPrefix(name, "Custom Scan (") && strings.HasSuffix(name, ")"):
		n.NodeType = "Custom Scan"
		provider := strings.TrimSuffix(strings.TrimPrefix(name, "Custom Scan ("), ")")
		n.Properties = append(n.Properties, Property{Key: "Custom Plan Provider", Value: provider})
	default:
		if base, ok := strings.CutSuffix(name, " Backward"); ok {
			n.ScanDirection = "Backward"
			name = base
		} else if name == "Index Scan" || name == "Index Only Scan" {
			n.ScanDirection = "Forward"
		}
		n.NodeType = name
	}

	if using != "" {
		n.IndexName = unquoteIdent(using)
	}
	if target == "" {
		return
	}
	words := splitIdents(target)
	object := words[0]
	alias := ""
	if len(words) > 1 {
		alias = words[len(words)-1]
	}
	switch n.NodeType {
	case "Bitmap Index Scan":
		n.IndexName = object
	case "CTE Scan", "WorkTable Scan":
		n.CTEName = object
		n.Alias = firstNonEmpty(alias, object)
	case "Function Scan":
		n.FunctionName = object
		n.Alias = firstNonEmpty(alias, object)
	case "Subquery Scan", "Values Scan", "Table Function Scan", "Named Tuplestore Scan":
		n.Alias = firstNonEmpty(alias, object)
	default:
		if schema, rel, ok := strings.Cut(object, "."); ok && !strings.Contains(object, `"`) {
			n.Schema, object = schema, rel
		}
		n.RelationName = object
		n.Alias = firstNonEmpty(alias, object)
	}
}

// splitTarget splits "Index Scan using idx on t a" into the node name, the
// index and the target ("t a").
func splitTarget(s string) (name, using, target string) {
	if before, after, ok := strings.Cut(s, " using "); ok {
		idx, tgt, _ := strings.Cut(after, " on ")
		return before, idx, tgt
	}
	if before, after, ok := strings.Cut(s, " on "); ok {
		return before, "", after
	}
	return s, "", ""
}

func isJoinName(name string) bool {
	if name == "Nested Loop" || strings.HasPrefix(name, "Nested Loop ") {
		return true
	}
	return (strings.HasPrefix(name, "Hash ") || strings.HasPrefix(name, "Merge ")) &&
		strings.HasSuffix(name, " Join")
}

// splitJoin turns "Hash Left Join" into ("Hash Join", "Left").
func splitJoin(name string) (nodeType, joinType string) {
	base := "Nested Loop"
	rest := strings.TrimPrefix(name, "Nested Loop")
	if !strings.HasPrefix(name, "Nested Loop") {
		var kind string
		kind, rest, _ = strings.Cut(name, " ")
		base = kind + " Join"
	}
	rest = strings.TrimSpace(strings.TrimSuffix(rest, "Join"))
	for _, jt := range joinTypes {
		if rest == jt {
			return base, jt
		}
	}
	return base, "Inner"
}

// splitIdents splits a space-separated list of possibly double-quoted
// identifiers and unquotes them.
func splitIdents(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			cur.WriteByte(c)
			if inQuote && i+1 < len(s) && s[i+1] == '"' {
				cur.WriteByte('"')
				i++
				continue
			}
			inQuote = !inQuote
		case c == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, unquoteIdent(cur.String()))
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, unquoteIdent(cur.String()))
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Title is the node name as PostgreSQL prints it in text format, e.g.
// "Parallel Seq Scan", "Hash Left Join", "Finalize HashAggregate", "Update".
func (n *Node) Title() string {
	name := n.NodeType
	switch name {
	case "Aggregate":
		switch n.Strategy {
		case "Sorted":
			name = "GroupAggregate"
		case "Hashed":
			name = "HashAggregate"
		case "Mixed":
			name = "MixedAggregate"
		}
		if n.PartialMode == "Partial" || n.PartialMode == "Finalize" {
			name = n.PartialMode + " " + name
		}
	case "Hash Join", "Merge Join", "Nested Loop":
		if n.JoinType != "" && n.JoinType != "Inner" {
			name = strings.TrimSuffix(name, " Join") + " " + n.JoinType + " Join"
		}
	case "ModifyTable":
		if n.Operation != "" {
			name = n.Operation
		}
	case "SetOp":
		if n.Strategy == "Hashed" {
			name = "HashSetOp"
		}
		if cmd, ok := n.Property("Command"); ok {
			name += " " + cmd
		}
	case "Custom Scan":
		if p, ok := n.Property("Custom Plan Provider"); ok {
			name += " (" + p + ")"
		}
	}
	if n.ScanDirection == "Backward" {
		name += " Backward"
	}
	if n.ParallelAware {
		name = "Parallel " + name
	}
	return name
}

// Target describes what the node reads, as in "using idx on orders o";
// empty for nodes that do not scan a named object.
func (n *Node) Target() string {
	var object string
	switch {
	case n.NodeType == "Bitmap Index Scan":
		return "on " + n.IndexName
	case n.RelationName != "":
		object = n.RelationName
		if n.Schema != "" {
			object = n.Schema + "." + object
		}
	case n.CTEName != "":
		object = n.CTEName
	case n.FunctionName != "":
		object = n.FunctionName
	case n.Alias != "":
		object = n.Alias
	default:
		return ""
	}
	s := "on " + object
	if n.Alias != "" && n.Alias != object && n.Alias != n.RelationName {
		s += " " + n.Alias
	}
	if n.IndexName != "" {
		s = "using " + n.IndexName + " " + s
	}
	return s
}

// Label is Title plus Target: the full one-line description of the node.
func (n *Node) Label() string {
	if t := n.Target(); t != "" {
		return n.Title() + " " + t
	}
	return n.Title()
}
