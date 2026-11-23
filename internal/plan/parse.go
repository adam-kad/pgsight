package plan

import (
	"errors"
	"fmt"
	"io"
)

// MaxInputSize caps how much input Parse reads. Real plans are far smaller;
// the limit only protects against piping in something that is not a plan.
const MaxInputSize = 64 << 20

// ErrEmpty is returned when the input contains no plan at all.
var ErrEmpty = errors.New("input is empty: expected the output of EXPLAIN or EXPLAIN ANALYZE")

// errTextUnsupported is temporary until the text parser lands.
var errTextUnsupported = errors.New("text EXPLAIN format is not supported yet; use EXPLAIN (FORMAT JSON)")

// ParseError describes malformed input. Line is 1-based and refers to the
// input as given, including any psql header; it is 0 when unknown.
type ParseError struct {
	Format Format
	Line   int
	Msg    string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("invalid %s plan at line %d: %s", e.Format, e.Line, e.Msg)
	}
	return fmt.Sprintf("invalid %s plan: %s", e.Format, e.Msg)
}

// Parse reads EXPLAIN output in text or JSON format, with or without the
// decoration psql adds, and builds the plan tree. The format is detected
// automatically.
func Parse(r io.Reader) (*Plan, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxInputSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading plan: %w", err)
	}
	if len(data) > MaxInputSize {
		return nil, fmt.Errorf("input is larger than %d MiB; is it really an EXPLAIN plan?", MaxInputSize>>20)
	}
	return ParseString(string(data))
}

// ParseString is Parse for input already in memory.
func ParseString(input string) (*Plan, error) {
	src := clean(input)
	switch detect(src) {
	case FormatJSON:
		return parseJSON(src)
	case FormatText:
		return nil, errTextUnsupported
	default:
		return nil, ErrEmpty
	}
}

// finish fills in plan-wide fields derived from the node tree.
func (p *Plan) finish() {
	for _, n := range p.Nodes() {
		if n.Actual != nil {
			p.Analyzed = true
			return
		}
	}
}
