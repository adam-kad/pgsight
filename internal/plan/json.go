package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// jsonObject is a decoded JSON object that remembers key order, so unknown
// node properties can be shown in the order PostgreSQL printed them.
type jsonObject struct {
	keys   []string
	values map[string]any
}

func (o *jsonObject) get(key string) (any, bool) {
	v, ok := o.values[key]
	return v, ok
}

// decodeValue reads one JSON value. Objects become *jsonObject, arrays
// []any, numbers json.Number; strings, booleans and null are as usual.
// json.Decoder caps nesting depth, so the recursion is bounded.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := &jsonObject{values: map[string]any{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := kt.(string) // the decoder guarantees object keys are strings
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			if _, dup := obj.values[key]; !dup {
				obj.keys = append(obj.keys, key)
			}
			obj.values[key] = v
		}
		_, err := dec.Token() // closing '}'
		return obj, err
	case '[':
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token() // closing ']'
		return arr, err
	default:
		return nil, fmt.Errorf("unexpected %q", delim)
	}
}

func parseJSON(src source) (*Plan, error) {
	text := src.text()
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()

	v, err := decodeValue(dec)
	if err != nil {
		return nil, jsonSyntaxError(src, text, dec, err)
	}

	p := &Plan{Format: FormatJSON}
	if rest := strings.TrimSpace(text[dec.InputOffset():]); rest != "" {
		p.Warnings = append(p.Warnings, "input contains more than one plan; only the first one is shown")
	}

	top := v
	if arr, ok := v.([]any); ok {
		if len(arr) == 0 {
			return nil, &ParseError{Format: FormatJSON, Msg: "the JSON array is empty"}
		}
		if len(arr) > 1 {
			p.Warnings = append(p.Warnings, "input contains more than one plan; only the first one is shown")
		}
		top = arr[0]
	}
	obj, ok := top.(*jsonObject)
	if !ok {
		return nil, &ParseError{Format: FormatJSON, Msg: `expected an object with a "Plan" key, as printed by EXPLAIN (FORMAT JSON)`}
	}

	if rawRoot, ok := obj.get("Plan"); ok {
		rootObj, ok := rawRoot.(*jsonObject)
		if !ok {
			return nil, &ParseError{Format: FormatJSON, Msg: `"Plan" must be an object`}
		}
		if p.Root, err = jsonNode(rootObj, "Plan"); err != nil {
			return nil, err
		}
		if err := jsonStatement(p, obj); err != nil {
			return nil, err
		}
	} else if _, ok := obj.get("Node Type"); ok {
		// A bare plan node, e.g. copied out of a larger document.
		if p.Root, err = jsonNode(obj, "Plan"); err != nil {
			return nil, err
		}
	} else {
		return nil, &ParseError{Format: FormatJSON, Msg: `no "Plan" key found; is this the output of EXPLAIN (FORMAT JSON)?`}
	}

	p.finish()
	return p, nil
}

func jsonSyntaxError(src source, text string, dec *json.Decoder, err error) error {
	offset := dec.InputOffset()
	var syn *json.SyntaxError
	msg := err.Error()
	switch {
	case errors.As(err, &syn) && strings.HasPrefix(msg, "invalid character "):
		offset = syn.Offset
		msg = "unexpected character " + strings.TrimPrefix(msg, "invalid character ")
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		strings.Contains(msg, "unexpected end of JSON input"):
		offset = int64(len(text))
		msg = "unexpected end of input; the plan looks truncated"
	}
	if offset > int64(len(text)) {
		offset = int64(len(text))
	}
	line := strings.Count(text[:offset], "\n")
	return &ParseError{Format: FormatJSON, Line: src.lineNo(line), Msg: msg}
}

// jsonStatement reads the statement-level keys that sit next to "Plan".
func jsonStatement(p *Plan, obj *jsonObject) error {
	for _, key := range obj.keys {
		v := obj.values[key]
		switch key {
		case "Planning Time", "Execution Time":
			f, err := jsonFloat(v, key, "")
			if err != nil {
				return err
			}
			if key == "Planning Time" {
				p.PlanningTime = &f
			} else {
				p.ExecutionTime = &f
			}
		case "Planning":
			po, ok := v.(*jsonObject)
			if !ok {
				return fieldTypeError("", key, "an object", v)
			}
			var b Buffers
			for _, k := range po.keys {
				if dst := bufferField(&b, k); dst != nil {
					n, err := jsonFloat(po.values[k], k, "Planning")
					if err != nil {
						return err
					}
					*dst = int64(n)
				}
			}
			if !b.IsZero() {
				p.PlanningBuffers = &b
			}
		case "Triggers":
			arr, ok := v.([]any)
			if !ok {
				return fieldTypeError("", key, "an array", v)
			}
			for i, raw := range arr {
				t, err := jsonTrigger(raw, fmt.Sprintf("Triggers[%d]", i))
				if err != nil {
					return err
				}
				p.Triggers = append(p.Triggers, t)
			}
		case "JIT":
			jo, ok := v.(*jsonObject)
			if !ok {
				return fieldTypeError("", key, "an object", v)
			}
			p.JIT = jsonJIT(jo)
		case "Plan":
		default:
			p.Properties = append(p.Properties, Property{Key: key, Value: formatValue(v)})
		}
	}
	return nil
}

