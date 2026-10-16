# Configuration

One YAML file, `checks.yaml` by convention. Unknown keys are errors with their line
number, and every problem in the file is reported at once, before anything connects
to a database.

```yaml
version: 1                 # required, the only version so far
currencies: {USDT: 6}      # optional: codes ISO 4217 does not have, or a different exponent
defaults:
  timeout: 30s             # per check; default 30s
  samples: 10              # rows shown per check; default 10, at most 1000
sources: {...}
checks: [...]
serve: {...}               # only for money-checks serve
known_file: known.yaml     # optional: violations that are accepted, see below
```

## sources

A map from a name you choose to one database. A check names the source it runs
against.

```yaml
sources:
  billing:
    mongodb:
      uri_env: BILLING_MONGO_URI   # the environment variable with the connection URI
      database: shop
      require_read_only: true      # default true
  ledger:
    postgres:
      dsn_env: LEDGER_PG_DSN       # the environment variable with the DSN
  stats:
    clickhouse:
      url_env: STATS_CLICKHOUSE_URL  # the environment variable with the HTTP URL
      database: stats
      require_read_only: true        # default true
```

The URI, the DSN and the URL are read from the environment when the source is opened, so the
file can be committed. An empty variable fails the checks of that source with its
name in the message; checks on other sources still run.

**mongodb.** The read preference, the auth source and the TLS settings come from the
URI: `mongodb://checks:secret@host/?authSource=admin&readPreference=secondary`. At
start money-checks runs `connectionStatus` with `showPrivileges` and stops if the
user can insert, update, remove, create or drop collections or indexes, `collMod`,
`applyOps` or `compact` on the database (directly, on all databases, or on any
resource), or if no user is authenticated. With `require_read_only: false` it runs
anyway and prints a warning on every run.

**postgres.** Any DSN pgx accepts, URL or key=value. Every check runs in a
`READ ONLY` transaction with `statement_timeout` set to the check's timeout, and over
the extended protocol whatever the DSN says. Values come back as text, so `numeric`
and `float8` are never read through a float. A role with `SELECT` on the tables the
checks read is enough.

**clickhouse.** The HTTP interface, `http://` or `https://`, port 8123 or 8443:
`https://checks:secret@host:8443/`. The user and the password may also be the `user`
and `password` parameters of the URL; other parameters are passed to the server as
they are. Every check is sent with `readonly=2` and `max_execution_time` set to the
check's timeout; these replace the same parameters in the URL. At start money-checks
reads the user's own `readonly` setting:

- `0`: it checks the user's grants on the database with `CHECK GRANT`, roles
  included, and stops if the user has INSERT, ALTER UPDATE, ALTER DELETE, ALTER DROP
  COLUMN, ALTER MODIFY COLUMN, ALTER MOVE PARTITION, TRUNCATE, DROP TABLE, CREATE
  TABLE or OPTIMIZE. With `require_read_only: false` it runs anyway and prints a
  warning on every run.
- `1`, set by a settings profile: writes are already impossible, and so is sending
  any setting, the timeout included. The checks run with the timeout on the client
  side only, and money-checks warns that a query keeps running on the server after
  the check gives up, unless the profile sets `max_execution_time`.

Values come back as text, so `Decimal`, `UInt64` and `Float64` are read like
`numeric` on PostgreSQL. A query must not have a `FORMAT` clause. Give the user
`SELECT` on one database and nothing else: table functions such as `url()` and
`remote()` only read, but they reach other servers, and only grants keep them out.
Why ClickHouse is read this way: `docs/adr/0003-clickhouse.md`.

## checks

A list. Checks run in this order, one at a time.

| Key | | |
|---|---|---|
| `name` | required | `^[a-z][a-z0-9_]*$`, unique; the `check` label of the metrics |
| `description` | | one sentence for the report |
| `source` | required | a name from `sources` |
| `severity` | | `error` (default) or `warn`. Only `error` sets exit code 1 |
| `collection` | mongodb | the collection the pipeline runs on |
| `pipeline` | mongodb | a list of stages, or `pipeline_file` |
| `pipeline_file` | mongodb | a JSON or YAML file with the stages, relative to the config file |
| `query` | postgres, clickhouse | one SQL statement, or `query_file` |
| `query_file` | postgres, clickhouse | a file with the statement, relative to the config file |
| `id` | | the column that identifies a row; default `_id` for mongodb, required for postgres and clickhouse |
| `amount` | | where the money of a row is, below; without it the check only counts rows |
| `show` | | columns shown next to the id in the report |
| `mask` | | columns of `show` to mask |
| `samples` | | rows shown in the report; overrides `defaults.samples` |
| `timeout` | | overrides `defaults.timeout` |
| `window` | | `24h`: `serve` and `run --window` read only the rows since then, below |
| `compare` | | two sides whose totals must match, below; replaces `source`, the query, `id`, `amount`, `show` and `mask` |
| `settle` | compare | `15m`: both sides leave out the rows of the last 15 minutes, below |

