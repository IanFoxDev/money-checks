# 0006. serve remembers each violation, not only whether a check is clean

Date: 2026-10-20. Status: accepted.

## Context

`serve` posts to Slack when a check changes state: it starts finding violations,
stops finding them, fails, or runs again after failing. That is enough while checks
are clean most of the time. It is not how money checks look once they meet a real
database. The first run finds rows that are real bugs, not accepted cases: a
subscription paid twice, a refund that left access open. They go to the bug tracker,
not to the known file, and until the fix ships the check keeps finding them.

From then on the check is in the state "found violations" and stays there. A new
double charge the next day does not change the state, so nothing is posted. The
count in `money_check_violations` goes from 150 to 151, and someone has to be looking
at the graph to notice. The check that was written to catch new cases goes quiet
exactly when it has something to say.

The state also lives in memory. A restart, which in Kubernetes is every deploy and
every node drain, makes the first run post every check that is not clean, again.
After a few deploys people mute the channel.

## Decision

**serve keeps, for each check, the violations it last saw: id, amount and when it
first saw them.** A violation is new when its id was not there before, or when the
id was there with another amount, as the known file treats a changed amount. The
first time an id is seen stays with it when only the amount changes, so its age is
how long the problem has existed.

- After each run `serve` compares what the check found with what it had. Every
  row counts, not the samples of the report: the rows are read through the same path
  as `run --check --csv`.
- New violations are posted even when the check already had violations: "found 2 new
  violations (59.98 USD), 151 in all [error]". The message gives the count and the
  total. Ids go to the log, not to Slack: a channel is read by more people than the
  report. The ids stay in the report and the CSV, and never become metric labels.
- Only a full run removes ids. A window run sees the rows of its window, so it adds
  what is new and leaves the rest; a full run replaces the set, and the ids that are
  gone are posted as fixed: "3 violations fixed (89.97 USD), 148 left". This is the
  rule `serve` already has for "no longer finds violations".
- Known violations are not tracked. When a known entry expires or its amount
  changes, the row becomes a violation, and that is new.
- A failed run changes nothing: the check did not say what it sees.
- For a `compare` check the id is the key, and the amount is the difference. A day
  whose difference grew is new.

**The set can be kept in a file.** With `serve.state_file`, `serve` reads the file at
start and writes it after every run, to a temporary file next to it that is then
renamed, with mode 0600. A restart then posts only what changed while it was down.
Without `state_file` the set lives in memory, as the state does now, and the first
run after a start posts a summary of every check that is not clean, as it does now.

- The file is JSON with a version: one object per check, id to amount and first
  seen. It is read by people when something looks wrong, and it is small: ten
  thousand violations are about a megabyte.
- A check in the file that is no longer in the configuration is dropped. A check
  that is new in the configuration gets the first-run summary, like any check on a
  first run.
- A file that cannot be read or has another version is a warning, and `serve`
  starts as without it. The cost is one noisy run; refusing to start would turn a
  bad file into an outage of the monitoring.
- One check keeps at most 100000 ids. Above that the check falls back to the state
  it has today, with a warning: a check with that many violations needs a fix of
  the data or a known entry, not a message per row.

**Two metrics come from the set.** `money_check_new_violations_total{check}` counts
the new violations, for an alert on any increase. `money_check_oldest_violation_age_seconds{check}`
is the age of the oldest violation the check still finds, for an alert such as "a
violation is open for more than seven days", which turns a bug that nobody owns into
a page.

## Why not SQLite

A database would keep history: every violation, when it appeared and left. That is
more than `serve` needs to decide what to post, and it costs CGO or a pure Go driver,
a schema and migrations, in a tool that ships as a static binary for two
architectures. History belongs in the metrics, which Prometheus already keeps, and
in the reports of `run`. The file holds the present, which is what the next run is
compared with.

## Consequences

- serve reads every row of every check, not only the samples. That is what
  `run --check --csv` does already; the rows are counted and kept as ids and amounts,
  so memory grows with the number of violations, not with the rows the query scans.
- The file holds ids and amounts of violations, as the reports do. It is written with
  mode 0600 and belongs where the reports go, not in the repository of the
  configuration.
- An id that changes form, because the query now builds it differently, makes every
  violation new and, on the next full run, every old one fixed. Change ids and expect
  one noisy run, or remove the check from the file first.
- One file belongs to one `serve`. Two replicas with one configuration would post
  twice with or without the file; run one.
- `run` stays without state. A report of one run has nothing to compare with;
  known violations and the CSV cover what a person needs from it.
