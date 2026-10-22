# money-checks

[![go](https://github.com/IanFoxDev/money-checks/actions/workflows/go.yml/badge.svg)](https://github.com/IanFoxDev/money-checks/actions/workflows/go.yml)
[![examples](https://github.com/IanFoxDev/money-checks/actions/workflows/examples.yml/badge.svg)](https://github.com/IanFoxDev/money-checks/actions/workflows/examples.yml)

Money invariants as code. A check is a query against your own database, a MongoDB
aggregation pipeline or SQL for PostgreSQL or ClickHouse, that returns the rows
breaking a rule:
a subscription charged twice for one month, a user with two active subscriptions,
refunds larger than the payment, a ledger transaction that does not sum to zero.
money-checks runs the checks, adds up what they found per currency and tells you,
once as a report or every few minutes as Prometheus metrics and Slack messages.

Bugs like these show up in the data weeks before they show up in support tickets or
in a chargeback. The queries that find them usually get written once, by hand, during
an incident, and nobody runs them again. money-checks keeps them in a file, runs them
against a read-only user and turns each one into a number you can alert on.

> Status: v0.5. Until 1.0 a minor version may change the configuration, the report
> or the metric names; such changes are marked **BREAKING** in the
> [CHANGELOG](CHANGELOG.md).

## Quick start

```yaml
# checks.yaml
version: 1
sources:
  shop:
    mongodb: {uri_env: SHOP_MONGO_URI, database: shop}
checks:
  - name: two_renewals_in_one_period
    description: A subscription was charged more than once for the same period.
    source: shop
    collection: payments
    pipeline:
      - $match: {kind: renewal, status: succeeded}
      - $group:
          _id: {subscription: "$subscription_id", period: "$period"}
          n: {$sum: 1}
          total: {$sum: "$amount"}
          largest: {$max: "$amount"}
          currency: {$first: "$currency"}
      - $match: {n: {$gt: 1}}
      - $set: {extra: {$round: [{$subtract: ["$total", "$largest"]}, 2]}}
    amount: {field: extra, unit: major, currency_field: currency}
    show: [n]
```

```bash
docker run --rm -v "$PWD:/etc/money-checks" \
  -e SHOP_MONGO_URI='mongodb://checks:secret@db/?authSource=shop&readPreference=secondary' \
  ghcr.io/ianfoxdev/money-checks:0.5 run -c /etc/money-checks/checks.yaml
```

```
| Check | Severity | Status | Violations | Amount |
|---|---|---|---:|---:|
| two_renewals_in_one_period | error | violations | 1 | 9.99 USD |

## two_renewals_in_one_period

A subscription was charged more than once for the same period.

Source `shop`, severity error. 1 violation, 9.99 USD.

| id | amount | n |
|---|---|---|
| {"subscription":"sub_a","period":"2026-09"} | 9.99 USD | 2 |
```

The exit code is 0 when no check with severity `error` found anything, 1 when one
did, and 2 when a check could not run (a bad query, a timeout, a value that is not an
exact amount). The report is Markdown on stdout, ready to paste into an incident or a
ticket; `--json` writes the same data for scripts.

## Run once or keep watching

`money-checks run` runs every check once. Use it for a baseline, in cron or in CI
against a staging copy.

`money-checks serve` runs the checks at start and then every `serve.interval`
(10 minutes by default) and serves:

| Endpoint | |
|---|---|
| `/metrics` | Prometheus metrics, below |
| `/healthz` | the process is alive |
| `/readyz` | the first run has finished, so the metrics mean something |

| Metric | Labels | |
|---|---|---|
| `money_check_violations` | `check`, `severity`, `scope` | rows the check returned on its last successful run, not counting known violations |
| `money_check_known_violations` | `check`, `scope` | rows the known file accepts |
| `money_check_amount_minor_units` | `check`, `currency`, `scope` | total of those rows in minor units (cents for USD) |
| `money_check_up` | `check` | 1 if the last run finished, 0 if it failed |
| `money_check_last_success_timestamp_seconds` | `check`, `scope` | when the check last finished |
| `money_check_duration_seconds` | `check`, `scope` | how long the last run took |
| `money_check_errors_total` | `check` | runs that failed |
| `money_check_expect_value` | `check`, `scope`, `side` | the number the `expect` query found, below |
| `money_check_new_violations_total` | `check` | violations `serve` had not seen before, or saw with another amount |
| `money_check_oldest_violation_age_seconds` | `check` | how long ago `serve` first saw the oldest violation the check still finds |
| `money_check_interval_seconds` | `check` | how often `serve` runs the check: its `every`, or `serve.interval` |
| `money_check_source_up` | `source` | 1 if the source answered in the last run that used it, 0 if it could not be reached |
| `money_checks_runs_total` | | runs of all checks |

When a run fails, `money_check_up` drops to 0 and the violation gauges keep the last
known values, so an alert on violations does not clear itself because the database
was unreachable. `scope` is `full` for a run that read every row and `window` for
one that read only the last `window` of a check (below); the two series are kept
apart, so a window run does not hide what the last full run found. An alert rule to
start from:

```yaml
groups:
  - name: money-checks
    rules:
      - alert: MoneyInvariantBroken
        expr: money_check_violations{severity="error"} > 0
        labels: {severity: page}
        annotations:
          summary: "{{ $labels.check }}: {{ $value }} violations"
      - alert: MoneyCheckNotRunning
        expr: |
          time() - max by (check) (money_check_last_success_timestamp_seconds)
            > 3 * max by (check) (money_check_interval_seconds)
        annotations:
          summary: "{{ $labels.check }} has missed three runs in a row"
      - alert: MoneyViolationOpenForAWeek
        expr: money_check_oldest_violation_age_seconds > 7 * 86400
        annotations:
          summary: "{{ $labels.check }}: a violation is open for more than a week"
      - alert: MoneyCheckSourceDown
        expr: money_check_source_up == 0
        for: 15m
        annotations:
          summary: "money-checks cannot reach {{ $labels.source }}"
```

With `serve.slack.webhook_env` set, a message goes to Slack when a check changes
state (it starts finding violations, stops finding them, fails or recovers) and when
it finds new violations while it already has some. Not on every run. All that
changed in one run is one message, a line per check. A database that cannot be
reached is one line for all its checks, and one more when it is back. Messages carry the check
name, the count and the totals, never rows. `serve.state_file` keeps what
`serve` has seen across restarts, so a deploy does not post everything again.

## Read the last day, not the whole table

A check that scans every payment every ten minutes costs the database the same
whether anything changed or not. With `window: 24h` the query gets the start of the
window as a parameter, `$1` in SQL and `$$since` in a pipeline, and `serve` reads only
the last day between full passes (`serve.full_every`, 24h by default):

```yaml
  - name: refund_twice
    source: ledger
    window: 24h
    query: |
      select payment_id, sum(amount) as amount, currency
      from refunds
      where payment_id in (select payment_id from refunds where created_at >= $1)
      group by payment_id, currency
      having count(*) > 1
```

The window picks the payments that changed; the invariant is checked over all their
refunds. `where created_at >= $1` alone would miss a payment refunded once last month
and once today. The column needs an index. Details: `docs/config.md`.

## Compare two databases

When purchases are written to MongoDB and their daily totals reach ClickHouse through
Kafka, a consumer that skips a batch leaves both databases looking fine on their
own. A `compare` check groups each side in its own database and compares the totals
by key:

```yaml
  - name: purchases_reach_stats
    compare:
      left: {source: billing, collection: purchases, pipeline_file: purchases_by_day.yaml}
      right:
        source: stats
        query: select day, currency, sum(amount) as amount, sum(purchases) as n from daily_purchases group by day, currency
      key: [day, currency]
      amount: {field: amount, unit: major, currency_field: currency}
      counts: [n]
```

A key that only one side has, an amount or a count that differs, is a violation with
the id `2026-10-13/USD`. The report shows both sides and the difference; the amount
metric is the money that does not match. Amounts meet in minor units, so a MongoDB
double and a ClickHouse `Decimal` compare exactly. `settle: 15m` leaves the last
fifteen minutes out of both sides, so a purchase still on its way through Kafka is
not reported. Details: `docs/config.md`.

## A check that sees nothing

Zero rows means the rule holds, or that the query no longer sees the data: a field
renamed in the code, a status that changed case, a collection moved to another
database. The check stays green and nobody notices. Give it an `expect` query that
must find data, usually a count of the rows it reads:

```yaml
  - name: two_renewals_in_one_period
    ...
    expect:
      pipeline:
        - $match: {kind: renewal, status: succeeded}
        - $count: n
      min: 1          # the default
```

It runs first. When it finds less than `min`, the check fails instead of passing:
exit code 2, `money_check_up` 0, a Slack message, and the alert on checks that have
not finished a run fires without a new rule. The number is in the report and in
`money_check_expect_value`, so a drop from thousands to ten is visible before it
reaches zero. The report warns about checks without `expect`;
`defaults.require_expect: true` makes it an error. See `docs/config.md` and
`docs/adr/0005-expect-and-test.md`.

## Test the checks

A query that looked right against a copy of the data can still be wrong. Put rows
with known bugs into a test database, list what each check must find, and run the
checks against it in CI:

```yaml
# tests.yaml
version: 1
tests:
  - check: two_renewals_in_one_period
    violations:
      - {id: '{"subscription":"sub_a","period":"2026-09"}', amount: 9.99 USD}
  - check: refund_twice
    violations: []
```

```bash
money-checks test -c checks.yaml -t tests.yaml
```

A missing row, an extra one or an amount that differs fails the test, with a diff.
money-checks never writes the test data: it holds no credentials that can write,
so the database is filled by your migrations and seeds. Details: `docs/config.md`.

## Known violations

The first run against a real database usually finds old rows that were looked at
long ago: a double charge refunded by hand, a test account. List them in a known
file with the reason, and the check reports only what is new:

```yaml
# checks.yaml
known_file: known.yaml

# known.yaml
version: 1
known:
  - check: two_renewals_in_one_period
    id: '{"subscription":"sub_a","period":"2026-09"}'
    reason: refunded by hand on 2026-09-30, ticket FIN-12
    amount: 9.99 USD
    until: 2026-12-31
```

Known violations do not change the status, the exit code or the alert, and they are
not posted to Slack. They are not hidden either: the report lists them with their
reasons, `money_check_known_violations` counts them, and an entry the check no
longer finds is listed so it can be removed. If the amount changes or `until`
passes, the row counts again. A reason is required. See `docs/config.md` and
`docs/adr/0002-known-violations.md`.

After a first run, look at what a check found and accept it in one step:

```
money-checks known add -c checks.yaml --check refund_twice \
  --reason "refunded by hand in 2025, ticket FIN-12" --until 2026-12-31
```

It runs the check and adds an entry for every new violation, with its amount. It
stops at `--limit` rows, 100 by default. Review the diff of the known file like code.

## Money

Amounts are `int64` minor units with the ISO 4217 exponent of their currency, from
the database to the report. Nothing is held in a float and nothing is rounded.

- Integers are minor units (`unit: minor`) or whole major units (`unit: major`).
- Decimal strings, MongoDB `Decimal128`, PostgreSQL `numeric` and ClickHouse
  `Decimal` are parsed digit by digit: `10.99` USD is 1099, `1200` JPY is 1200.
- A MongoDB double, which is how PHP and JavaScript code often stores prices, is read
  through its shortest decimal form: `10.1` is 1010 cents. A double that is not a
  whole number of minor units fails the check with the row and the value.

That last rule matters for sums. `$sum` over doubles gives `0.30000000000000004` for
0.1 + 0.2, and money-checks refuses it rather than reporting a total that is off.
Round in the pipeline, where the decision is visible: `{$round: [{$sum: "$amount"}, 2]}`.

Totals are kept per currency and never converted.

## Read-only, enforced by the database

Checks run with production credentials, so "the file only contains SELECT" is not
enough.

- **PostgreSQL**: every check runs in its own `BEGIN READ ONLY` transaction with
  `statement_timeout` set to the check's timeout, so the server rejects writes,
  including writes from functions the query calls. Queries go over the extended
  protocol, one statement per query; a DSN with
  `default_query_exec_mode=simple_protocol` cannot turn that off, because with the
  simple protocol `select 1; commit; delete ...` would delete outside the read-only
  transaction.
- **MongoDB**: a pipeline with `$out` or `$merge` anywhere in it is rejected when the
  configuration is loaded. At start money-checks reads the user's privileges with
  `connectionStatus` and stops if the user can write to the database, or if there is
  no user at all (a server without authentication). `require_read_only: false`
  accepts the risk and prints a warning on every run. Each pipeline is sent with
  `maxTimeMS`, so the server stops the work when the check times out, and with the
  read preference from the URI, so checks can read from a secondary.
- **ClickHouse**: every query is sent with `readonly=2`, which the query cannot turn
  off, and with `max_execution_time`. At start money-checks checks the user's grants
  with `CHECK GRANT` and stops if the user can insert, alter, truncate, drop or create
  tables in the database, with the same `require_read_only: false` escape. The
  server never runs two statements in one request.

Connection strings and the Slack webhook come from environment variables named in the
file, never from the file itself. Why it is built this way:
[docs/adr/0001-read-only-and-money.md](docs/adr/0001-read-only-and-money.md).

## Every row, for the people who fix it

The report shows ten samples per check. Support refunding double charges, or
finance correcting a payout, needs all of them:

```bash
money-checks run -c checks.yaml --check two_renewals_in_one_period --csv renewals.csv
```

Every violation of that check goes to the file, with the same `show` columns and
masking as the report, and a `known` column for the rows the known file accepts.
`--jsonl` writes the same rows for a script. See `docs/config.md`.

## What leaves the database

A report shows, for at most `samples` rows per check (10 by default), the `id`
column, the amount and the columns listed in `show`. Columns in `mask` are masked
(`anna@example.com` becomes `a***@example.com`). Every other column the query returns
is dropped before anything is written. `--csv` and `--jsonl` write every row of
one check, with the same columns and masking, and only when asked for by name.
Slack messages carry no rows at all.

## Example

[examples/subscriptions](examples/subscriptions) is a subscription shop in MongoDB,
a double-entry ledger in PostgreSQL and daily statistics in ClickHouse with thirteen
money bugs planted, and nine checks that find them. `docker compose run --rm money-checks run -c ...` prints the report;
CI checks it against the list of planted bugs.

The full configuration: [docs/config.md](docs/config.md).

## Not yet

MySQL sources, a history of violations, and an MCP mode so an assistant can run the same named queries. If you need one of these
first, open an issue.

## Contributing

A check that caught a real bug in your system is the most useful contribution, with
the data made up: open an issue with the "Check recipe" template. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
