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
  require_expect: false    # true: a check without expect is an error, see below
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
| `pipeline_file` | mongodb | a JSON or YAML file with the stages, relative to the config file or absolute |
| `query` | postgres, clickhouse | one SQL statement, or `query_file` |
| `query_file` | postgres, clickhouse | a file with the statement, relative to the config file or absolute |
| `id` | | the column that identifies a row; default `_id` for mongodb, required for postgres and clickhouse |
| `amount` | | where the money of a row is, below; without it the check only counts rows |
| `show` | | columns shown next to the id in the report |
| `mask` | | columns of `show` to mask |
| `samples` | | rows shown in the report; overrides `defaults.samples` |
| `timeout` | | overrides `defaults.timeout` |
| `window` | | `24h`: `serve` and `run --window` read only the rows since then, below |
| `every` | | `6h`: `serve` runs the check every 6 hours instead of every `serve.interval`, a multiple of it; a full run still runs it, below |
| `compare` | | two sides whose totals must match, below; replaces `source`, the query, `id`, `amount`, `show` and `mask` |
| `settle` | compare | `15m`: both sides leave out the rows of the last 15 minutes, below |
| `expect` | | a query that must find data before the check runs, below; on a compare check, on each side |

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

A variable of your own named `since`, bound by `let` of `$lookup`, `vars` of `$let` or
`as` of `$map`, `$filter` and `$reduce`, is yours inside that stage or operator, not
the start of the window.

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
| `left`, `right` | required | each a `source` with a `collection` and a `pipeline` (or `pipeline_file`), or a `query` (or `query_file`), and an optional `expect`; the two sources may be the same |
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
a query that uses it needs `settle`. Only `compare` checks have it: a check of one
database sees its rows as they are written. `settle` needs no `window`: a compare of
the last 30 whole days, which picks its own start from the current time, can take
its end from `settle` alone, and both sides then end at the same moment. With a
window, `settle` is shorter than it.

Mind the buckets. When one side keeps daily totals, a row for today is partial
until the day ends, whatever `settle` says. Compare whole days on both sides, as
above: `day < toDate(until)` on one side, `$dateTrunc` of `$$until` on the other.
Pick `settle` longer than the usual delay between the sides, and alert on the lag
itself elsewhere: a consumer that is an hour behind is an incident of its own.

### expect

A check that finds nothing because its query sees nothing looks like a check that
passes. A renamed field, a status written in another case, a collection that moved:
the query keeps running and returns no rows. `expect` is a second query that returns
one number, usually a count of the rows the check reads, and the check fails when the
number is below `min`:

```yaml
  - name: refund_twice
    source: ledger
    query_file: refund_twice.sql
    id: payment_id
    expect:
      query: select count(*) from refunds where created_at >= now() - interval '7 days'
  - name: two_renewals_in_one_period
    source: shop
    collection: payments
    pipeline: [...]
    expect:
      pipeline:
        - $match: {kind: renewal, status: succeeded, paid_at: {$gte: {$date: "2026-01-01T00:00:00Z"}}}
        - $count: n
      min: 100
```

| Key | | |
|---|---|---|
| `query`, `query_file` | postgres, clickhouse | one statement that returns one row with one column |
| `pipeline`, `pipeline_file` | mongodb | a pipeline that returns one document with one field besides `_id`, such as `$count` |
| `collection` | mongodb | default: the collection of the check |
| `min` | | the smallest number that passes; default 1 |

- It runs before the check, against the same source, with the same read-only rules
  and the same timeout. When the number is below `min`, the check is `failed` with
  "expect found 0, at least 1 required" and its query does not run.
- No rows is 0, because `$count` over no documents returns none. More than one row,
  more than one column, or a value that is not an integer fails the check.
- A failed check is what a check that sees nothing should be: exit code 2,
  `money_check_up` 0, the violation gauges keep their last values, Slack says it
  fails, and `money_check_last_success_timestamp_seconds` stops moving.
- The number is in the report (column Expect) and in `money_check_expect_value`, also
  when it is above `min`, so a drop can be seen before it reaches zero.
- The query may use the parameters of the check: the start of the window (`$1`,
  `$$since`, `{since:DateTime64(6, 'UTC')}`) and, on a compare side, the end of
  `settle`. It gets them only if it uses them, so a count over the last week needs no
  window. In a window run, "no payments since the start of the window" then fails the
  check. On PostgreSQL, `$1` needs a window on the check, or `$2` and `settle`.
- On a `compare` check each side has its own `expect`, under `compare.left` and
  `compare.right`. One is enough to tell a dead side: when the other side sees
  nothing, every key of the first is a violation.
- The report lists the checks without `expect` in its warnings.
  `defaults.require_expect: true` makes a check without it a configuration error; a
  compare check then needs `expect` on at least one side.

