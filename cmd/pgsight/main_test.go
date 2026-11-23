package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/adam-kad/pgsight/internal/analyze"
	"github.com/adam-kad/pgsight/internal/plan"
	"github.com/adam-kad/pgsight/internal/render"
)

const testdata = "../../testdata/"

type fakeEnv struct {
	env
	out, errOut bytes.Buffer
	tuiCalls    int
	tuiInput    io.Reader
	ttyOpened   bool
}

func newFake(stdin string) *fakeEnv {
	f := &fakeEnv{}
	f.env = env{
		stdin:  strings.NewReader(stdin),
		stdout: &f.out,
		stderr: &f.errOut,
		getenv: func(string) string { return "" },
		openTTY: func() (io.ReadCloser, error) {
			f.ttyOpened = true
			return io.NopCloser(strings.NewReader("")), nil
		},
		runTUI: func(_ *plan.Plan, _ []analyze.Hint, _ *render.Theme, in io.Reader, _ io.Writer) error {
			f.tuiCalls++
			f.tuiInput = in
			return nil
		},
	}
	return f
}

func (f *fakeEnv) run(t *testing.T, args ...string) int {
	t.Helper()
	return run(args, &f.env)
}

func TestPlainFromFile(t *testing.T) {
	f := newFake("")
	if code := f.run(t, testdata+"seqscan_filter.analyze.txt"); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, f.errOut.String())
	}
	out := f.out.String()
	for _, want := range []string{"EXPLAIN ANALYZE", "Seq Scan on orders 💡", "[warning]", "(amount = 42.5)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if f.tuiCalls != 0 {
		t.Error("TUI started although stdout is not a terminal")
	}
}

func TestPlainFromStdin(t *testing.T) {
	for _, args := range [][]string{nil, {"-"}} {
		f := newFake(" QUERY PLAN\n----\n Result  (cost=0.00..0.01 rows=1 width=4)\n(1 row)\n")
		if code := f.run(t, args...); code != exitOK {
			t.Fatalf("exit %d, stderr %q", code, f.errOut.String())
		}
		if !strings.Contains(f.out.String(), "Result") {
			t.Errorf("output = %q", f.out.String())
		}
	}
}

func TestNoInput(t *testing.T) {
	f := newFake("")
	f.stdinTTY = true
	if code := f.run(t); code != exitError {
		t.Errorf("exit %d, want %d", code, exitError)
	}
	if !strings.Contains(f.errOut.String(), "no plan to read") {
		t.Errorf("stderr = %q", f.errOut.String())
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		args  []string
		stdin string
		msg   string
	}{
		{[]string{"/does/not/exist"}, "", "no such file"},
		{nil, "garbage\n", "line 1: expected a plan node"},
		{[]string{testdata + "gen/generate.sql"}, "", "generate.sql: invalid text plan at line 1"},
		{[]string{"--fail-on=fatal", testdata + "limit.analyze.txt"}, "", "--fail-on"},
		{[]string{"a", "b"}, "", "accepts at most 1 arg"},
		{nil, "", "input is empty"},
	}
	for _, tt := range tests {
		f := newFake(tt.stdin)
		if code := f.run(t, tt.args...); code != exitError {
			t.Errorf("%v: exit %d, want %d", tt.args, code, exitError)
		}
		if !strings.Contains(f.errOut.String(), tt.msg) {
			t.Errorf("%v: stderr %q lacks %q", tt.args, f.errOut.String(), tt.msg)
		}
	}
}

func TestFailOn(t *testing.T) {
	tests := []struct {
		file, failOn string
		want         int
	}{
		{"sort_disk.analyze.txt", "", exitOK},
		{"sort_disk.analyze.txt", "none", exitOK},
		{"sort_disk.analyze.txt", "warning", exitFailOn},
		{"sort_disk.analyze.txt", "info", exitFailOn},
		{"limit.analyze.txt", "info", exitOK},
		{"join_estimate.plan.txt", "info", exitOK},
	}
	for _, tt := range tests {
		f := newFake("")
		args := []string{testdata + tt.file}
		if tt.failOn != "" {
			args = append(args, "--fail-on="+tt.failOn)
		}
		if code := f.run(t, args...); code != tt.want {
			t.Errorf("%s --fail-on=%s: exit %d, want %d (stderr %q)", tt.file, tt.failOn, code, tt.want, f.errOut.String())
		}
		if f.out.Len() == 0 {
			t.Errorf("%s: no output", tt.file)
		}
	}
}

func TestInteractive(t *testing.T) {
	t.Run("file argument, terminal stdin", func(t *testing.T) {
		f := newFake("")
		f.stdinTTY, f.stdoutTTY = true, true
		if code := f.run(t, testdata+"limit.analyze.txt"); code != exitOK || f.tuiCalls != 1 {
			t.Fatalf("exit %d, TUI calls %d", code, f.tuiCalls)
		}
		if f.ttyOpened || f.tuiInput != f.stdin {
			t.Error("keyboard should be read from stdin")
		}
	})
	t.Run("plan piped into stdin", func(t *testing.T) {
		f := newFake("Result  (cost=0.00..0.01 rows=1 width=4)")
		f.stdoutTTY = true
		if code := f.run(t); code != exitOK || f.tuiCalls != 1 {
			t.Fatalf("exit %d, TUI calls %d", code, f.tuiCalls)
		}
		if !f.ttyOpened || f.tuiInput == f.stdin {
			t.Error("keyboard should be read from the controlling terminal")
		}
	})
	t.Run("no terminal to read keys from", func(t *testing.T) {
		f := newFake("Result  (cost=0.00..0.01 rows=1 width=4)")
		f.stdoutTTY = true
		f.openTTY = func() (io.ReadCloser, error) { return nil, errors.New("no tty") }
		if code := f.run(t); code != exitOK || f.tuiCalls != 0 || !strings.Contains(f.out.String(), "Result") {
			t.Fatalf("exit %d, TUI calls %d, output %q", code, f.tuiCalls, f.out.String())
		}
	})
	t.Run("--plain", func(t *testing.T) {
		f := newFake("")
		f.stdinTTY, f.stdoutTTY = true, true
		if code := f.run(t, "--plain", testdata+"limit.analyze.txt"); code != exitOK || f.tuiCalls != 0 {
			t.Fatalf("exit %d, TUI calls %d", code, f.tuiCalls)
		}
	})
	t.Run("TUI error", func(t *testing.T) {
		f := newFake("")
		f.stdinTTY, f.stdoutTTY = true, true
		f.runTUI = func(*plan.Plan, []analyze.Hint, *render.Theme, io.Reader, io.Writer) error {
			return errors.New("boom")
		}
		if code := f.run(t, testdata+"limit.analyze.txt"); code != exitError || !strings.Contains(f.errOut.String(), "boom") {
			t.Fatalf("exit %d, stderr %q", code, f.errOut.String())
		}
	})
}

func TestVersionAndHelp(t *testing.T) {
	f := newFake("")
	if code := f.run(t, "--version"); code != exitOK || !strings.Contains(f.out.String(), "pgsight version") {
		t.Errorf("exit %d, output %q", code, f.out.String())
	}
	f = newFake("")
	if code := f.run(t, "--help"); code != exitOK || !strings.Contains(f.out.String(), "--fail-on") {
		t.Errorf("exit %d, output %q", code, f.out.String())
	}
}
