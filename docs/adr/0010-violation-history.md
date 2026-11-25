# 0010. serve keeps a history of violations, and a changed check is compared, not alerted

Date: 2026-11-25. Status: proposed.

## Context

ADR 0006 gave `serve` a memory of the present: for each check, the violations the
last runs found, with the amount and the time each was first seen. A run posts what
is new, a full run posts what is fixed, and the state file carries this over a
restart. That answers "what changed since the last run". After a month on a real
database three questions came up that it cannot answer.

**A violation that comes back looks new.** A double charge is fixed by a data patch,
the full run posts it as fixed and the id leaves the state. A week later a retry job
writes the second charge again. The id is new to the state, so the message says
"found 1 new violation", the same as for a bug nobody has seen. A fix that did not
hold is worse news than a new case: someone already closed the ticket.

**A changed check floods the channel or hides rows quietly.** A check is changed to
leave out a false positive, or to catch a case it missed. If the id stays the same,
the next full run posts every row the new version finds that the old did not as new
violations, as if they had appeared today, and the rows it no longer finds as fixed,
as if someone had fixed them. Neither is true: the data did not change, the check
did. If the id changes form, every row is posted twice, as new and as fixed (0006,
Consequences). The review of the change sees only the query. Nothing shows which
rows it adds or drops before it is merged.

**Nothing says how the checks do over time.** Which check finds bugs and which only
noise, how long a violation stays open, how many came back after a fix, what a new
check found in old data on its first run. The metrics hold counts per check, not per
violation, and the reports of `run` hold one moment each. To decide which check to
fix, which to drop and which to write next, someone needs the events, not the totals.

## Decision

### History file

**With `serve.history_file`, serve appends one line of JSON for every change of a
violation:**

```json
{"time":"2026-11-25T10:00:03Z","check":"double_charge","id":"p_1042","event":"new","scope":"window","amount":{"currency":"EUR","minor":999,"exponent":2},"version":"3f9a1c0e"}
```

| event | when |
|---|---|
| `new` | an id not in the state, not fixed before (or fixed longer ago than the state keeps it, below) |
| `changed` | an id in the state with another amount |
| `fixed` | an id a full run no longer finds |
| `regressed` | an id that was fixed and is found again |
| `baseline` | an id found by the first full run of a check that is new or changed, below |
| `dropped` | an id that a changed check no longer finds |

- Only changes are written. A violation open for a month is one line, not one per
  run, so the file grows with what happens, not with how often `serve` runs.
- `version` is the version of the check, below, so a person can see which query
  found a row.
- The file holds ids and amounts, as the state file and the CSV do. It is created
  with mode 0600 and belongs next to the state file, not in the repository of the
  configuration.
- The lines of a run are appended before the state file is written. A crash between
  the two writes the same events again on the next run; the reader of the file takes
  repeated events as one. Losing events would be worse than repeating them.
- serve does not rotate the file. At about 200 bytes a line, a hundred thousand
  events are 20 MB. A deployment that needs less keeps it on a volume with its own
  rotation, or loads it into its own store; JSON Lines loads into any of them.
- Without `history_file` nothing is written, and the rest of this ADR still applies:
  `regressed`, `baseline` and `dropped` come from the state file.

### Fixed violations are kept for 90 days

**The state keeps an id for 90 days after it was fixed, with the time it was first
seen and the time it was fixed.** An id that a run finds again in that time is
`regressed`, and the message says so:

> double_charge: 1 violation came back (9.99 EUR), fixed 6 days ago; 151 in all [error]

Regressions are posted even when the check also has new violations, in their own
line. `money_check_regressed_violations_total{check}` counts them.

90 days is a constant, not a setting. It is longer than most retry and repair jobs
run, and the state stays small: a fixed id costs as much as an open one. The state
file gets version 2; serve reads a version 1 file as before, with no fixed ids.

### The version of a check

**Each check gets a version: a hash of everything that decides which rows it
returns and how they are read.** That is the query or pipeline, the collection, the
`compare` sides, `key`, the column of the id and of the amount, `window`, `settle`
and the source kind. Not the description, the schedule, `show`, `mask` or the
known file: they change what a report shows, not what the check finds.

The state keeps the version of the last full run. When the first full run after a
start finds that the version changed, that run is a baseline and is not alerted row
by row:

- rows the old version did not find are `baseline`, not `new`;
- rows it no longer finds are `dropped`, not `fixed`;
- rows both versions find keep their first seen;
- one message says what the change did:

> double_charge changed: 151 violations, 12 found for the first time, 3 no longer found

A check that is new in the configuration gets the same: every row of its first full
run is `baseline`. The message of a first run (0006) stays as it is, and the history
then says that these rows were found in old data, not on the day they were posted.

The baseline needs a full run because a window run sees only its window: a row
outside it is neither found nor dropped. serve starts with a full run, and a change
of the configuration needs a restart, so the first run after a change is the
baseline.

### history

**`money-checks history FILE` reads a history file and prints, for each check:**

- violations found, fixed, regressed and dropped in the period;
- violations open at the end of it, and the oldest of them;
- the median time from first seen to fixed;
- the rows found by the baseline of each version.

`--since` sets the period (default `720h`), `--check NAME` limits it to one check,
`--json` gives the same numbers for a script or an agent that reviews the checks.
`history` connects to nothing and needs no configuration. It is the input for
deciding which check to fix or drop and which to write next.

### diff

**`money-checks diff -c NEW --old OLD` runs the checks whose version differs between
two configurations, both versions against the same database, and prints the rows
only the old version finds, the rows only the new one finds, and the rows whose
amount differs.** `--check NAME` limits it to one check, `--window` runs both with
the window. Rows are read as `run --check --csv` reads them, with `show` and `mask`
of the new version. The exit code is 0 when both versions find the same rows, 1
when they differ and 2 when a check fails, as `diff(1)` does.

`diff` is for the review of a change to a check: the author runs it against a copy
of production, or off peak against a replica, and the rows go with the change.
Hiding rows is the change that most needs a reason, and `diff` is what shows it.
`explain` comes first: `diff` runs each check twice.

## Why not

- **A database for the history.** 0006 holds: a static binary for two
  architectures, no schema or migrations in the tool. The history is a log of
  events, which a file appends and any store loads. A team that wants to query it
  loads it into the database it already has.
- **Recheck open violations by id.** A window run cannot say a violation is fixed,
  and a query by id would say so between full runs. But `serve.full_every` already
  reads every check in full every day by default, and a query by id is a second
  query to write and keep for every check, on four kinds of source. A check whose
  full run is too heavy to run daily is better served by `explain` and an index.
- **Learn from the history inside the tool.** Which check is noise, which rule to
  write next and which violation to accept are decisions about the business, and
  their reasons go through review: into the known file (0002) or into a change to
  a check, with `diff`. The tool gives the numbers; people, or an agent that
  proposes changes for people to review, decide.
- **A setting for the 90 days.** One more number to choose, with no case yet that
  needs another.

## Consequences

- The state file goes to version 2. A downgrade of serve reads a version 2 file as
  unreadable, warns and starts without it (0006): one noisy run.
- A change to a check is visible in Slack as one line instead of a list of rows, and
  in the history as `baseline` and `dropped`. A change that must not be silent, such
  as a check that stops finding a real bug, is caught at review by `diff`, not after
  the deploy.
- The version hashes the configuration, not the data. A change of the data, such as
  a migration that renames a field, keeps the version and is caught by `expect`
  (0005), as now.
- A history file is as sensitive as the CSV of a check, and it grows. Where it is
  kept, who reads it and how long it lives is the decision of the deployment.
