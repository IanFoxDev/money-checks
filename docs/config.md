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
```

The URI and the DSN are read from the environment when the source is opened, so the
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
| `query` | postgres | one SQL statement, or `query_file` |
| `query_file` | postgres | a file with the statement, relative to the config file |
| `id` | | the column that identifies a row; default `_id` for mongodb, required for postgres |
| `amount` | | where the money of a row is, below; without it the check only counts rows |
| `show` | | columns shown next to the id in the report |
| `mask` | | columns of `show` to mask |
| `samples` | | rows shown in the report; overrides `defaults.samples` |
| `timeout` | | overrides `defaults.timeout` |

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
as relaxed Extended JSON. To show an ObjectId inside one as plain hex, convert it in
the pipeline with `$toString`.

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

## serve

```yaml
serve:
  interval: 10m            # default 10m, at least 1m
  listen: ":8080"          # default :8080
  slack:
    webhook_env: SLACK_WEBHOOK_URL
```

Each run opens the sources again, so a database that restarts between runs does not
need money-checks to restart. With `slack`, a message is posted when a check starts
finding violations, stops finding them, fails or runs again after failing. The first
run posts only about checks that are not clean.

## Command line

```
money-checks run -c checks.yaml [--markdown file] [--json file]
money-checks serve -c checks.yaml
money-checks version
```

`run` writes the Markdown report to stdout unless `--markdown` names a file, and the
JSON report when `--json` is given. Warnings and the errors of failed checks go to
stderr. `serve` logs JSON to stderr and stops on SIGINT or SIGTERM.

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
      "duration_ms": 12
    }
  ]
}
```

`status` is `ok`, `violations` or `failed` (then `error` has the reason). Amounts are
given twice, as integer minor units and as a decimal string, never as a JSON number
with a fraction. Lists are never null.
