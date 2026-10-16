# money-checks report

Run at 2026-10-16 06:00:00 UTC. 2 checks: 1 found violations, 1 failed, 0 passed.

| Check | Severity | Status | Violations | Amount | Rows |
|---|---|---|---:|---:|---|
| purchases_reach_stats | error | violations | 2 | 32.00 USD | last 72h up to 15m ago |
| payouts_match | warn | failed | - |  | all |

## purchases_reach_stats

Every purchase reaches the daily statistics.

Sources `billing` (left) and `stats` (right), severity error, rows from 2026-10-13 06:00 UTC to 2026-10-16 05:45 UTC (last 72h up to 15m ago). 2 violations, 32.00 USD.

| id | difference | left | right | problem |
|---|---|---|---|---|
| 2026-10-13/USD | 2.00 USD | 20.00 USD | 22.00 USD | amount differs |
| 2026-10-14/USD | -30.00 USD | 30.00 USD | - | only left |

## payouts_match

Sources `ledger` (left) and `stats` (right). The check failed: right: more than max_rows (100000) rows; a side returns one row per key, grouped in the query
