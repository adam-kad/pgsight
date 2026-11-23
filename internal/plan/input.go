package plan

import (
	"regexp"
	"strings"
)

var (
	separatorRe = regexp.MustCompile(`^-+$`)
	rowCountRe  = regexp.MustCompile(`^\(\d+ rows?\)$`)
)

// source is EXPLAIN output with the psql decoration removed.
type source struct {
	lines []string
	// firstLine is the 1-based line number of lines[0] in the original
	// input, so errors can point at what the user actually pasted.
	firstLine int
}

func (s source) text() string { return strings.Join(s.lines, "\n") }

// lineNo maps an index into s.lines to a line number in the original input.
func (s source) lineNo(i int) int { return s.firstLine + i }

// clean strips what psql wraps around a plan so both parsers see bare
// EXPLAIN output:
//
//   - the "QUERY PLAN" header and the dashed separator under it;
//   - the "(N rows)" footer;
//   - the " +" that aligned output appends to every line of a multi-line
//     value (this is how JSON plans look in psql);
//   - the indentation shared by all lines and trailing padding.
//
// Input that has none of this passes through unchanged apart from line
// endings and trailing whitespace.
func clean(raw string) source {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	lines := strings.Split(raw, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}

	first := 0
	for first < len(lines) && lines[first] == "" {
		first++
	}
	if first+1 < len(lines) &&
		strings.TrimSpace(lines[first]) == "QUERY PLAN" &&
		separatorRe.MatchString(strings.TrimSpace(lines[first+1])) {
		first += 2
	}

	last := len(lines)
	for last > first && lines[last-1] == "" {
		last--
	}
	if last > first && rowCountRe.MatchString(strings.TrimSpace(lines[last-1])) {
		last--
		for last > first && lines[last-1] == "" {
			last--
		}
	}

	src := source{lines: lines[first:last], firstLine: first + 1}
	stripContinuations(src.lines)
	dedent(src.lines)
	return src
}

// stripContinuations removes psql's "+" line-continuation markers. They are
// only removed when every line but the last carries one, so a plan line
// that happens to end in "+" is left alone.
func stripContinuations(lines []string) {
	nonEmpty := 0
	for i, l := range lines {
		if l == "" {
			continue
		}
		nonEmpty++
		if i < len(lines)-1 && !strings.HasSuffix(l, "+") {
			return
		}
	}
	if nonEmpty < 2 {
		return
	}
	for i, l := range lines {
		if strings.HasSuffix(l, "+") {
			lines[i] = strings.TrimRight(strings.TrimSuffix(l, "+"), " \t")
		}
	}
}

// dedent removes the leading whitespace common to all non-empty lines.
func dedent(lines []string) {
	indent := -1
	for _, l := range lines {
		if l == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent <= 0 {
		return
	}
	for i, l := range lines {
		if l != "" {
			lines[i] = l[indent:]
		}
	}
}

// detect guesses the format of cleaned EXPLAIN output.
func detect(src source) Format {
	for _, l := range src.lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if t[0] == '[' || t[0] == '{' {
			return FormatJSON
		}
		return FormatText
	}
	return FormatUnknown
}