func jsonTrigger(raw any, path string) (Trigger, error) {
	obj, ok := raw.(*jsonObject)
	if !ok {
		return Trigger{}, &ParseError{Format: FormatJSON, Msg: path + " must be an object"}
	}
	var t Trigger
	for _, key := range obj.keys {
		v := obj.values[key]
		var err error
		switch key {
		case "Trigger Name":
			t.Name = formatValue(v)
		case "Relation":
			t.Relation = formatValue(v)
		case "Time":
			t.Time, err = jsonFloat(v, key, path)
		case "Calls":
			t.Calls, err = jsonFloat(v, key, path)
		}
		if err != nil {
			return Trigger{}, err
		}
	}
	return t, nil
}

func jsonJIT(obj *jsonObject) *JIT {
	j := &JIT{}
	for _, key := range obj.keys {
		v := obj.values[key]
		switch key {
		case "Functions":
			if f, err := jsonFloat(v, key, "JIT"); err == nil {
				j.Functions = int(f)
			}
		case "Timing":
			if t, ok := v.(*jsonObject); ok {
				if total, ok := t.get("Total"); ok {
					if f, err := jsonFloat(total, "Total", "JIT"); err == nil {
						j.Total = f
					}
				}
			}
			j.Properties = append(j.Properties, Property{Key: key, Value: formatValue(v)})
		default:
			j.Properties = append(j.Properties, Property{Key: key, Value: formatValue(v)})
		}
	}
	return j
}

// jsonNode converts one plan node and, recursively, its children. path is a
// human-readable location used in error messages.
func jsonNode(obj *jsonObject, path string) (*Node, error) {
	n := &Node{}
	actual := func() *Actual {
		if n.Actual == nil {
			n.Actual = &Actual{}
		}
		return n.Actual
	}

	for _, key := range obj.keys {
		v := obj.values[key]
		var err error

		if dst := stringField(n, key); dst != nil {
			*dst = formatValue(v)
			continue
		}
		if dst := intField(n, key); dst != nil {
			var f float64
			f, err = jsonFloat(v, key, path)
			*dst = int(f)
		} else if dst := int64Field(n, key); dst != nil {
			var f float64
			f, err = jsonFloat(v, key, path)
			*dst = int64(f)
		} else if dst := floatField(n, key); dst != nil {
			*dst, err = jsonFloat(v, key, path)
		} else if dst := bufferField(&n.Buffers, key); dst != nil {
			var f float64
			f, err = jsonFloat(v, key, path)
			*dst = int64(f)
		} else {
			switch key {
			case "Actual Startup Time":
				actual().StartupTime, err = jsonFloat(v, key, path)
			case "Actual Total Time":
				actual().TotalTime, err = jsonFloat(v, key, path)
			case "Actual Rows":
				actual().Rows, err = jsonFloat(v, key, path)
			case "Actual Loops":
				actual().Loops, err = jsonFloat(v, key, path)
			case "Parallel Aware":
				b, ok := v.(bool)
				if !ok {
					return nil, fieldTypeError(path, key, "a boolean", v)
				}
				n.ParallelAware = b
			case "Sort Key":
				n.SortKey = stringList(v)
			case "Group Key":
				n.GroupKey = stringList(v)
			case "Workers":
				n.Properties = append(n.Properties, workerProperties(v)...)
			case "Plans":
				arr, ok := v.([]any)
				if !ok {
					return nil, fieldTypeError(path, key, "an array", v)
				}
				for i, raw := range arr {
					childPath := fmt.Sprintf("%s → Plans[%d]", path, i)
					co, ok := raw.(*jsonObject)
					if !ok {
						return nil, &ParseError{Format: FormatJSON, Msg: childPath + " must be an object"}
					}
					child, err := jsonNode(co, childPath)
					if err != nil {
						return nil, err
					}
					n.Children = append(n.Children, child)
				}
			default:
				n.Properties = append(n.Properties, Property{Key: key, Value: formatValue(v)})
			}
		}
		if err != nil {
			return nil, err
		}
	}

	if n.NodeType == "" {
		return nil, &ParseError{Format: FormatJSON, Msg: path + `: node has no "Node Type"`}
	}
	return n, nil
}