Keep it cheap: a count over an indexed range, not a second scan of the table. Why it
works this way: [adr/0005-expect-and-test.md](adr/0005-expect-and-test.md).

## serve

```yaml
serve:
  interval: 10m            # default 10m, at least 1m
  full_every: 24h          # default 24h, at least interval: a full pass of checks with a window
  retry: 1m                # default 1m (or interval, if shorter), from 10s to interval, below
  listen: ":8080"          # default :8080
  state_file: state.json  # optional: what serve has seen survives a restart, below
  slack:
    webhook_env: SLACK_WEBHOOK_URL
```

Each run opens the sources again, so a database that restarts between runs does not
need money-checks to restart. With `slack`, a message is posted when a check starts
finding violations, stops finding them, fails or runs again after failing, and when
it finds new violations while it already has some. The first run posts only about
checks that are not clean. A check that failed and runs again says so with what
changed meanwhile ("runs again, no new violations, 158 in all", "runs again and
found 1 new violation ..."), not with its whole list.

All that changed in one run is one message: a single change as one line, several
under a first line that counts them, in the order of the configuration.

```
money-checks: 2 checks changed
two_active_subscriptions found 1 new violation (5.00 EUR), 2 in all [error]
refund_exceeds_payment failed: connection reset
```

Above 20 lines the message ends with "and N more"; the log has every line, as
`"msg":"state changed"`, and the message as `"msg":"message"`. Why: [adr/0007-source-down-one-message.md](adr/0007-source-down-one-message.md).

A source that cannot be reached, because the connection is refused, reset or
lost, or MongoDB finds no server to select, is down. Its checks do not run until
the next run and get no lines of their own; the source gets one line when it goes
down and one when it is back:

```
money-checks: source stats is down, 4 checks did not run: dial tcp 127.0.0.1:8123: connect: connection refused
money-checks: source stats is up again
```

The error in the message is cut to 200 characters; the log has all of it. While a
source is down its checks keep the violations they had: nothing is posted as
fixed, and when the source is back only what is new is posted.

`serve` does not wait a whole `interval` for such a source. It runs its checks
again after `retry`, then after twice as long, 1m, 2m, 4m and so on, until the
source answers or the wait reaches `interval`. A retry runs only those checks, in
the scope they would have had, and does not move the other runs: with `interval:
1h` a database that is back in two minutes costs two minutes of checks, not an hour.
`money_check_source_up{source}` is 0 meanwhile. A query that runs out of its
`timeout`, an empty variable or a user that can write are not a source that is
down: the check fails on its own, as before. `run` reports the checks of a source
that is down as failed with "source stats is down: ...", and does not wait for each
of them to time out.

A violation is new when its id was not found by the last run, or was found with
another amount, as the known file treats a changed amount: "found 2 new violations
(59.98 USD), 151 in all". Known violations are not news. The message gives the
count and the total; the new ids go to the log. A check with more than 100000
violations is news only when its state changes.

A check with `every` runs less often than the others: `every: 6h` with `interval: 1h`
runs it every sixth run. Use it for a check that is heavy on the database and whose
answer does not change by the hour. A full run, at start and every `full_every`, runs
every check whatever its turn, and its turns count from there, so every check is read
in full at least every `full_every`. A window must cover the time between two runs,
so a check with `every` and `window` needs `window` at least `every`. `run` ignores
`every`. `money_check_interval_seconds{check}` says how often each check runs, for an
alert on checks that stopped running (README).

The first run and then one every `full_every` read every row; the runs in between
read the window of the checks that have one. A window run posts the new violations
of its window ("found 1 new violation (5.00 EUR) in the last 24h"), not that older
ones are gone: only a full run says a violation is fixed ("1 violation fixed (9.99
USD), 150 left"). Why: [adr/0006-serve-remembers-violations.md](adr/0006-serve-remembers-violations.md).

Without `state_file` this lives in memory, and the first run after a start posts
every check that is not clean again. With it, `serve` reads the file at start and
writes it after every run, so a restart posts only what changed while it was down.
A relative path is relative to the configuration file. The file is JSON: per check,
the ids of its violations with their amounts and when `serve` first saw them. It is
written to a temporary file next to it and renamed, with mode 0600, because it holds
ids and amounts as the reports do; keep it where the reports go, on a volume of its
own in a container. `serve` does not start when it cannot write the file. A file it
cannot read, or of another version, is a warning, and `serve` starts as without it.
Checks that are no longer in the configuration are dropped from it. Run one `serve`
per state file.

## known_file

Violations someone has looked at and accepted: a double charge that support refunded
by hand, a test account. The path is relative to `checks.yaml`, or absolute. Why this is a
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

One known file can serve several configurations, such as one per database and one
for `serve` with all the checks. An entry for a check the configuration does not
have is left out with a warning, not an error: it cannot hide a violation, and a
misspelt check name shows itself as a violation that still counts.

`serve` reads the file at start; restart it after a change. `known add` writes the
entries for you after a first run, see below.

## Command line

```
money-checks run -c checks.yaml [--window] [--markdown file] [--json file]
                 [--check NAME [--csv file] [--jsonl file]]
money-checks serve -c checks.yaml
money-checks known add -c checks.yaml --check NAME --reason TEXT [--until DATE] [--limit N]
money-checks test -c checks.yaml -t tests.yaml
money-checks version
```

`run` writes the Markdown report to stdout unless `--markdown` names a file, and the
JSON report when `--json` is given. With `--window`, checks that have a `window` read
only its rows; without it, `run` reads every row. Warnings and the errors of failed checks go to
stderr. `serve` logs JSON to stderr and stops on SIGINT or SIGTERM.

With `--check`, `run` runs that check only. `--csv` and `--jsonl` then write every
violation it finds, not only the samples of the report: the list a refund or a
fix is made from.

- The CSV has `id`, `amount` and `currency` when the check has an amount, the
  columns of `show` (masked as in the report) or, for a `compare` check, both sides,
  the counts and the problem, then `known`, the reason of a row the known file
  accepts, and `known_note`, why a listed row counts again. Known rows are in the
  list; filter them by `known`. The amount of a `compare` row is the difference,
  right minus left.
- A value that starts with `=`, `+`, `-` or `@` and is not a number gets a `'` in
  front, so a spreadsheet shows it instead of running it as a formula.
- JSON Lines has one object per violation, as the samples of the JSON report.
- The rows are written as the database returns them, not held in memory. Each file
  is written next to its target and renamed when the check finishes; a check that
  fails leaves no file. The files are readable by their owner only.
- `--csv` and `--jsonl` without `--check` are an error: the list is for one thing to
  do, and the columns differ from check to check.

`known add` runs one check and appends every new violation it finds to the known
file: one entry per row, with the reason, the amount and `until` when given. It runs
the check again instead of reading a report, because a report has samples, not every
row. It refuses when the check finds more than `--limit` rows (100 by default) and
skips rows whose entry expired or whose amount changed; edit those entries by hand.
The file is created if it does not exist. Appending needs the `known` list to be the
last key of the file; otherwise nothing is written and the entries are printed to
add by hand. Exit code 0 when entries were added or there was nothing to add, 2 on
an error.

`test` runs the checks a tests file lists and compares what they find with what
they must find, below. Exit code 0 when every test passes, 1 when one does not, 2
when a check could not run.

## Tests file

A test database with known content, and a file that says what each check must find
in it:

```yaml
# tests.yaml
version: 1
tests:
  - check: refund_twice
    violations:
      - {id: pay_1, amount: 21.98 USD}
      - {id: pay_7}
  - check: two_active_subscriptions
    violations: []
```

| Key | | |
|---|---|---|
| `check` | required | the name of a check in `checks.yaml`, once |
| `violations` | required | the rows the check must report; `[]` when it must find nothing |
| `violations[].id` | required | the id as the report shows it; for a `compare` check, the key such as `2026-10-13/USD` |
| `violations[].amount` | | amount and currency, `21.98 USD`; when given it must match (for `compare`, the difference, right minus left) |

```
$ money-checks test -c checks.yaml -t tests.yaml
FAIL  refund_twice: 2 violations, want 2 violations
        amount  pay_1 10.99 USD, want 21.98 USD
        missing pay_7
        extra   pay_9 5.00 EUR
ok    two_active_subscriptions: 0 violations

not tested: unbalanced_transactions

2 tests: 1 passed, 1 failed, 0 could not run
```

- money-checks does not write test data. Fill the test database the way the
  application does: migrations and seeds, a fixture loader, or a compose file as in
  `examples/subscriptions`. Use the same `checks.yaml` with the environment variables
  pointing at the test database.
- Every row a check returns is compared, not only the samples of a report, up to
  10000; more fails the test.
- The known file is not read, so it need not exist where the tests run.
- Checks run in full: a check with a window gets the start of a full pass as its
  parameter, so the result does not depend on the clock. `settle` still leaves out
  the last minutes before now.
- `expect` runs as in production, so the test data must satisfy it.
- Checks the file does not list do not run and are named in the output.

Why tests only read: [adr/0005-expect-and-test.md](adr/0005-expect-and-test.md).

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
      "resolved": [{"id": "...", "reason": "test account, deleted"}],
      "expect": [{"found": 1532, "min": 1}]
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
`until`, the end of the rows both sides read, and `settle`. `expect` has what the
expect query found and its `min`; a compare check has one entry per side that has
an expect, with `side` `left` or `right`.
