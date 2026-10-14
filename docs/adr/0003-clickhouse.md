# 0003. ClickHouse is read over HTTP with readonly set on every query

Date: 2026-10-14. Status: accepted.

## Context

Many teams keep a second copy of their money in ClickHouse: daily revenue, the
ledger behind partner payouts, the purchases a statistics service replicated from
the billing database. Two kinds of checks need it. Some invariants live inside
ClickHouse (the shares paid out for a period add up to the pool). Others compare
ClickHouse with the database the money came from, and those need ClickHouse as a
source first.

ClickHouse has no transactions, so the PostgreSQL approach from ADR 0001 (a READ ONLY
transaction per check) does not apply. It has a `readonly` setting instead:

- `readonly=1`: only reads, and no setting can be changed.
- `readonly=2`: only reads, settings can be changed, except `readonly` itself.

A user can get `readonly=1` from a settings profile. Such a user cannot pass any
setting with a query, not even `max_execution_time`, and a query that tries fails.

Two things were checked against ClickHouse 25.8 before this decision. With
`readonly=2` the server rejects INSERT, ALTER, DROP and SYSTEM, `SETTINGS readonly=0`
inside the query, and `SET readonly=0`. Over HTTP the server never runs two
statements joined with `;`. But in a query string `readonly=2&readonly=0` the last
value wins, so the setting has to be set by money-checks, not taken from the URL.

There are two protocols: HTTP (port 8123, or 8443 with TLS) and native (9000, 9440).
The Go driver for the native protocol brings its own dependency tree and returns
typed values that would have to be turned back into what a check needs.

## Decision

**HTTP, with the standard library.** The source is a URL in an environment variable
(`url_env`), like `dsn_env` for PostgreSQL, and the database is named in the
configuration. Every query is a POST with `default_format=TabSeparatedWithNames`.
In that format every value arrives as text, with no setting needed to get there, so it
works for a `readonly=1` user too: `Decimal`, `Int64` and `UInt64` are parsed digit by
digit, as `numeric` is on PostgreSQL. Nothing passes through a float that was not a
float in the table. A query with its own `FORMAT` clause is answered in that format;
the response header `X-ClickHouse-Format` shows it, and the check fails with an error
that says to remove the clause. Every query also has `wait_end_of_query=1`: without
it, an error after the first rows comes as HTTP 200 with the message after the rows,
and with it the answer is a 500.

**The read-only guarantee is a setting money-checks puts on every query.** On start
the source reads `getSetting('readonly')` for its user.

- `0` or `2`: every query is sent with `readonly=2`, `max_execution_time` equal to
  the check's timeout, and `cancel_http_readonly_queries_on_client_close=1`. These
  replace any value with the same name in the URL.
- `1`: the profile already forbids writes. Queries are sent with `readonly=1`, the
  only value of a setting such a user may send, so the timeout is only on the client
  side, and the source warns once that the server keeps running a query after the
  client gave up. The fix is `max_execution_time` in the user's profile.

**The user's grants are checked once, as for MongoDB.** When the user's `readonly`
is `0`, the source runs `CHECK GRANT` on the configured database for INSERT, ALTER
UPDATE, ALTER DELETE, ALTER DROP COLUMN, ALTER MODIFY COLUMN, ALTER MOVE PARTITION,
TRUNCATE, DROP TABLE, CREATE TABLE and OPTIMIZE. The parts of ALTER are checked one by
one because `CHECK GRANT ALTER` is 0 for a user who has only ALTER DELETE. These
statements are sent without `readonly`, since under it `CHECK GRANT` answers 0 for
everything. `CHECK GRANT` sees grants that come through roles. If any of them is
granted, the run stops, unless the source sets
`require_read_only: false`, in which case it warns on every run. The setting on each
query is what stops writes. The grant check tells the owner of the configuration
that the credentials are wider than they need to be.

**A window is a server-side parameter.** The query uses
`{since:DateTime64(6, 'UTC')}` and the start of the window goes in `param_since`. It
is never put into the query text.

**Values stay text, as on PostgreSQL.** `\N` is nil, everything else is the string
ClickHouse printed, with the TabSeparated escapes undone. An amount is parsed from that
text with the currency's exponent, so `Decimal`, `UInt64` and `Int128` are exact, and a
`Float64` is read in the shortest form ClickHouse prints: an amount that does not fit
the currency is an error for the check, never rounded. Dates and times are printed in
the column's time zone, or the server's when the column has none, as they would be by
`clickhouse-client`.

## Consequences

- No new module dependency. The HTTP client is small but it is ours: errors come
  from the response body, rows are streamed line by line, and a check that returns a
  million rows does not need a million rows in memory.
- A ClickHouse reachable only on the native port cannot be read. If that turns out
  to be common, a second implementation behind the same source can use the driver.
- Table functions such as `url()` and `remote()` still work under `readonly`: they
  read, but they reach other servers. Whether a check may use them is up to the
  grants (`READ ON URL`, `REMOTE`). The README says to give the user SELECT on one
  database and nothing else.
- A user with a `readonly=1` profile and no `max_execution_time` can leave a slow
  query running on the server after a timeout. money-checks says so on start rather
  than trying to work around it.