Every row the query returns is a violation. Write the query so that a correct
database returns nothing.

### pipeline

Stages are written in YAML, and key order is kept, so `$sort` and a compound `_id`
work as written. Values can use Extended JSON for types YAML does not have:

```yaml
pipeline:
  - $match:
      created_at: {$gte: {$date: "2026-07-01T00:00:00Z"}}
      price: {$gt: {$numberDecimal: "0"}}
  - $sort: {created_at: 1}
```

`$out` and `$merge` are rejected anywhere in the pipeline, including inside `$facet`
and `$lookup`. Each pipeline gets a comment `money-checks: <name>`, so it can be found
in `currentOp` and the profiler.

A document or an array in a column, such as a compound `_id` from `$group`, is shown
as JSON with its keys in order. ObjectIds, dates and decimals inside it are written as
they are at the top level: `{"user":"652f1c9e8a1b2c3d4e5f6071","period":"2026-09"}`.
Other BSON types keep their relaxed Extended JSON form. This text is what a known file
matches, so it does not change between runs.

### amount

```yaml
amount:
  field: extra               # the column with the amount
  unit: major                # minor or major, no default
  currency: USD              # a fixed currency, or:
  currency_field: currency   # the column with the currency code of each row
```

`unit: minor` means the value counts minor units (cents). `unit: major` means the
value is in whole units and may have decimals (dollars). There is no default, because
guessing wrong is off by a factor of 100.

How values are read:

| Value | `minor` | `major` |
|---|---|---|
| integer | as is | times 10^exponent |
| decimal string, `Decimal128`, `numeric` | must be whole | parsed digit by digit, at most the currency's decimals |
| double | must be whole | its shortest decimal form, then as a string |
| null, missing, anything else | error | error |

A row whose amount cannot be read exactly fails the check, and the message names the
row and the value. Sums of doubles almost always need `$round` in the pipeline.

The currency code is upper-cased and must be in ISO 4217 or under `currencies`.

### window

A full pass over a large table every ten minutes reads rows that have not changed for
years. With `window`, the query gets the start of the window as a parameter and
reads only recent rows; a full pass still runs every `serve.full_every`.

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

On PostgreSQL the start is `$1`, a bind parameter of the type of the column it is
compared with (`timestamptz` or `timestamp`, in UTC). On MongoDB it is the variable
`$$since`, which works inside `$expr` (MongoDB 5.0 and later):

```yaml
    pipeline:
      - $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
```

On ClickHouse it is the query parameter `{since:DateTime64(6, 'UTC')}`, exactly this
type: another one reads the start in the server's time zone or drops the
microseconds, so the configuration check refuses it.

```yaml
    query: |
      select day, sum(amount) as amount, currency
      from daily_purchases
      where day >= toDate({since:DateTime64(6, 'UTC')})
      group by day, currency
```

Nothing is put into the text of the query. On a full pass the start is
`0001-01-01T00:00:00Z` (`1970-01-01` on ClickHouse, so that `toDate()` of it does
not wrap around), so the same query reads every row. A check with `window` must use
the parameter, and a query or pipeline that uses it must have a `window`.
`window` is at least `serve.interval`, or rows written between two runs would wait
for the next full pass. Hours are the largest unit: a week is `168h`.

Mind invariants that span time. "Two refunds of one payment" with `where created_at
>= $1` alone misses a payment refunded once last month and once today: only today's
refund is in the window, so the group has one row. The query above uses the window
to pick the payments that changed and checks the invariant over all their rows. The
column of the window needs an index; money-checks does not create one.

A row that leaves the window is not fixed. Only a full pass says that violations
are gone: the metrics keep one series per `scope`, and the Slack message that a check
no longer finds violations comes from a full pass only.

### compare

Some money is written to two databases: purchases to MongoDB by the billing service,
daily totals to ClickHouse by a Kafka consumer. A `compare` check groups each side in
its own database and compares the totals key by key:

