package plan

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	costRe     = regexp.MustCompile(`\s*\(cost=([\d.]+)\.\.([\d.]+) rows=([\d.]+) width=(\d+)\)$`)
	actualRe   = regexp.MustCompile(`\s*\(actual(?: time=([\d.]+)\.\.([\d.]+))? rows=([\d.]+) loops=([\d.]+)\)$`)
	neverRe    = regexp.MustCompile(`\s*\(never executed\)$`)
	labelRe    = regexp.MustCompile(`^(?:InitPlan \d+(?: \(returns [^)]*\))?|SubPlan \d+|CTE .+)$`)
	triggerRe  = regexp.MustCompile(`^Trigger (.+?)(?: on (\S+))?: time=([\d.]+) calls=([\d.]+)$`)
	msRe       = regexp.MustCompile(`^([\d.]+) ms$`)
	jitTotalRe = regexp.MustCompile(`Total ([\d.]+) ms`)
	kbRe       = regexp.MustCompile(`^(\d+)kB$`)
	originalRe = regexp.MustCompile(`^(\d+)(?: \(originally (\d+)\))?$`)
)

// textEntry is an element of the indentation stack: either a plan node or
// an "InitPlan 1" / "SubPlan 2" / "CTE x" label whose single child is the
// root of that subplan.
type textEntry struct {
	col   int // column where the node name or label starts
	node  *Node
	label string
	owner *Node // for labels: the node the subplan belongs to
}

// textParser builds a Plan from text-format EXPLAIN output.
//
// The format nests by indentation: every line belongs to the closest
// preceding node (or label) whose name starts at a smaller column. Child
// nodes are marked with "->", properties are "Key: value" lines, and the
// statement summary ("Planning Time", "Execution Time", triggers, JIT)
// follows the tree at column 0.
type textParser struct {
	src       source
	plan      *Plan
	stack     []textEntry
	rootCol   int
	inSummary bool   // the tree is finished; only summary lines follow
	section   string // "Planning" or "JIT" while inside those summary blocks
}

func parseText(src source) (*Plan, error) {
	tp := &textParser{src: src, plan: &Plan{Format: FormatText}}
	if err := tp.run(); err != nil {
		return nil, err
	}
	tp.plan.finish()
	return tp.plan, nil
}

func (tp *textParser) errorf(i int, format string, args ...any) error {
	return &ParseError{Format: FormatText, Line: tp.src.lineNo(i), Msg: fmt.Sprintf(format, args...)}
}

func (tp *textParser) run() error {
	for i, line := range tp.src.lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		content := line[indent:]

		if tp.plan.Root == nil {
			n, ok := parseNodeLine(content, true)
			if !ok {
				return tp.errorf(i, "expected a plan node such as \"Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)\", got %q", truncate(content, 60))
			}
			tp.plan.Root = n
			tp.rootCol = indent
			tp.stack = []textEntry{{col: indent, node: n}}
			continue
		}

		if indent <= tp.rootCol {
			tp.inSummary = true
			if stop := tp.summaryLine(content); stop {
				return nil
			}
			continue
		}
		if tp.inSummary {
			if tp.section != "" {
				tp.sectionLine(content)
			} else {
				tp.addPlanProperty(content)
			}
			continue
		}

		for len(tp.stack) > 0 && tp.stack[len(tp.stack)-1].col >= indent {
			tp.stack = tp.stack[:len(tp.stack)-1]
		}
		if len(tp.stack) == 0 {
			return tp.errorf(i, "indentation does not match any plan node")
		}
		parent := tp.stack[len(tp.stack)-1]

		switch {
		case strings.HasPrefix(content, "->"):
			text := strings.TrimLeft(content[2:], " ")
			n, ok := parseNodeLine(text, false)
			if !ok {
				return tp.errorf(i, "expected a plan node after \"->\"")
			}
			if parent.node == nil {
				n.SubplanName = parent.label
				n.ParentRelationship = "InitPlan"
				if strings.HasPrefix(parent.label, "SubPlan") {
					n.ParentRelationship = "SubPlan"
				}
				parent.owner.Children = append(parent.owner.Children, n)
			} else {
				parent.node.Children = append(parent.node.Children, n)
			}
			tp.stack = append(tp.stack, textEntry{col: indent + len(content) - len(text), node: n})
		case labelRe.MatchString(content) && parent.node != nil:
			tp.stack = append(tp.stack, textEntry{col: indent, label: content, owner: parent.node})
		default:
			target := parent.node
			if target == nil {
				target = parent.owner
			}
			applyTextProperty(target, content)
		}
	}
	return nil
}

