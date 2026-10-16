# money-checks report

Run at 2026-10-16 06:00:00 UTC. 2 checks: 1 found violations, 1 failed, 0 passed.

| Check | Severity | Status | Violations | Amount |
|---|---|---|---:|---:|
| purchases_reach_stats | error | violations | 2 | 32.00 USD |
| payouts_match | warn | failed | - |  |

## purchases_reach_stats

Every purchase reaches the daily statistics.

Sources `billing` (left) and `stats` (right), severity error. 2 violations, 32.00 USD.

| id | difference | left | right | problem |
|---|---|---|---|---|
| 2026-10-13/USD | 2.00 USD | 20.00 USD | 22.00 USD | amount differs |
| 2026-10-14/USD | -30.00 USD | 30.00 USD | - | only left |

## payouts_match

Sources `ledger` (left) and `stats` (right). The check failed: right: more than max_rows (100000) rows; a side returns one row per key, grouped in the query