func stringField(n *Node, key string) *string {
	switch key {
	case "Node Type":
		return &n.NodeType
	case "Parent Relationship":
		return &n.ParentRelationship
	case "Subplan Name":
		return &n.SubplanName
	case "Join Type":
		return &n.JoinType
	case "Strategy":
		return &n.Strategy
	case "Partial Mode":
		return &n.PartialMode
	case "Operation":
		return &n.Operation
	case "Scan Direction":
		return &n.ScanDirection
	case "Relation Name":
		return &n.RelationName
	case "Schema":
		return &n.Schema
	case "Alias":
		return &n.Alias
	case "Index Name":
		return &n.IndexName
	case "CTE Name":
		return &n.CTEName
	case "Function Name":
		return &n.FunctionName
	case "Filter":
		return &n.Filter
	case "Join Filter":
		return &n.JoinFilter
	case "Index Cond":
		return &n.IndexCond
	case "Recheck Cond":
		return &n.RecheckCond
	case "Hash Cond":
		return &n.HashCond
	case "Merge Cond":
		return &n.MergeCond
	case "Sort Method":
		return &n.SortMethod
	case "Sort Space Type":
		return &n.SortSpaceType
	}
	return nil
}

func floatField(n *Node, key string) *float64 {
	switch key {
	case "Startup Cost":
		return &n.StartupCost
	case "Total Cost":
		return &n.TotalCost
	case "Plan Rows":
		return &n.PlanRows
	case "Rows Removed by Filter":
		return &n.RowsRemovedByFilter
	case "Rows Removed by Join Filter":
		return &n.RowsRemovedByJoinFilter
	case "Rows Removed by Index Recheck":
		return &n.RowsRemovedByIndexRecheck
	}
	return nil
}

func intField(n *Node, key string) *int {
	switch key {
	case "Plan Width":
		return &n.PlanWidth
	case "Hash Buckets":
		return &n.HashBuckets
	case "Original Hash Buckets":
		return &n.OriginalHashBuckets
	case "Hash Batches":
		return &n.HashBatches
	case "Original Hash Batches":
		return &n.OriginalHashBatches
	case "Workers Planned":
		return &n.WorkersPlanned
	case "Workers Launched":
		return &n.WorkersLaunched
	}
	return nil
}

func int64Field(n *Node, key string) *int64 {
	switch key {
	case "Sort Space Used":
		return &n.SortSpaceUsed
	case "Peak Memory Usage":
		return &n.PeakMemoryUsage
	case "Heap Fetches":
		return &n.HeapFetches
	case "Exact Heap Blocks":
		return &n.ExactHeapBlocks
	case "Lossy Heap Blocks":
		return &n.LossyHeapBlocks
	}
	return nil
}

func bufferField(b *Buffers, key string) *int64 {
	switch key {
	case "Shared Hit Blocks":
		return &b.SharedHit
	case "Shared Read Blocks":
		return &b.SharedRead
	case "Shared Dirtied Blocks":
		return &b.SharedDirtied
	case "Shared Written Blocks":
		return &b.SharedWritten
	case "Local Hit Blocks":
		return &b.LocalHit
	case "Local Read Blocks":
		return &b.LocalRead
	case "Local Dirtied Blocks":
		return &b.LocalDirtied
	case "Local Written Blocks":
		return &b.LocalWritten
	case "Temp Read Blocks":
		return &b.TempRead
	case "Temp Written Blocks":
		return &b.TempWritten
	}
	return nil
}

// workerProperties flattens per-worker details ("Workers": [{...}]) into
// one property per worker, e.g. "Worker 0" → "Sort Method: quicksort, ...".
func workerProperties(v any) []Property {
	arr, ok := v.([]any)
	if !ok {
		return []Property{{Key: "Workers", Value: formatValue(v)}}
	}
	var out []Property
	for i, raw := range arr {
		key := fmt.Sprintf("Worker %d", i)
		obj, ok := raw.(*jsonObject)
		if !ok {
			out = append(out, Property{Key: key, Value: formatValue(raw)})
			continue
		}
		var parts []string
		for _, k := range obj.keys {
			if k == "Worker Number" {
				key = "Worker " + formatValue(obj.values[k])
				continue
			}
			parts = append(parts, k+": "+formatValue(obj.values[k]))
		}
		out = append(out, Property{Key: key, Value: strings.Join(parts, ", ")})
	}
	return out
}

func jsonFloat(v any, key, path string) (float64, error) {
	switch t := v.(type) {
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f, nil
		}
	case string:
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f, nil
		}
	}
	return 0, fieldTypeError(path, key, "a number", v)
}

func fieldTypeError(path, key, want string, got any) error {
	loc := fmt.Sprintf("%q", key)
	if path != "" {
		loc = path + " → " + loc
	}
	return &ParseError{Format: FormatJSON, Msg: fmt.Sprintf("%s must be %s, got %s", loc, want, describe(got))}
}

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("the string %q", v)
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case []any:
		return "an array"
	case *jsonObject:
		return "an object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, formatValue(e))
		}
		return out
	case nil:
		return nil
	default:
		return []string{formatValue(v)}
	}
}

// formatValue renders any decoded JSON value as display text.
func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = formatValue(e)
		}
		return strings.Join(parts, ", ")
	case *jsonObject:
		parts := make([]string, len(t.keys))
		for i, k := range t.keys {
			parts[i] = k + ": " + formatValue(t.values[k])
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}