// summaryLine handles a line at the root's indentation that follows the
// tree. It reports true when parsing should stop (a second plan begins).
func (tp *textParser) summaryLine(content string) (stop bool) {
	tp.section = ""

	key, value := splitProperty(content)
	switch {
	case content == "Planning:":
		tp.section = "Planning"
	case content == "JIT:":
		tp.section = "JIT"
		tp.plan.JIT = &JIT{}
	case strings.EqualFold(key, "Planning Time"):
		if f, ok := parseMs(value); ok {
			tp.plan.PlanningTime = &f
		}
	case strings.EqualFold(key, "Execution Time") || strings.EqualFold(key, "Total runtime"):
		if f, ok := parseMs(value); ok {
			tp.plan.ExecutionTime = &f
		}
	case triggerRe.MatchString(content):
		m := triggerRe.FindStringSubmatch(content)
		t := Trigger{Name: m[1], Relation: m[2]}
		t.Time, _ = strconv.ParseFloat(m[3], 64)
		t.Calls, _ = strconv.ParseFloat(m[4], 64)
		tp.plan.Triggers = append(tp.plan.Triggers, t)
	default:
		if _, ok := parseNodeLine(content, true); ok {
			tp.plan.Warnings = append(tp.plan.Warnings, "input contains more than one plan; only the first one is shown")
			return true
		}
		tp.addPlanProperty(content)
	}
	return false
}

func (tp *textParser) sectionLine(content string) {
	key, value := splitProperty(content)
	switch tp.section {
	case "Planning":
		if key == "Buffers" {
			var b Buffers
			parseBuffers(&b, value)
			tp.plan.PlanningBuffers = &b
			return
		}
		tp.plan.Properties = append(tp.plan.Properties, Property{Key: "Planning " + key, Value: value})
	case "JIT":
		switch key {
		case "Functions":
			n, _ := strconv.Atoi(value)
			tp.plan.JIT.Functions = n
			return
		case "Timing":
			if m := jitTotalRe.FindStringSubmatch(value); m != nil {
				tp.plan.JIT.Total, _ = strconv.ParseFloat(m[1], 64)
			}
		}
		tp.plan.JIT.Properties = append(tp.plan.JIT.Properties, Property{Key: key, Value: value})
	}
}

func (tp *textParser) addPlanProperty(content string) {
	key, value := splitProperty(content)
	tp.plan.Properties = append(tp.plan.Properties, Property{Key: key, Value: value})
}

// parseNodeLine parses "Name  (cost=...) (actual ...)". In strict mode a
// line without any parenthesised statistics is accepted only if it starts
// with a known node name, so that "Planning Time: 1 ms" or random text is
// not mistaken for a node.
func parseNodeLine(s string, strict bool) (*Node, bool) {
	n := &Node{}
	hasStats := false

	if m := actualRe.FindStringSubmatchIndex(s); m != nil {
		a := &Actual{}
		if m[2] >= 0 {
			a.StartupTime, _ = strconv.ParseFloat(s[m[2]:m[3]], 64)
			a.TotalTime, _ = strconv.ParseFloat(s[m[4]:m[5]], 64)
		}
		a.Rows, _ = strconv.ParseFloat(s[m[6]:m[7]], 64)
		a.Loops, _ = strconv.ParseFloat(s[m[8]:m[9]], 64)
		n.Actual = a
		s = s[:m[0]]
		hasStats = true
	} else if loc := neverRe.FindStringIndex(s); loc != nil {
		n.Actual = &Actual{}
		s = s[:loc[0]]
		hasStats = true
	}

	if m := costRe.FindStringSubmatch(s); m != nil {
		n.StartupCost, _ = strconv.ParseFloat(m[1], 64)
		n.TotalCost, _ = strconv.ParseFloat(m[2], 64)
		n.PlanRows, _ = strconv.ParseFloat(m[3], 64)
		n.PlanWidth, _ = strconv.Atoi(m[4])
		s = s[:len(s)-len(m[0])]
		hasStats = true
	}

	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ":") {
		return nil, false
	}
	if strict && !hasStats && !looksLikeNodeName(s) {
		return nil, false
	}
	applyTextName(n, s)
	return n, true
}

func looksLikeNodeName(s string) bool {
	if strings.Contains(s, ": ") {
		return false
	}
	name, _, _ := splitTarget(s)
	for _, p := range []string{"Parallel ", "Async ", "Partial ", "Finalize "} {
		name = strings.TrimPrefix(name, p)
	}
	name = strings.TrimSuffix(name, " Backward")
	if knownNodeTypes[name] || isJoinName(name) || strings.HasPrefix(name, "Custom Scan (") {
		return true
	}
	first, _, _ := strings.Cut(name, " ")
	return first == "SetOp" || first == "HashSetOp"
}

