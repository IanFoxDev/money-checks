# 0005. A check proves it sees data, and a test only reads

Date: 2026-10-17. Status: accepted.

## Context

A check reports rows that break a rule, so zero rows means the rule holds. Zero rows
also means the query sees nothing at all, and the two look the same. A field renamed
from `amount` to `amount_total`, a status that became `SUCCEEDED` instead of
`succeeded`, a collection moved to another database: the query keeps running, finds
nothing, and the check stays green until someone reads it by hand. The alert on
`money_check_violations` never fires, and the alert on runs that fail does not fire
either, because the run did not fail.

This is the usual way such a check dies. Schemas change with the code, and the
checks live in another file, often in another repository.

The same gap exists before a check reaches production. A new query is tried once
against a copy of the data, looks right, and is never run against a case where the
answer is known. The example in this repository has a list of planted bugs and a
script that compares the report with it; a user of money-checks has to build the
same thing on their own.

## Decision

**A check can have an `expect` query that must find data.** It runs against the
same source as the check, before it, with the same read-only rules and the same
timeout. It returns one number, such as the count of payments in the last week, and
the check fails when the number is below `min`, 1 by default:

```yaml
  - name: refund_twice
    source: ledger
    query: ...
    expect:
      query: select count(*) as n from refunds where created_at >= now() - interval '7 days'
      min: 1
```

- The result is one row with one column, `_id` aside for MongoDB. No rows is 0,
  because `$count` over nothing returns no document. More rows or more columns is an
  error of the check, so a wrong expect query is loud.
- `expect` gets the same parameters as the check: the start of the window in a window
  run, the end of `settle`. In a window run "no payments in the last day" then fails
  the check, which is how a busy service that stopped writing looks.
- When the number is below `min`, the check is `failed` with the text "expect found
  0, at least 1 required", and the check query does not run. A failed check already
  does what a check that sees nothing should do: exit code 2, `money_check_up` 0, the
  violation gauges keep their last values, Slack says the check fails, and
  `money_check_last_success_timestamp_seconds` stops moving, so the alert on checks
  that have not finished a run fires without a new rule.
- The number is in the report next to the status and in
  `money_check_expect_value{check, scope, side}`, so a drop that is still above
  `min` can be seen and alerted on.
- Each side of a `compare` check has its own `expect`; `side` is `left` or `right`.
  For other checks `side` is empty.
- `expect` is optional. The report lists the checks without it in its warnings, and
  `defaults.require_expect: true` makes it a configuration error, for teams that
  want every check covered.

**`money-checks test` compares what checks find with what they should find, and
never writes.** A tests file lists checks and the violations each must report:

```yaml
version: 1
tests:
  - check: refund_twice
    violations:
      - {id: pay_1, amount: 10.00 USD}
      - {id: pay_7}
  - check: two_active_subscriptions
    violations: []
```

`money-checks test -c checks.yaml -t tests.yaml` runs the listed checks against the
sources of the configuration and compares the ids exactly: a missing or an extra id
is a failure, shown as a diff. An amount, when given, must match. Exit code 0 when
every test passes, 1 when one does not, 2 when a check could not run.

- The data is the user's job. money-checks does not insert fixtures: it never holds
  credentials that can write, and a test command that writes is one wrong environment
  variable away from writing to production. The test database is filled the way the
  application fills it, by its migrations and seeds, or by a compose file as in
  `examples/subscriptions`. The same `checks.yaml` is used, with the connection
  strings pointing at the test database.
- A test reads every row a check returns, not the samples of the report, up to 10000
  rows; more is an error, because a test database should be small.
- The known file is not applied. A test checks the query, and known violations are a
  fact about one production database.
- Checks run in full, without a window: the rows of a window depend on the clock, and
  a test should not.
- `expect` runs as in production. A test database without data for an `expect` fails
  the test, which keeps the expect query honest too.
- Checks that the file does not list are not run, and the output names them, so a new
  check without a test is visible.

## Consequences

- An `expect` query is one more query per check against the database. It should be
  cheap: a count over an indexed range, not a second scan of the table.
- `expect` catches a check that sees no data. It does not catch a check that sees
  data and is wrong, such as a `$match` on a status that no longer exists in the
  rows the check needs. That is what `test` is for, and what review of the checks is
  for.
- `min` is a number, not a rule over time. A drop from a million rows to ten passes
  `min: 1`; the gauge is there for an alert on the trend.
- `test` does not test a window. A window check runs in full, so its query is tested
  with the start of a full pass (`0001-01-01`, `1970-01-01` on ClickHouse), not with
  a recent date.
- The example keeps its own comparison with planted bugs until `test` can replace it,
  then switches to `test`, so CI runs the command users run.
