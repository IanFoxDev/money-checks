# Example: a subscription shop

A small shop with its payments, subscriptions and wallets in MongoDB, a
double-entry ledger in PostgreSQL and daily payment totals in ClickHouse. The data
has money bugs planted on purpose, the kind that turn up in real billing code: a
renewal charged twice for one month, a user with two active subscriptions, one
provider transaction recorded on two payments, refunds larger than the payment, a
wallet balance that no longer matches its entries, ledger transactions that do not
sum to zero, daily statistics that lost a payment, a whole batch, and gained a test
payment.

`checks.yaml` has one check per bug and one that passes. `tests.yaml` lists what
each check must find, by id and amount, and CI runs `money-checks test` with it.

## Run it

```
docker compose build
docker compose run --rm money-checks run -c /etc/money-checks/checks.yaml
```

The exit code is 1: checks with severity `error` found violations. The report starts
with:

```
| Check | Severity | Status | Violations | Amount | Rows | Expect |
|---|---|---|---:|---:|---|---:|
| two_renewals_in_one_period | error | violations | 1 | 9.99 USD | all | 6 |
| two_active_subscriptions | error | violations | 2 | 1200 JPY, 9.99 USD | all | 6 |
| provider_txn_recorded_twice | warn | violations | 1 | 1200 JPY | all | 7 |
| refund_exceeds_payment | error | violations | 1 | 11.00 USD | all | 3 |
| wallet_balance_matches_entries | error | violations | 1 | -1.00 EUR | all | 3 |
| payment_without_subscription | error | ok | 0 |  | all | 10 |
| unbalanced_transactions | error | violations | 3 | -10.00 EUR, 9.50 USD | all | 12 |
| negative_wallet_balance | warn | violations | 1 | -3.00 USD | all | 6 |
| payments_reach_stats | error | violations | 3 | 113.99 USD | all up to 15m ago | left 7, right 6 |
```

Expect is what the `expect` query of each check found: the rows it reads. A check
whose query stops seeing its data fails instead of reporting 0 violations.

Each check that found something gets a section with up to 10 rows. Emails are
masked (`a***@example.com`), because `two_active_subscriptions` lists `email` under
both `show` and `mask`.

To keep checking every 10 minutes and expose the results to Prometheus:

```
docker compose up
curl -s 127.0.0.1:8080/metrics | grep '^money_check_violations'
```

Set `MC_PORT` if 8080 is taken on your machine.

## What serve posts

`serve` without Slack writes what it would post to its log, as `"msg":"state changed"`.
The first run posts every check that finds violations, one message with eight
lines:

```
money-checks: 8 checks changed
two_renewals_in_one_period found 1 violation (9.99 USD) [error]
...
```

The log has each line as `"msg":"state changed"` and the message as
`"msg":"message"`.

From then on a check is posted when it finds violations it has not seen, also when
it already had some. With `serve.state_file` it remembers them across restarts. CI
does this with a copy of `checks.yaml` that has `state_file: /out/state.json`:

1. The first start posts the eight lines above.
2. While `serve` is down, a second renewal of `sub_d` for 2026-09 is inserted.
3. The next start posts one line, not eight:
   `two_renewals_in_one_period found 1 new violation (19.99 EUR), 2 in all [error]`.
4. One more start, with nothing new, posts nothing.

To try it, give the container your user so it can write `out/`:

```
mkdir -p out
{ cat checks.yaml; printf 'serve:\n  state_file: /out/state.json\n'; } > out/serve.yaml
MC_UID=$(id -u) MC_GID=$(id -g) docker compose run --rm money-checks serve -c /out/serve.yaml
```

The file holds the ids and amounts of the violations, with mode 0600. Without it a
restart posts all eight lines again, which after a few deploys is how a channel gets
muted.

To check that every check finds exactly the planted bugs:

```
docker compose run --rm money-checks test -c /etc/money-checks/checks.yaml -t /etc/money-checks/tests.yaml
```

## What to look at in checks.yaml

- **Amounts in MongoDB are doubles** (`9.99`), as PHP and JavaScript code often
  writes them. A sum of doubles is not exact (0.1 + 0.2 is 0.30000000000000004), so
  every pipeline that adds amounts rounds the sum with `$round` before money-checks
  reads it. Remove a `$round` and the check fails with the document id and the
  value, instead of reporting a total that is off by a fraction of a cent.
- **Wallet amounts are integers in minor units** (`unit: minor`), so
  `wallet_balance_matches_entries` needs no rounding at all.
- **JPY has no minor units**: 1200 JPY is 1200, not 12.00. money-checks knows the
  exponent of every ISO 4217 currency.
- **`payments_reach_stats` compares two databases.** The payments in MongoDB are
  grouped by day and currency in a pipeline, the statistics in ClickHouse in SQL,
  and money-checks compares the totals by key. Its section of the report shows each
  day that differs, both sides and the difference:

  ```
  | id | difference | left | right | payments left | payments right | problem |
  |---|---|---|---|---|---|---|
  | 2026-09-01/USD | -9.99 USD | 19.98 USD | 9.99 USD | 2 | 1 | amount differs, payments differs |
  | 2026-09-05/USD | -99.00 USD | 99.00 USD | - | 1 | - | only left |
  | 2026-09-08/USD | 5.00 USD | - | 5.00 USD | - | 1 | only right |
  ```

  ClickHouse has two rows for 2026-09-07, one per batch; the query sums them, so the
  day matches. `settle: 15m` with whole days on both sides keeps today and the last
  minutes out, where the two databases always differ.
- **Every check has an `expect` query**, usually the same `$match` with `$count`.
  CI changes `status: succeeded` to `status: success` in
  `payment_without_subscription`, as if the code had started writing another
  status. Without `expect` the check would still pass with 0 violations; with it, the
  check fails with "expect found 0, at least 1 required" and exit code 2.
- **Every database is read by a user that can only read.** `mongo-init/seed.js`
  creates a MongoDB user with the `read` role, `postgres/readonly.sql` a role with
  `SELECT` only, and `clickhouse/init.sh` a ClickHouse user with `SELECT` on `stats`.
  money-checks refuses a MongoDB or ClickHouse user that can write, runs every
  PostgreSQL check in a `READ ONLY` transaction regardless of the role, and sends
  every ClickHouse query with `readonly=2`.
