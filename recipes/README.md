# Recipes

Checks for money invariants that most payment, wallet and subscription systems
share. Each recipe is a query you copy into your project, and test data that
shows what it catches and what it must leave alone.

## Use one

1. Copy the query file for your database: `postgres/<recipe>.sql` or
   `mongodb/<recipe>.yaml`.
2. Change the table, collection and column names to yours.
3. Copy the check from `postgres/checks.yaml` or `mongodb/checks.yaml` into
   your config and point `query_file` or `pipeline_file` at your copy.
4. Run `money-checks run --check <recipe>` against a replica. If it finds
   violations you already know about, put them in a
   [known file](../README.md#known-violations) before you start `serve`.

## Catalog

| Recipe | The bug it catches | PostgreSQL | MongoDB |
|---|---|---|---|
| `negative_balance` | An account goes below zero when two debits read the same old balance, or a timed out job is retried | [query](postgres/negative_balance.sql) | [pipeline](mongodb/negative_balance.yaml) |
| `unbalanced_transaction` | A ledger transaction does not sum to zero in a currency: a fee booked with different amounts on each side, or a conversion that lost a leg when the process died between two inserts | [query](postgres/unbalanced_transaction.sql) | [pipeline](mongodb/unbalanced_transaction.yaml) |
| `balance_matches_entries` | A stored balance is not the sum of its entries: updated twice on a retried request, or changed by hand without an entry | [query](postgres/balance_matches_entries.sql) | [pipeline](mongodb/balance_matches_entries.yaml) |
| `duplicate_provider_txn` | One provider transaction recorded as two payments: a webhook handled twice, or a redirect racing the webhook | [query](postgres/duplicate_provider_txn.sql) | [pipeline](mongodb/duplicate_provider_txn.yaml) |
| `refund_exceeds_payment` | Refunds add up to more than the payment: a refund retried after a timeout, or a manual refund on top of the automatic one | [query](postgres/refund_exceeds_payment.sql) | [pipeline](mongodb/refund_exceeds_payment.yaml) |
| `currency_mismatch` | A payment in another currency than its order, which every check comparing amounts only lets through | [query](postgres/currency_mismatch.sql) | [pipeline](mongodb/currency_mismatch.yaml) |
| `stuck_in_pending` | A payment pending for hours: a lost webhook, a dead polling worker, a status update that failed after the provider answered | [query](postgres/stuck_in_pending.sql) | [pipeline](mongodb/stuck_in_pending.yaml) |
| `paid_not_granted` | An order paid and not fulfilled: the event that grants access was lost, or fulfilment failed and nothing retried it | [query](postgres/paid_not_granted.sql) | [pipeline](mongodb/paid_not_granted.yaml) |
| `access_after_expiry` | A subscription active long after its paid period: the renewal failed and the job that expires subscriptions skipped it | [query](postgres/access_after_expiry.sql) | [pipeline](mongodb/access_after_expiry.yaml) |

## Test them

Each database gets the tables the recipes read (`postgres/schema.sql`), rows
that break every recipe and rows next to them that must not (`postgres/seed.sql`,
`mongodb/seed/`), and `tests.yaml` with exactly what each recipe must find.

```sh
docker compose build
docker compose run --rm money-checks test -c /recipes/postgres/checks.yaml -t /recipes/postgres/tests.yaml
docker compose run --rm money-checks test -c /recipes/mongodb/checks.yaml -t /recipes/mongodb/tests.yaml
```

The rows the lifecycle recipes read are dated from now: `now() - interval '3 hours'`
in `seed.sql`, `"now-3h"` in `mongodb/seed/`, which `mongodb/init.js` turns into a
date. The MongoDB pipelines use `$dateSubtract`, MongoDB 5.0 and later.

CI runs the same on every push.

## Add one

A recipe needs the query, a row that breaks it, a row at the edge that must not
(zero, the end of a window, a partial refund), and its line in `tests.yaml`.
If it caught a real bug in your system, say what it cost in the pull request.
