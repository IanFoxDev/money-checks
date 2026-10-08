# Example: a subscription shop

A small shop with its payments, subscriptions and wallets in MongoDB and a
double-entry ledger in PostgreSQL. The data has money bugs planted on purpose, the
kind that turn up in real billing code: a renewal charged twice for one month, a
user with two active subscriptions, one provider transaction recorded on two
payments, refunds larger than the payment, a wallet balance that no longer matches
its entries, ledger transactions that do not sum to zero.

`checks.yaml` has one check per bug and one that passes. `expected.json` lists what
money-checks must find. CI runs this example and compares the two.

## Run it

```
docker compose build
docker compose run --rm money-checks run -c /etc/money-checks/checks.yaml
```

The exit code is 1: checks with severity `error` found violations. The report starts
with:

```
| Check | Severity | Status | Violations | Amount |
|---|---|---|---:|---:|
| two_renewals_in_one_period | error | violations | 1 | 9.99 USD |
| two_active_subscriptions | error | violations | 2 | 1200 JPY, 9.99 USD |
| provider_txn_recorded_twice | warn | violations | 1 | 1200 JPY |
| refund_exceeds_payment | error | violations | 1 | 11.00 USD |
| wallet_balance_matches_entries | error | violations | 1 | -1.00 EUR |
| payment_without_subscription | error | ok | 0 |  |
| unbalanced_transactions | error | violations | 3 | -10.00 EUR, 9.50 USD |
| negative_wallet_balance | warn | violations | 1 | -3.00 USD |
```

Each check that found something gets a section with up to 10 rows. Emails are
masked (`a***@example.com`), because `two_active_subscriptions` lists `email` under
both `show` and `mask`.

To keep checking every 10 minutes and expose the results to Prometheus:

```
docker compose up
curl -s 127.0.0.1:8080/metrics | grep '^money_check_violations'
```

Set `MC_PORT` if 8080 is taken on your machine.

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
- **Both databases are read by users that can only read.** `mongo-init/seed.js`
  creates a MongoDB user with the `read` role, and `postgres/readonly.sql` a role
  with `SELECT` only. money-checks refuses a MongoDB user that can write, and runs
  every PostgreSQL check in a `READ ONLY` transaction regardless of the role.
