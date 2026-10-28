# 0009. explain shows the plan of a check before it runs on a schedule

Date: 2026-10-27. Status: accepted.

## Context

A check can be right about money and still be wrong for the database. Two real
cases, both found only by running the check against production data:

- A MongoDB check grouped refunds by the id of the payment they point to and then
  looked the payment up with `$lookup`. Some refunds had no such field. The empty
  key matched every document of the collection, and the check ran for five minutes
  before its timeout stopped it.
- Another `$lookup` joined on a field with no index. On a small copy it took a
  second; on production it failed by timeout on every run.

ADR 0001 gives every check a timeout that the database enforces, so neither case
harmed anything. The timeout says that a check is slow. It does not say why, and it
says so only after the check has run on the database it watches, often from
`serve` every few minutes.

Every database money-checks reads can describe a query without running it, and can
run it and report where the time went. What each one shows was checked against the
recipe databases (PostgreSQL 17, MySQL 8.4, MongoDB 8.2, ClickHouse 25.8), as the
read-only users the recipes use:

- **PostgreSQL.** `EXPLAIN` inside `BEGIN READ ONLY` plans without running.
  `EXPLAIN ANALYZE` runs the query, and `statement_timeout` stops it with an error,
  as it stops the check itself. On a table of 21 rows the planner chose `Seq Scan`
  over an index on `created_at` that it used as soon as sequential scans were
  turned off. On a table never analyzed it estimated 117 rows for those 21.
- **MySQL.** `EXPLAIN FORMAT=JSON` works under `transaction_read_only`.
  `EXPLAIN ANALYZE` runs the query, and `max_execution_time` stops it, **without an
  error**: the plan comes back with the times up to the limit and `rows=0` at the
  top, and looks like a finished plan.
- **MongoDB.** The `read` role may run `explain`. With `queryPlanner` verbosity a
  `$lookup` on `localField`/`foreignField` shows its join strategy (`HashJoin`,
  `IndexedLoopJoin`, `NestedLoopJoin`), but a `$lookup` with `let` and `pipeline`
  shows only its own definition: nothing about the other collection. With
  `executionStats` the same stage reports `collectionScans`, `indexesUsed` and
  `totalDocsExamined`. `maxTimeMS` stops `explain` only when it is set on the
  `explain` command itself; set inside the `aggregate`, it was ignored and the
  explain ran for almost four seconds against a limit of 300 ms.
- **ClickHouse.** `EXPLAIN indexes = 1` and `EXPLAIN ESTIMATE` work under
  `readonly=2` and read no data. They show which parts and granules the primary
  key and partition key leave to read.

## Decision

**A command, `money-checks explain`, takes the same configuration and flags as
`run` (`-c`, `--check`, `--window`) and prints, for each check and its `expect`,
the plan from the database and a short summary of it:**

- tables and collections read in full, including the foreign side of a `$lookup`
  and the inner side of a join;
- indexes used;
- rows the plan expects to read, or rows read with `--analyze`.

With `--window` the query gets the same `since` and `until` that `serve` would
pass. A window is what lets an index on the date work, and the plan of the full
pass says little about the plan of the window.

**By default nothing runs.** `explain` is for a check that its author is not yet
sure is safe to run.

| Source | default | `--analyze` |
|---|---|---|
| PostgreSQL | `EXPLAIN (FORMAT JSON)` | `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` |
| MySQL | `EXPLAIN FORMAT=JSON` | `EXPLAIN ANALYZE` |
| MongoDB | `explain`, `queryPlanner` | `explain`, `executionStats` |
| ClickHouse | `EXPLAIN indexes = 1` | `EXPLAIN ESTIMATE` as well |

`--analyze` runs the query through the same source as `run`: the same read-only
transaction or session, the same timeout. On MongoDB the timeout goes on the
`explain` command as `maxTimeMS`. On MySQL a plan that took as long as the timeout
is reported as stopped by the timeout, not as a plan. ClickHouse has no explain
that runs the query; `--analyze` adds the estimate and nothing more.

**The summary says what it does not know.** A MongoDB `$lookup` with a pipeline,
explained without `--analyze`, is listed as "not checked: run with --analyze",
never as "no full scans". Both cases above were that kind of stage.

**explain does not fail on a full scan.** On a small table a sequential scan is
the right plan, and every recipe test would fail. The exit code is 0 when every
plan was received and 2 when one could not be, as for a check that fails in `run`.
`--max-rows N` makes it 1 when a plan expects to read, or read, more than N rows,
as `run` returns 1 for violations: for CI against a copy of production size, where
the number means something.

**The text output holds the summary, not the plan.** A plan holds names of
tables, columns and indexes, the constants of the query, which are in the
configuration already, and the window values. It can also hold a row. MySQL reads a
table it can match by primary key or unique key to one row while it plans, and puts
the values of that row into the plan: `p.id = 'p16'` joined to refunds came back as
the condition `r.amount > '50.00'`, the amount of payment p16. So the text output
prints only names, counts and notes. The raw plan of each query is in the `--json`
output, which is for a person who reads plans and is treated like the CSV of `run`:
a file to keep where the data may be kept.

## Why not

- **`--analyze` by default.** Running the query is the step that hurt in both
  cases. The plan alone would have shown the join without an index.
- **Fail on any full scan.** Wrong on small tables, as shown above, and wrong on
  a table read in full on purpose once a day.
- **Advice on indexes.** The plan shows what is read; which index to create depends
  on the writes and the other queries of the table, which money-checks does not
  see.
- **Explain inside `serve`, on every run.** A plan changes with statistics, but
  watching that is the job of the database's own monitoring. A check that becomes
  slow already shows in `money_check_duration_seconds` and by its timeout.

## Consequences

- One more command to keep across four sources. A new source gets `explain` with
  the source, or `explain` says the source cannot explain.
- The summary depends on plan formats that change between versions. The raw plan
  is always in the JSON, so a summary that misses something is a bug to report,
  not a dead end.
- Estimates on a table without statistics are guesses, as the 117 rows above.
  `--analyze` gives real counts.
