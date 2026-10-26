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

## Test them

Each database gets the tables the recipes read (`postgres/schema.sql`), rows
that break every recipe and rows next to them that must not (`postgres/seed.sql`,
`mongodb/seed/`), and `tests.yaml` with exactly what each recipe must find.

```sh
docker compose build
docker compose run --rm money-checks test -c /recipes/postgres/checks.yaml -t /recipes/postgres/tests.yaml
docker compose run --rm money-checks test -c /recipes/mongodb/checks.yaml -t /recipes/mongodb/tests.yaml
```

CI runs the same on every push.

## Add one

A recipe needs the query, a row that breaks it, a row at the edge that must not
(zero, the end of a window, a partial refund), and its line in `tests.yaml`.
If it caught a real bug in your system, say what it cost in the pull request.
