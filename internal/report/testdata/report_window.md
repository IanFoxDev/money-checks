# money-checks report

Run at 2026-10-08 06:00:00 UTC. 4 checks: 2 found violations, 1 failed, 1 passed.

Warning: source billing: mongodb: the user can write to shop (insert); running anyway because require_read_only is false

| Check | Severity | Status | Violations | Amount | Rows |
|---|---|---|---:|---:|---|
| two_rebills_in_one_period | error | violations | 3 | 5.00 EUR, 37.09 USD | last 24h |
| balance_matches_journal | error | ok | 0 |  | all |
| paid_without_access | warn | violations | 1 |  | all |
| refund_twice | error | failed | - |  | all |

## two_rebills_in_one_period

A subscription was charged twice for the same period.

Source `billing`, severity error, rows since 2026-10-07 06:00 UTC (last 24h). 3 violations, 5.00 EUR, 37.09 USD.

| id | amount | email | note |
|---|---|---|---|
| {"subscription":"s1","period":"2026-09"} | 26.99 USD | a***@example.com | a\|b c |
| {"subscription":"s2","period":"2026-09"} | 10.10 USD | b***@example.com |  |

Showing 2 of 3.

## paid_without_access

Source `billing`, severity warn. 1 violation.

| id |
|---|
| p9 |

## refund_twice

Source `ledger`. The check failed: source ledger: environment variable LEDGER_DSN is empty
