# 0002. Known violations are listed with a reason and stay visible

Date: 2026-10-09. Status: accepted.

## Context

The first run of a new check against a real database almost always finds old rows:
a double charge that support refunded by hand two years ago, a test account with a
negative balance, an import that was fixed in the books but not in the table. Each
of them was looked at once and accepted.

Without a way to say so, the check reports them forever. The alert on
`money_check_violations > 0` stays red, people learn to ignore it, and then a new
double charge arrives on a check that everyone has stopped reading. The usual fix is
to add `and id not in (...)` to the query. That hides the row for good, with no
reason next to it, and the query stops saying what the invariant is.

## Decision

**Accepted violations live in a separate file, `known_file`, one entry per row.**
An entry names the check, the id as the report prints it and a reason. The reason is
required: an entry without one is a configuration error. The file is separate from
`checks.yaml` because it changes more often and is often kept by other people
(support, finance), while the checks change with the code.

**An entry can say when it stops applying.** `until` is a date; from the next day
(UTC) the row counts again. `amount`, when given, must match the row: a refund that
grows after it was accepted is news. A row whose entry expired or whose amount
changed counts as a new violation, and the report says which of the two happened.

**Known violations do not count, but they do not disappear.**

- `violations`, the totals, the status, the exit code of `run` and the Slack
  messages are about new violations only.
- The report lists known violations in their own table with the reason, and the
  totals of known violations separately. `/metrics` has
  `money_check_known_violations{check}`.
- An entry the check no longer finds is listed as "no longer found", so the file
  shrinks instead of collecting entries nobody can explain. This needs a run that
  finished: a failed run says nothing about which rows are gone.

The rows are matched by the id as the report prints it, so an entry can be copied
from a report. Matching is exact.

## Consequences

- `money_check_violations` changes meaning when a known file is used: it counts new
  violations. Without `known_file` nothing changes.
- Accepting a violation is a change to a file that goes through review, with a
  reason in it. That is slower than editing a query, on purpose.
- A check whose id is not stable (a row number, a generated key) cannot use known
  violations. The id should be the business key: a payment id, a subscription and a
  period.
- `serve` reads the file at start. A change needs a restart, as a change to
  `checks.yaml` does.
- Writing a hundred entries by hand after a first run is tedious. A command that
  turns a JSON report into entries is a separate step.
