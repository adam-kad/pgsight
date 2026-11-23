package plan

import (
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      string
		firstLine int
	}{
		{
			name: "psql aligned text output",
			in: `                          QUERY PLAN
--------------------------------------------------------------
 Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)
   Filter: (a = 1)
 Planning Time: 0.1 ms
(3 rows)

`,
			want:      "Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)\n  Filter: (a = 1)\nPlanning Time: 0.1 ms",
			firstLine: 3,
		},
		{
			name: "psql aligned json output with continuation markers",
			in: `      QUERY PLAN
----------------------
 [                   +
   {                 +
     "Plan": {}      +
   }                 +
 ]
(1 row)
`,
			want:      "[\n  {\n    \"Plan\": {}\n  }\n]",
			firstLine: 3,
		},
		{
			name:      "bare output passes through",
			in:        "Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)\n  Filter: (a = 1)\n",
			want:      "Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)\n  Filter: (a = 1)",
			firstLine: 1,
		},
		{
			name:      "windows line endings, BOM and leading blank lines",
			in:        "\uFEFF\r\n\r\n QUERY PLAN\r\n------\r\n Result  (cost=0.00..0.01 rows=1 width=4)\r\n(1 row)\r\n",
			want:      "Result  (cost=0.00..0.01 rows=1 width=4)",
			firstLine: 5,
		},
		{
			name:      "a single trailing plus is not a continuation marker",
			in:        "Result\n  Filter: (a = b +",
			want:      "Result\n  Filter: (a = b +",
			firstLine: 1,
		},
		{
			name:      "empty",
			in:        "  \n\n",
			want:      "",
			firstLine: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := clean(tt.in)
			if got := src.text(); got != tt.want {
				t.Errorf("text:\n got %q\nwant %q", got, tt.want)
			}
			if tt.want != "" && src.firstLine != tt.firstLine {
				t.Errorf("firstLine = %d, want %d", src.firstLine, tt.firstLine)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		in   string
		want Format
	}{
		{`[{"Plan": {}}]`, FormatJSON},
		{"\n  {\"Plan\": {}}", FormatJSON},
		{"Seq Scan on t  (cost=0.00..1.00 rows=1 width=4)", FormatText},
		{" QUERY PLAN\n----\n [ +\n ]\n(1 row)", FormatJSON},
		{"", FormatUnknown},
		{" \n\t\n", FormatUnknown},
	}
	for _, tt := range tests {
		if got := detect(clean(tt.in)); got != tt.want {
			t.Errorf("detect(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseEmpty(t *testing.T) {
	for _, in := range []string{"", "   \n", " QUERY PLAN\n------\n(0 rows)\n"} {
		if _, err := Parse(strings.NewReader(in)); err != ErrEmpty {
			t.Errorf("Parse(%q) error = %v, want ErrEmpty", in, err)
		}
	}
}