// splitProperty splits "Key: value". A line without ": " is all key.
func splitProperty(s string) (key, value string) {
	if k, v, ok := strings.Cut(s, ": "); ok {
		return strings.TrimSpace(k), strings.TrimSpace(v)
	}
	return strings.TrimSuffix(s, ":"), ""
}

// applyTextProperty stores one "Key: value" line on a node.
func applyTextProperty(n *Node, content string) {
	key, value := splitProperty(content)

	// Lines that pack several "Key: value" pairs separated by two spaces.
	switch key {
	case "Sort Method", "Buckets", "Batches", "Hits":
		for _, seg := range strings.Split(content, "  ") {
			if seg = strings.TrimSpace(seg); seg != "" {
				k, v := splitProperty(seg)
				applyTextSegment(n, k, v)
			}
		}
		return
	}
	if dst := stringField(n, key); dst != nil && key != "Node Type" {
		*dst = value
		return
	}
	if dst := floatField(n, key); dst != nil {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			*dst = f
			return
		}
	}
	if dst := intField(n, key); dst != nil {
		if v, err := strconv.Atoi(value); err == nil {
			*dst = v
			return
		}
	}
	switch key {
	case "Sort Key":
		n.SortKey = splitList(value)
		return
	case "Group Key":
		n.GroupKey = splitList(value)
		return
	case "Buffers":
		parseBuffers(&n.Buffers, value)
		return
	case "Heap Fetches":
		if v, err := strconv.ParseInt(value, 10, 64); err == nil {
			n.HeapFetches = v
			return
		}
	case "Heap Blocks":
		for _, f := range strings.Fields(value) {
			k, v, _ := strings.Cut(f, "=")
			num, _ := strconv.ParseInt(v, 10, 64)
			switch k {
			case "exact":
				n.ExactHeapBlocks = num
			case "lossy":
				n.LossyHeapBlocks = num
			}
		}
		return
	}
	n.Properties = append(n.Properties, Property{Key: key, Value: value})
}

func applyTextSegment(n *Node, key, value string) {
	switch key {
	case "Sort Method":
		n.SortMethod = value
	case "Memory", "Disk":
		if m := kbRe.FindStringSubmatch(value); m != nil && n.SortMethod != "" {
			n.SortSpaceType = key
			n.SortSpaceUsed, _ = strconv.ParseInt(m[1], 10, 64)
			return
		}
		n.Properties = append(n.Properties, Property{Key: key, Value: value})
	case "Buckets", "Batches":
		m := originalRe.FindStringSubmatch(value)
		if m == nil {
			n.Properties = append(n.Properties, Property{Key: key, Value: value})
			return
		}
		cur, _ := strconv.Atoi(m[1])
		orig := cur
		if m[2] != "" {
			orig, _ = strconv.Atoi(m[2])
		}
		switch {
		case key == "Buckets":
			n.HashBuckets, n.OriginalHashBuckets = cur, orig
		case n.NodeType == "Hash":
			n.HashBatches, n.OriginalHashBatches = cur, orig
		default:
			// HashAggregate spill batches; named as in the JSON format.
			n.Properties = append(n.Properties, Property{Key: "HashAgg Batches", Value: m[1]})
		}
	case "Memory Usage":
		if m := kbRe.FindStringSubmatch(value); m != nil {
			n.PeakMemoryUsage, _ = strconv.ParseInt(m[1], 10, 64)
			return
		}
		n.Properties = append(n.Properties, Property{Key: key, Value: value})
	default:
		n.Properties = append(n.Properties, Property{Key: key, Value: value})
	}
}

// parseBuffers reads "shared hit=1 read=2, local ..., temp read=3 written=4".
func parseBuffers(b *Buffers, s string) {
	for _, group := range strings.Split(s, ",") {
		fields := strings.Fields(group)
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			k, v, _ := strings.Cut(f, "=")
			num, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				continue
			}
			if dst := bufferCounter(b, fields[0], k); dst != nil {
				*dst = num
			}
		}
	}
}

func bufferCounter(b *Buffers, kind, counter string) *int64 {
	switch kind + " " + counter {
	case "shared hit":
		return &b.SharedHit
	case "shared read":
		return &b.SharedRead
	case "shared dirtied":
		return &b.SharedDirtied
	case "shared written":
		return &b.SharedWritten
	case "local hit":
		return &b.LocalHit
	case "local read":
		return &b.LocalRead
	case "local dirtied":
		return &b.LocalDirtied
	case "local written":
		return &b.LocalWritten
	case "temp read":
		return &b.TempRead
	case "temp written":
		return &b.TempWritten
	}
	return nil
}

// splitList splits "a, f(b, c) DESC, 'x, y'" on top-level commas.
func splitList(s string) []string {
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if last := strings.TrimSpace(s[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

func parseMs(s string) (float64, bool) {
	m := msRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	return f, err == nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
