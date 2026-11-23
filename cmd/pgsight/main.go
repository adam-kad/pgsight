// Command pgsight shows PostgreSQL EXPLAIN plans as an interactive tree in
// the terminal. It only reads plan text; it never connects to a database.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/adam-kad/pgsight/internal/analyze"
	"github.com/adam-kad/pgsight/internal/plan"
	"github.com/adam-kad/pgsight/internal/render"
	"github.com/adam-kad/pgsight/internal/tui"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// version is set at build time by GoReleaser (-ldflags "-X main.version=...").
var version = "dev"

// Exit codes.
const (
	exitOK     = 0
	exitError  = 1
	exitFailOn = 2 // hints at or above --fail-on were found
)

// env is the process environment, abstracted for tests.
type env struct {
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	stdinTTY  bool
	stdoutTTY bool
	getenv    func(string) string
	// openTTY opens the controlling terminal for keyboard input when the
	// plan itself arrives on stdin.
	openTTY func() (io.ReadCloser, error)
	// runTUI starts the interactive view; replaced in tests.
	runTUI func(p *plan.Plan, hints []analyze.Hint, theme *render.Theme, in io.Reader, out io.Writer) error
}

func main() {
	e := &env{
		stdin:     os.Stdin,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		stdinTTY:  isTerminal(os.Stdin),
		stdoutTTY: isTerminal(os.Stdout),
		getenv:    os.Getenv,
		openTTY:   openTTY,
		runTUI:    tui.Run,
	}
	os.Exit(run(os.Args[1:], e))
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// exitError carries a specific exit code through cobra.
type codeError struct {
	code int
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }

func run(args []string, e *env) int {
	cmd := newRootCmd(e)
	cmd.SetArgs(args)
	cmd.SetIn(e.stdin)
	cmd.SetOut(e.stdout)
	cmd.SetErr(e.stderr)
	err := cmd.Execute()
	if err == nil {
		return exitOK
	}
	var ce *codeError
	if errors.As(err, &ce) {
		if ce.err != nil && ce.code != exitFailOn {
			fmt.Fprintln(e.stderr, "pgsight:", ce.err)
		}
		return ce.code
	}
	fmt.Fprintln(e.stderr, "pgsight:", err)
	return exitError
}

type options struct {
	plain   bool
	noColor bool
	failOn  string
}

func newRootCmd(e *env) *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "pgsight [file]",
		Short: "Readable PostgreSQL EXPLAIN plans, right in your terminal",
		Long: `pgsight reads the output of EXPLAIN or EXPLAIN ANALYZE (text or JSON format,
with or without psql's header) and shows it as an interactive tree with the
time each node takes, colored by its share, plus optimization hints.

It only reads plan text: it never connects to a database or sends data anywhere.`,
		Example: `  pgsight plan.txt
  psql -c "EXPLAIN (ANALYZE, BUFFERS) SELECT ..." | pgsight
  pgsight --plain plan.json
  pgsight --plain --fail-on=warning plan.txt   # exit code 2 if a warning is found`,
		Args:          cobra.MaximumNArgs(1),
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return execute(e, o, args)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&o.plain, "plain", "p", false, "print the plan once instead of opening the interactive view (default when output is not a terminal)")
	f.BoolVar(&o.noColor, "no-color", false, "disable colors (also honored: NO_COLOR environment variable)")
	f.StringVar(&o.failOn, "fail-on", "none", "exit with code 2 if a hint of this severity or higher is found: none, info, warning")
	return cmd
}

func execute(e *env, o options, args []string) error {
	failOn, err := parseFailOn(o.failOn)
	if err != nil {
		return &codeError{exitError, err}
	}

	in, name, closeIn, err := openInput(e, args)
	if err != nil {
		return &codeError{exitError, err}
	}
	p, err := plan.Parse(in)
	closeIn()
	if err != nil {
		if name != "" {
			err = fmt.Errorf("%s: %w", name, err)
		}
		return &codeError{exitError, err}
	}
	hints := analyze.Analyze(p)

	color := !o.noColor && e.getenv("NO_COLOR") == ""
	interactive := !o.plain && e.stdoutTTY
	var keyboard io.Reader = e.stdin
	if interactive && !e.stdinTTY {
		tty, err := e.openTTY()
		if err != nil {
			interactive = false // no terminal to read keys from: print instead
		} else {
			defer tty.Close()
			keyboard = tty
		}
	}

	if interactive {
		theme := render.NewTheme(e.stdout, color)
		if err := e.runTUI(p, hints, theme, keyboard, e.stdout); err != nil {
			return &codeError{exitError, err}
		}
	} else {
		theme := render.NewTheme(e.stdout, color && e.stdoutTTY)
		if err := render.Plain(e.stdout, p, hints, theme); err != nil {
			return &codeError{exitError, err}
		}
	}

	if failOn != nil {
		for _, h := range hints {
			if h.Severity >= *failOn {
				return &codeError{code: exitFailOn}
			}
		}
	}
	return nil
}

func parseFailOn(s string) (*analyze.Severity, error) {
	if s == "" || s == "none" {
		return nil, nil
	}
	sev, err := analyze.ParseSeverity(s)
	if err != nil {
		return nil, fmt.Errorf("--fail-on: %w", err)
	}
	return &sev, nil
}

// openInput returns the plan source: the named file, or stdin for no
// argument or "-".
func openInput(e *env, args []string) (r io.Reader, name string, closeFn func(), err error) {
	if len(args) == 1 && args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return nil, "", nil, err
		}
		return f, args[0], func() { f.Close() }, nil
	}
	if e.stdinTTY {
		return nil, "", nil, errors.New("no plan to read: pass a file, or pipe EXPLAIN output into pgsight\n" +
			"  e.g.  psql -c \"EXPLAIN ANALYZE SELECT ...\" | pgsight\n" +
			"Run pgsight --help for more.")
	}
	return e.stdin, "", func() {}, nil
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}
