# 0008. MySQL is read in a read-only session, set again before every check

Date: 2026-10-26. Status: accepted.

## Context

Many PHP applications keep their money in MySQL: Laravel and Symfony projects,
WooCommerce and Magento shops, billing services written years ago. The recipes in
`recipes/` are plain SQL, and most of them would run on MySQL with small changes. What
is missing is a source that keeps the promise of ADR 0001: money-checks never writes,
whatever the query says and whatever the credentials allow.

ADR 0001 runs every PostgreSQL check in a `BEGIN READ ONLY` transaction. The same
statement exists in MySQL, `START TRANSACTION READ ONLY`, and it looks like the
answer. It is not. Checked against MySQL 8.4.13 with a user that has ALL on the
database:

- Inside `START TRANSACTION READ ONLY`, INSERT, a stored function that inserts, and
  `SELECT ... FOR UPDATE` fail with error 1792.
- `CREATE TABLE`, `DROP TABLE` and `TRUNCATE TABLE` inside the same transaction
  succeed. DDL commits the open transaction implicitly and then runs outside it. The
  table was dropped, the other one emptied.
- With `SET SESSION transaction_read_only = ON` instead, every one of these fails
  with error 1792: INSERT, CREATE, DROP, TRUNCATE, `FOR UPDATE`.
- Any user can turn it off again: `SET SESSION transaction_read_only = OFF` needs no
  privilege. A check is one statement, so a check whose query is that `SET` would
  leave the connection writable for the next check.

Two more things were checked on the same server:

- `max_execution_time` can be set by any user for the session, and a SELECT that
  runs longer fails with error 3024. It applies to SELECT only, which is all a check
  may run under a read-only session anyway.
- A `TIMESTAMP` column compared with a `DATETIME` value reads the value in the
  session's time zone. With the session at `+03:00` and the window starting at
  12:00 UTC, a row written at 10:00 UTC was inside the window. With the session at
  `+00:00` it was not.

MySQL has `?` placeholders only. A window query that needs the start twice cannot
say `$1` twice, as on PostgreSQL, and positional `?` would make the order of the
parameters part of the configuration.

## Decision

**Every check runs on one connection that money-checks prepares just before it.**
The source takes a connection from the pool and sends, in one statement:

```sql
SET SESSION transaction_read_only = ON, max_execution_time = <timeout in ms>,
    time_zone = '+00:00'
```

then the window values, then the query, all on that connection. The session is set
again before every check, so whatever the previous check did to it does not carry
over. No `START TRANSACTION`: the session setting already covers what the
transaction would, and a single SELECT reads one consistent snapshot by itself.

**One statement per query, enforced.** The DSN is parsed by the driver
(`go-sql-driver/mysql`) and money-checks sets `multiStatements=false` and
`interpolateParams=false` whatever the DSN says. A query that tries `;` and a
second statement fails, and parameters always go to the server as parameters,
never as text in the query.

**The window is two user variables.** For a check with `window` the source runs
`SET @since = CAST(? AS DATETIME(6))`, and with `settle` also `@until`, as a prepared
statement with the UTC time as the parameter. The query uses `@since` and `@until` as
many times as it needs. The configuration check requires `@since` in a query with
`window` and refuses it in one without, as it does with `$1` on PostgreSQL. Because
the session is in UTC, `@since` means the same instant against `TIMESTAMP` and
against a `DATETIME` column written in UTC. A full pass sets `@since` to
`1000-01-01 00:00:00`, the earliest `DATETIME`.

**The user's grants are checked once, as on MongoDB and ClickHouse.** On start the
source reads `SHOW GRANTS`, which in MySQL 8 includes the privileges of the active
roles. A privilege that writes, on `*.*`, on the database of the DSN, or on a table
in it, stops the run: `ALL PRIVILEGES`, INSERT, UPDATE, DELETE, CREATE, DROP, ALTER,
INDEX, CREATE VIEW, CREATE ROUTINE, ALTER ROUTINE, TRIGGER, EVENT, FILE. With
`require_read_only: false` it is a warning on every run instead. As on ClickHouse,
the session setting is what stops writes; the grant check tells the owner of the
configuration that the credentials can do more than they need to.

**Values stay text.** The driver gives `DECIMAL`, `BIGINT` and dates as bytes when
`parseTime` is off, which money-checks also forces. An amount is parsed from that
text with the currency's exponent, so `DECIMAL(20,2)` is exact, and a `DOUBLE` goes
through the shortest form, as on the other sources.

**MySQL 8.0 and later.** `transaction_read_only` replaced `tx_read_only` in 8.0,
and 5.7 is out of support. MariaDB has the setting but calls the timeout
`max_statement_time` and counts it in seconds; it is not supported until someone
asks, and the source refuses a server whose version says MariaDB rather than
running without a timeout.

## Why not

- **`START TRANSACTION READ ONLY` per check**, as on PostgreSQL. DDL commits it and
  runs anyway, as shown above. Adding it on top of the session setting changes
  nothing that the session setting does not already do.
- **Rely on the grants alone.** A user who can only SELECT cannot write, but the
  check of grants is advisory with `require_read_only: false`, and the privileges
  of a role that is granted but not active show in `SHOW GRANTS` only as the role's
  name, until `SET ROLE` activates it. The session setting holds regardless.
- **`super_read_only` or a replica.** A replica with `super_read_only` is the right
  place to run checks, and the README says so. It is a server setting that
  money-checks cannot set or require.
- **Positional `?` in the query.** Every window query would have to pass the start
  once per use, in order, and the configuration would have to say how many.

## Consequences

- One extra round trip per check for the `SET`, two with a window. Checks run every
  few minutes, not every millisecond.
- A check can still read anything its user can read, `mysql.*` included if granted.
  The README says to grant SELECT on one database and nothing else.
- `@since` and `@until` are taken. A query that uses its own user variable with
  either name gets the window value instead; the configuration check cannot tell.
