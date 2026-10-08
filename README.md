# money-checks

[![go](https://github.com/IanFoxDev/money-checks/actions/workflows/go.yml/badge.svg)](https://github.com/IanFoxDev/money-checks/actions/workflows/go.yml)
[![examples](https://github.com/IanFoxDev/money-checks/actions/workflows/examples.yml/badge.svg)](https://github.com/IanFoxDev/money-checks/actions/workflows/examples.yml)

Money invariants as code. A check is a query against your own database, a MongoDB
aggregation pipeline or SQL for PostgreSQL, that returns the rows breaking a rule:
a subscription charged twice for one month, a user with two active subscriptions,
refunds larger than the payment, a ledger transaction that does not sum to zero.
money-checks runs the checks, adds up what they found per currency and tells you,
once as a report or every few minutes as Prometheus metrics and Slack messages.

Bugs like these show up in the data weeks before they show up in support tickets or
in a chargeback. The queries that find them usually get written once, by hand, during
an incident, and nobody runs them again. money-checks keeps them in a file, runs them
against a read-only user and turns each one into a number you can alert on.

> Status: v0.1. Until 1.0 a minor version may change the configuration, the report
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
  ghcr.io/ianfoxdev/money-checks:0.1 run -c /etc/money-checks/checks.yaml
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
| `money_check_violations` | `check`, `severity` | rows the check returned on its last successful run |
| `money_check_amount_minor_units` | `check`, `currency` | total of those rows in minor units (cents for USD) |
| `money_check_up` | `check` | 1 if the last run finished, 0 if it failed |
| `money_check_last_success_timestamp_seconds` | `check` | when the check last finished |
| `money_check_duration_seconds` | `check` | how long the last run took |
| `money_check_errors_total` | `check` | runs that failed |
| `money_checks_runs_total` | | runs of all checks |

When a run fails, `money_check_up` drops to 0 and the violation gauges keep the last
known values, so an alert on violations does not clear itself because the database
was unreachable. An alert rule to start from:

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
        expr: time() - money_check_last_success_timestamp_seconds > 3600
        annotations:
          summary: "{{ $labels.check }} has not finished a run for an hour"
```

With `serve.slack.webhook_env` set, a message goes to Slack when a check changes
state: it starts finding violations, stops finding them, fails or recovers. Not on
every run. Messages carry the check name, the count and the totals, never rows.

## Money

Amounts are `int64` minor units with the ISO 4217 exponent of their currency, from
the database to the report. Nothing is held in a float and nothing is rounded.

- Integers are minor units (`unit: minor`) or whole major units (`unit: major`).
- Decimal strings, MongoDB `Decimal128` and PostgreSQL `numeric` are parsed digit by
  digit: `10.99` USD is 1099, `1200` JPY is 1200.
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

Connection strings and the Slack webhook come from environment variables named in the
file, never from the file itself. Why it is built this way:
[docs/adr/0001-read-only-and-money.md](docs/adr/0001-read-only-and-money.md).

## What leaves the database

A report shows, for at most `samples` rows per check (10 by default), the `id`
column, the amount and the columns listed in `show`. Columns in `mask` are masked
(`anna@example.com` becomes `a***@example.com`). Every other column the query returns
is dropped before anything is written. Slack messages carry no rows at all.

## Example

[examples/subscriptions](examples/subscriptions) is a subscription shop in MongoDB
and a double-entry ledger in PostgreSQL with ten money bugs planted, and eight checks
that find them. `docker compose run --rm money-checks run -c ...` prints the report;
CI checks it against the list of planted bugs.

The full configuration: [docs/config.md](docs/config.md).

## Not yet

ClickHouse and MySQL sources, checks that compare two sources (the day's payments in
one database against the day's revenue in another), a history of violations, and an
MCP mode so an assistant can run the same named queries. If you need one of these
first, open an issue.

## Contributing

A check that caught a real bug in your system is the most useful contribution, with
the data made up: open an issue with the "Check recipe" template. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