```yaml
  - name: purchases_reach_stats
    description: Every purchase is in the daily statistics, once.
    window: 72h
    compare:
      left:
        source: billing
        collection: purchases
        pipeline:
          - $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
          - $group:
              _id: {day: {$dateToString: {format: "%Y-%m-%d", date: "$paid_at"}}, currency: "$currency"}
              amount: {$sum: "$amount"}
              n: {$sum: 1}
          - $project: {_id: 0, day: "$_id.day", currency: "$_id.currency", amount: {$round: ["$amount", 2]}, n: 1}
      right:
        source: stats
        query: |
          select day, currency, sum(amount) as amount, sum(purchases) as n
          from daily_purchases
          where day >= toDate({since:DateTime64(6, 'UTC')})
          group by day, currency
      key: [day, currency]
      amount: {field: amount, unit: major, currency_field: currency}
      counts: [n]
```

| Key | | |
|---|---|---|
| `left`, `right` | required | each a `source` with a `collection` and a `pipeline` (or `pipeline_file`), or a `query` (or `query_file`); the two sources may be the same |
| `key` | required | the columns both sides return and group by; the id of a violation is their values joined with `/`, such as `2026-10-13/USD` |
| `amount` | | as for other checks, read the same way on both sides; `currency_field` must be in `key` |
| `counts` | | integer columns that must be equal on both sides |
| `tolerance_minor` | | a difference of the amount up to this many minor units is not a violation; default 0 |
| `max_rows` | | the most rows a side may return, default 100000; more is an error of the check |

A violation is a key only one side returns, an amount that differs by more than
`tolerance_minor`, or a count that differs. The report shows each key with both sides
and the difference, right minus left; the totals and
`money_check_amount_minor_units` add up the absolute differences, so they say how
much money does not match. A known file
entry for `2026-10-13/USD` with an `amount` accepts that difference; a different one
counts again.

Each side returns one row per key: a side that returns a key twice, or more than
`max_rows` rows, fails the check rather than compare half of the data. The key must
come out as the same text on both sides. A MongoDB date is written as
`2026-10-13T00:00:00Z` unless the pipeline formats it with `$dateToString`, and then
every key is "only left" and "only right". Amounts are compared in minor units, never
as floating point numbers; a side whose amounts are in another unit converts them in
its query.

Why it works this way: `docs/adr/0004-compare.md`.

### settle

The two sides are not written at the same moment: a purchase of the last minute is
in MongoDB and still on its way to ClickHouse. Compared as they are, the freshest
rows always differ, the check fires on every run of `serve`, and someone turns it
off. `settle` gives both sides an end, `settle` before the start of the run, and the
query reads only the rows before it:

```yaml
  - name: purchases_reach_stats
    window: 72h
    settle: 15m
    compare:
      left:
        source: billing
        collection: purchases
        pipeline:
          - $match:
              $expr:
                $and:
                  - {$gte: ["$paid_at", "$$since"]}
                  - {$lt: ["$paid_at", {$dateTrunc: {date: "$$until", unit: day}}]}
          # $group and $project as above
      right:
        source: stats
        query: |
          select toString(day) as day, currency, sum(amount) as amount, sum(purchases) as n
          from daily_purchases
          where day >= toDate({since:DateTime64(6, 'UTC')})
            and day < toDate({until:DateTime64(6, 'UTC')})
          group by day, currency
```

The end is `$$until` in a pipeline, `{until:DateTime64(6, 'UTC')}` on ClickHouse and
`$2` on PostgreSQL. A PostgreSQL query with `$2` uses `$1` too, because PostgreSQL
cannot tell the type of `$2` alone; without a window `$1` is
`0001-01-01T00:00:00Z`. Every side of a check with `settle` must use the end, and
a query that uses it needs `settle`. `settle` is shorter than `window`, and only
`compare` checks have it: a check of one database sees its rows as they are written.

Mind the buckets. When one side keeps daily totals, a row for today is partial
until the day ends, whatever `settle` says. Compare whole days on both sides, as
above: `day < toDate(until)` on one side, `$dateTrunc` of `$$until` on the other.
Pick `settle` longer than the usual delay between the sides, and alert on the lag
itself elsewhere: a consumer that is an hour behind is an incident of its own.

## serve

```yaml
serve:
  interval: 10m            # default 10m, at least 1m
  full_every: 24h          # default 24h, at least interval: a full pass of checks with a window
  listen: ":8080"          # default :8080
  slack:
    webhook_env: SLACK_WEBHOOK_URL
```

Each run opens the sources again, so a database that restarts between runs does not
need money-checks to restart. With `slack`, a message is posted when a check starts
finding violations, stops finding them, fails or runs again after failing. The first
run posts only about checks that are not clean.

