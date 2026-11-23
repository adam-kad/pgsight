package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adam-kad/pgsight/internal/analyze"
	"github.com/adam-kad/pgsight/internal/plan"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

const testdataDir = "../../testdata"

func load(t *testing.T, name string) *plan.Plan {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.ParseString(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPlainGolden renders every plan in testdata/ without colors and compares
// with testdata/golden/<file>.golden. Run `go test ./internal/render -update`
// after an intentional output change and review the diff.
func TestPlainGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testdataDir, "*.*"))
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			p := load(t, name)
			var buf bytes.Buffer
			if err := Plain(&buf, p, analyze.Analyze(p), NewTheme(&buf, false)); err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join(testdataDir, "golden", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if got := buf.String(); got != string(want) {
				t.Errorf("output differs from %s (run with -update if intended)\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
			}
		})
	}
}

func TestPlainHasNoEscapeCodesWithoutColor(t *testing.T) {
	p := load(t, "hash_batches.analyze.txt")
	var buf bytes.Buffer
	if err := Plain(&buf, p, analyze.Analyze(p), NewTheme(&buf, false)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Error("colorless output contains ANSI escape codes")
	}
}

func TestFlatten(t *testing.T) {
	p := load(t, "hash_batches.analyze.txt")
	rows := Flatten(p.Root, nil)
	var guides []string
	for _, r := range rows {
		guides = append(guides, r.Guide)
	}
	want := []string{"", "├─ ", "└─ ", "   └─ "}
	if strings.Join(guides, "|") != strings.Join(want, "|") {
		t.Errorf("guides = %q, want %q", guides, want)
	}
	if rows[3].Parent != 2 || rows[0].Parent != -1 || !rows[2].HasChildren {
		t.Errorf("rows = %+v", rows)
	}

	hash := p.Root.Children[1]
	rows = Flatten(p.Root, map[*plan.Node]bool{hash: true})
	if len(rows) != 3 || !rows[2].Collapsed {
		t.Errorf("collapsed rows = %+v", rows)
	}
	line := ansi.Strip(NewTheme(&bytes.Buffer{}, false).RenderRow(p, rows[2], RowOptions{Markers: true}))
	if !strings.Contains(line, "▸ Hash (+1)") {
		t.Errorf("collapsed row = %q", line)
	}
}

func TestRenderRowWidth(t *testing.T) {
	p := load(t, "cte_subplans.analyze.txt")
	th := NewTheme(&bytes.Buffer{}, false)
	for _, r := range Flatten(p.Root, nil) {
		for _, sel := range []bool{false, true} {
			line := th.RenderRow(p, r, RowOptions{Width: 50, Selected: sel, Markers: true})
			if w := ansi.StringWidth(line); w > 50 || (sel && w != 50) {
				t.Errorf("row width = %d (selected=%v): %q", w, sel, line)
			}
		}
	}
}

func TestFormat(t *testing.T) {
	durations := map[float64]string{0: "0 ms", 0.0123: "0.012 ms", 4.5: "4.50 ms", 188.449: "188.4 ms", 2500: "2.50 s", 90_000: "1.5 min"}
	for in, want := range durations {
		if got := Duration(in); got != want {
			t.Errorf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
	compact := map[float64]string{0: "0", 999: "999", 1234: "1.2k", 107198: "107k", 2_500_000: "2.5M", 0.5: "0.50"}
	for in, want := range compact {
		if got := Compact(in); got != want {
			t.Errorf("Compact(%v) = %q, want %q", in, got, want)
		}
	}
	bars := map[float64]string{0: "     ", 100: "█████", 50: "██▌  ", 0.1: "▏    "}
	for in, want := range bars {
		if got := Bar(in, 5); got != want {
			t.Errorf("Bar(%v) = %q, want %q", in, got, want)
		}
	}
	if got := Kilobytes(13496); got != "13.2 MB" {
		t.Errorf("Kilobytes = %q", got)
	}
}

func TestHeat(t *testing.T) {
	th := NewTheme(&bytes.Buffer{}, false)
	cases := []struct {
		pct  float64
		want string
	}{{0, "cool"}, {9.9, "cool"}, {10, "warm"}, {30, "warm"}, {30.1, "hot"}}
	for _, c := range cases {
		got := map[string]bool{"cool": th.Heat(c.pct).GetForeground() == th.Cool.GetForeground(),
			"warm": th.Heat(c.pct).GetForeground() == th.Warm.GetForeground(),
			"hot":  th.Heat(c.pct).GetForeground() == th.Hot.GetForeground()}
		if !got[c.want] {
			t.Errorf("Heat(%v) is not %s", c.pct, c.want)
		}
	}
}

func TestDetails(t *testing.T) {
	p := load(t, "hash_batches.analyze.txt")
	got := map[string]string{}
	for _, f := range Details(p, p.Root.Children[1]) {
		got[f.Key] = f.Value
	}
	want := map[string]string{
		"Node":    "Hash",
		"Batches": "8",
		"Buckets": "16384",
		"Memory":  "375 kB",
		"Buffers": "shared hit=406, temp written=146",
		"Rows":    "50,000 per loop",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if !strings.HasSuffix(got["Self time"], "%)") {
		t.Errorf("Self time = %q", got["Self time"])
	}

	cost := load(t, "join_estimate.plan.txt")
	for _, f := range Details(cost, cost.Root) {
		if f.Key == "Self time" || f.Key == "Rows" {
			t.Errorf("estimate-only plan shows %s", f.Key)
		}
	}
}
