# 0004. compare checks two totals, not two tables of rows

Date: 2026-10-16. Status: accepted.

## Context

Some invariants do not live in one database. Purchases are written to MongoDB by the
billing service and reach a ClickHouse table of daily totals through Kafka. When a
consumer skips a batch, or a retry writes it twice, the dashboard and the payouts
built on ClickHouse are wrong, while each database on its own looks fine. No query
against one of them can find it.

Copying the rows of both sides into money-checks and joining them there does not
work for this: a day of purchases is millions of rows, and the check runs every ten
minutes against production.

## Decision

**Each side returns totals, the check compares them by key.** A `compare` check has
a `left` and a `right` side, each a query or a pipeline against any source, the two
sources can be the same. Each side groups its rows in its own database, by day, by
partner, by currency, and returns one row per key. money-checks reads both results
and compares them row by row.

- `key` names the columns both sides return. The id of a violation is the values of
  the key joined with `/`: `2026-10-13/USD`. The key must be the same text on both
  sides; a date that one side prints as `2026-10-13` and the other as
  `2026-10-13T00:00:00Z` makes every row a violation, which is loud, not silent.
- `amount` is read on both sides with the same unit and currency rule, into minor
  units of the currency, before the comparison. Amounts are never compared as
  floating point numbers. When the currency comes from a column, that column must be
  part of the key, so the two amounts of a key are always in one currency.
- `counts` are integer columns that must be equal, such as the number of purchases.
  A count catches a purchase written twice with a refund of the same amount, which
  the sum alone misses.
- `tolerance_minor` allows a difference of the amount up to that many minor units.
  The default is 0.

**A violation is a key** that only one side returns, whose amounts differ by more
than the tolerance, or whose counts differ. Its amount is the difference, right
minus left; the totals and `money_check_amount_minor` add up the absolute
differences, so they say how much money does not match. The report shows both sides
and the difference for each key.

**Known violations work by key.** An entry with the id `2026-10-13/USD` and an
amount accepts that difference; if the difference changes, it is news, as for any
check with an amount.

**Each side has a row limit**, `max_rows`, 100000 by default. More rows is an error
of the check, never a silent cut: a side that returns raw rows instead of totals
would otherwise make the comparison wrong and slow. A key returned twice by one side
is an error too; the side has to group by the key.

**The sides run one after another**, as all checks do, so each database gets one
query at a time. The left side is held in memory, the right side is streamed past
it.

## Consequences

- The two sides are read at different moments, and the data behind them is written
  at different moments. Fresh rows always differ. `settle` leaves the last minutes
  out of both sides: the queries get an end, `settle` before the start of the run,
  as a parameter next to the start of the window. Only `compare` checks have it; a
  check of one database has no second side to wait for.
- The key has to be produced as the same text on both sides, and that is the
  author's work: `$dateToString` in MongoDB, `toString(day)` in ClickHouse.
- One `amount` serves both sides. A side whose amounts are in a different unit
  converts them in its query. Different units per side can be added when a real
  configuration needs them.
- `show` and `mask` do not apply: the rows are totals, there is nothing personal in
  them.