The first run and then one every `full_every` read every row; the runs in between
read the window of the checks that have one. A window run posts new violations
("found 1 violation (5.00 EUR) in the last 24h"), not that they are gone.

## known_file

Violations someone has looked at and accepted: a double charge that support refunded
by hand, a test account. The path is relative to `checks.yaml`. Why this is a
separate file and not a filter in the query: `docs/adr/0002-known-violations.md`.

```yaml
# known.yaml
version: 1
known:
  - check: two_renewals_in_one_period
    id: '{"subscription":"sub_a","period":"2026-09"}'   # as the report prints it
    reason: refunded by hand on 2026-09-30, ticket FIN-12   # required
    amount: 9.99 USD       # optional: if the amount changes, the row counts again
    until: 2026-12-31      # optional: from 2027-01-01 UTC the row counts again
```

| Key | | |
|---|---|---|
| `check` | required | the name of a check in `checks.yaml` |
| `id` | required | the id exactly as the report shows it; quote ids with `{` or `:` |
| `reason` | required | why the row is accepted; an entry without a reason is an error |
| `amount` | | amount and currency, `26.99 USD`; only for checks with `amount` |
| `until` | | a date, `2026-12-31`; the entry applies through the end of that day, UTC |

A row with an entry does not count: it is not in `violations`, the totals, the
status, the exit code or Slack. The report lists it in a separate table with the
reason, and `/metrics` counts it in `money_check_known_violations`. A row whose entry
expired or whose amount changed counts again, with a note that says which. An entry
the check no longer finds is listed under "no longer found", so it can be removed.

`serve` reads the file at start; restart it after a change. `known add` writes the
entries for you after a first run, see below.

## Command line

```
money-checks run -c checks.yaml [--window] [--markdown file] [--json file]
money-checks serve -c checks.yaml
money-checks known add -c checks.yaml --check NAME --reason TEXT [--until DATE] [--limit N]
money-checks version
```

`run` writes the Markdown report to stdout unless `--markdown` names a file, and the
JSON report when `--json` is given. With `--window`, checks that have a `window` read
only its rows; without it, `run` reads every row. Warnings and the errors of failed checks go to
stderr. `serve` logs JSON to stderr and stops on SIGINT or SIGTERM.

`known add` runs one check and appends every new violation it finds to the known
file: one entry per row, with the reason, the amount and `until` when given. It runs
the check again instead of reading a report, because a report has samples, not every
row. It refuses when the check finds more than `--limit` rows (100 by default) and
skips rows whose entry expired or whose amount changed; edit those entries by hand.
The file is created if it does not exist. Appending needs the `known` list to be the
last key of the file; otherwise nothing is written and the entries are printed to
add by hand. Exit code 0 when entries were added or there was nothing to add, 2 on
an error.

## The JSON report

```json
{
  "version": 1,
  "started_at": "2026-10-08T06:00:00Z",
  "summary": {"checks": 4, "violations": 2, "failed": 1, "passed": 1},
  "warnings": [],
  "checks": [
    {
      "name": "two_renewals_in_one_period",
      "source": "shop",
      "severity": "error",
      "status": "violations",
      "violations": 1,
      "totals": [{"currency": "USD", "minor": 999, "amount": "9.99"}],
      "samples": [
        {"id": "...", "amount": {"currency": "USD", "minor": 999, "amount": "9.99"}, "fields": {"n": "2"}}
      ],
      "duration_ms": 12,
      "scope": "window",
      "since": "2026-10-07T06:00:00Z",
      "window": "24h",
      "known": 1,
      "known_totals": [{"currency": "USD", "minor": 2699, "amount": "26.99"}],
      "known_samples": [
        {"id": "...", "amount": {"currency": "USD", "minor": 2699, "amount": "26.99"}, "reason": "refunded by hand, FIN-12"}
      ],
      "resolved": [{"id": "...", "reason": "test account, deleted"}]
    }
  ]
}
```

`status` is `ok`, `violations` or `failed` (then `error` has the reason). Amounts are
given twice, as integer minor units and as a decimal string, never as a JSON number
with a fraction. Lists are never null. `violations`, `totals` and `samples` are about
new violations; `known*` are the rows the known file accepts, and `resolved` lists
its entries the check no longer finds. A sample that is in the known file but counts
again has a `note` that says why. `scope` is `full` when the check read every row and
`window` when it read the rows since `since`; then `resolved` is empty, because a
window run does not see older rows. A `compare` check has `right_source` next to
`source`, its left side; the amount of each of its samples is the difference, right
minus left, and its `fields` are both sides and the `problem`. With `settle` it has
`until`, the end of the rows both sides read, and `settle`.
