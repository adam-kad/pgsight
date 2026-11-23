# pgsight

**Readable PostgreSQL EXPLAIN plans, right in your terminal.**

[![CI](https://github.com/adam-kad/pgsight/actions/workflows/ci.yml/badge.svg)](https://github.com/adam-kad/pgsight/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/adam-kad/pgsight)](https://github.com/adam-kad/pgsight/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

![pgsight demo](docs/demo.gif)

[Русская версия](README.ru.md)

pgsight takes the output of `EXPLAIN` or `EXPLAIN ANALYZE` and turns it into an
interactive tree. Each node shows the time it took by itself, colored by its
share of the query, and pgsight adds hints about likely problems: missing
indexes, stale statistics, sorts and hashes spilling to disk.

> **Runs locally.** pgsight only reads the plan text you give it. It never
> connects to a database and never sends anything over the network, so plans
> containing table names, columns and literal values stay on your machine.

## Install

With Go 1.27 or newer:

```bash
go install github.com/adam-kad/pgsight/cmd/pgsight@latest
```

Or download a prebuilt binary for Linux, macOS or Windows from the
[releases page](https://github.com/adam-kad/pgsight/releases).

## Usage

```bash
# A plan saved to a file
pgsight plan.txt

# Straight from psql
psql -c "EXPLAIN (ANALYZE, BUFFERS) SELECT ..." mydb | pgsight

# Print once instead of opening the interactive view (for pipes and CI)
pgsight --plain plan.txt

# Fail a CI job when a warning is found (exit code 2)
pgsight --plain --fail-on=warning plan.json
```

pgsight detects the format by itself and accepts:

- **text** (the default `EXPLAIN` output) and **JSON** (`EXPLAIN (FORMAT JSON)`);
- output copied from psql with its `QUERY PLAN` header, the `(N rows)` footer and
  the `+` line continuations, or the bare plan as copied from GUI clients;
- plans with `ANALYZE` (real timings) and without it (estimates only); `BUFFERS`,
  `VERBOSE`, `SETTINGS`, `COSTS OFF` and `TIMING OFF` are understood too.

For the most useful picture, run `EXPLAIN (ANALYZE, BUFFERS)`. Keep in mind that
`ANALYZE` actually executes the statement: wrap data-modifying queries in
`BEGIN; ... ROLLBACK;`.

### Options

| Flag | Description |
|---|---|
| `-p`, `--plain` | Print the plan once. This is the default when output is not a terminal. |
| `--no-color` | Disable colors. The `NO_COLOR` environment variable is honored as well. |
| `--fail-on` | `none` (default), `info` or `warning`: exit with code 2 if a hint of that severity or higher is found. |
| `--version` | Print the version. |

Exit codes: `0` success, `1` the input could not be read or parsed, `2` hints
found at the `--fail-on` level.

### Keys

| Key | Action |
|---|---|
| `↑` `↓` / `k` `j` | Move between nodes |
| `←` / `h` | Fold the node, or go to its parent |
| `→` / `l` | Unfold the node, or go to its first child |
| `space` | Fold or unfold |
| `enter` | Show or hide the details panel for the node |
| `n` | Jump to the next node with a hint |
| `g` `G`, `PgUp` `PgDn` | Top, bottom, page up and down |
| `?` | All keys |
| `esc` | Close the panel |
| `q` | Quit |

## Reading the output

```
EXPLAIN ANALYZE · text format
Execution 52.8 ms · Planning 0.185 ms
💡 2 warnings

     SELF      % SHARE       NODE
  22.8 ms  43.2% ████▍       Hash Join  107k rows (est 108k)
  20.7 ms  39.2% ███▉        ├─ Seq Scan on orders o 💡  107k rows (est 108k)
  4.05 ms   7.7% ▊           └─ Hash 💡  50k rows (est 75k)
  2.96 ms   5.6% ▌              └─ Seq Scan on customers c  50k rows (est 75k)

Hints
  [warning] Seq Scan on orders o
            Seq Scan on orders read 400,000 rows and kept 107,198 (27%)
            → An index on the filtered columns may let PostgreSQL skip the rest: (created_at > '2023-01-01'::date)
  [warning] Hash
            Hash table was split into 8 batches and spilled to disk
            → Increase work_mem so the hash table fits in memory (it used 375 kB per batch).
```

- **SELF** is the node's *exclusive* time: the time spent in the node itself,
  without its children. PostgreSQL prints per-loop averages, so pgsight
  multiplies by `loops` and subtracts the children.
- **%** and **SHARE** compare SELF to the total `Execution Time`. Colors:
  green below 10%, yellow from 10% to 30%, red above 30%.
- **rows** are actual rows per loop, with the planner's estimate in parentheses
  when they differ, and `×N` for the number of loops.
- 💡 marks nodes with hints. The details panel (`enter`) shows everything
  PostgreSQL reported for the node: conditions, buffers, sort and hash details.

A few details make the numbers match reality:

- Nodes below `Gather` run in several processes at once; their time is divided
  by the number of processes so parallel work is not counted twice.
- A CTE is executed by the `CTE Scan` that reads it, so its time is subtracted
  from that scan rather than from the node the CTE is attached to.
- Without `ANALYZE` (or with `TIMING OFF`) there is no timing, and shares are
  computed from the planner's cost instead.

## Hints

Each hint is a separate rule in [`internal/analyze`](internal/analyze/analyze.go)
with its own tests. All rules need `EXPLAIN ANALYZE` data.

| Rule | Fires when | Suggests |
|---|---|---|
| `seq-scan-filter` | A Seq Scan with a filter reads more than 100,000 rows and discards most of them | An index on the filtered columns |
| `row-estimate` | Actual rows differ from the estimate by more than 10× (and one of them is at least 100) | `ANALYZE` the table; `CREATE STATISTICS` for correlated columns |
| `sort-disk` | `Sort Method: external merge` — the sort spilled to disk | More `work_mem`, or an index that provides the order |
| `nested-loop` | The inner side of a Nested Loop runs more than 1,000 times | Checking the inner side and the outer row estimate |
| `inefficient-filter` | A filter removes over 10× more rows than it keeps (10,000+ rows in total) | An index matching the filter |
| `hash-batches` | A Hash is split into more than one batch | More `work_mem` |

To keep the list short and relevant:

- a row misestimate is reported only on the node where it starts, not on every
  node above it, and "fewer rows than expected" below a `LIMIT` is ignored,
  since the executor stops early there on purpose;
- some hints are downgraded to `info` for nodes that take less than 10% of the time.

## Limitations

- One plan at a time: if the input contains several, the first is shown.
- Without `ANALYZE`, exclusive cost is approximate. Planner costs do not add up
  like times do (for example, a Nested Loop's cost already includes its inner
  side multiplied by the expected loops).
- The YAML and XML formats of `EXPLAIN` are not supported.

## Contributing

Issues and pull requests are welcome. To work on pgsight you need Go 1.27+.

```bash
go test ./...                       # unit and golden tests
go test -race ./...
golangci-lint run                   # lint, config in .golangci.yml
go test -run='^$' -fuzz=FuzzParse -fuzztime=1m ./internal/plan
```

The project layout:

```
cmd/pgsight/       CLI entry point (cobra)
internal/plan/     plan model, text and JSON parsers, exclusive time
internal/analyze/  hints
internal/render/   plain output and shared formatting
internal/tui/      interactive view (Bubble Tea)
testdata/          real plans in both formats, plus golden outputs
```

**Example plans.** Every plan in `testdata/` comes from a real PostgreSQL
server, generated by [`testdata/gen/generate.sql`](testdata/gen/generate.sql).
The script runs inside a transaction that is rolled back, so it leaves the
database untouched:

```bash
cd testdata && psql -X -d postgres -f gen/generate.sql
```

Each scenario is saved in text and JSON format, and the tests check that both
parse into the same tree. After a change to the plain output, update the golden
files and review the diff:

```bash
go test ./internal/render -update
```

**Adding a hint.** Write a check function in `internal/analyze/analyze.go`,
add it to `Rules`, cover it in `analyze_test.go` with a plan from `testdata/`
(add a scenario to `generate.sql` if none fits) and an edge case or two, and
list it in the table above.

**Demo GIF.** `docs/demo.gif` is recorded with [vhs](https://github.com/charmbracelet/vhs):
`vhs docs/demo.tape`.

## License

[MIT](LICENSE)
