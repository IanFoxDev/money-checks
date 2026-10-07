# 0001. Checks only read, and amounts are integers

Date: 2026-10-07. Status: accepted.

## Context

money-checks runs queries written by its users against a production database, usually
on a schedule and with credentials that someone copied from another service. Two
things can go wrong there that are worse than a missed violation.

The first is a write. A MongoDB pipeline with `$merge` or `$out`, or a SQL statement
that calls a function with side effects, changes the data the checks are supposed to
watch. Production users often have more privileges than they need, so "the
configuration only contains SELECT" is not a guarantee.

The second is a wrong amount. A report that says a check found 412 rows worth
$1,203.47 will be pasted into an incident or a planning document. If that number came
through a float and lost a cent, or if a value was silently rounded, people stop
trusting the report. Many databases that need these checks keep amounts badly:
doubles in MongoDB, decimal strings, `numeric` columns, integers in minor units.

## Decision

**The read-only guarantee comes from the database, not from the configuration.**

- PostgreSQL: every check runs in its own transaction opened with
  `BEGIN READ ONLY`, with `statement_timeout` set for that transaction. The server
  rejects writes, including writes from functions the query calls.
- MongoDB: a pipeline with a `$out` or `$merge` stage is rejected when the
  configuration is loaded. At start, money-checks reads the user's privileges with
  `connectionStatus` (`showPrivileges: true`). If the user can write to any
  database, the run stops, unless the source sets `require_read_only: false`, in
  which case it prints a warning on every run. Every aggregation has `maxTimeMS`.
  The read preference comes from the connection URI, so checks can read from a
  secondary.
- There is no setting that turns these off for PostgreSQL.

**An amount is an `int64` count of minor units plus a currency.**

- Integers are taken as minor units, or as major units when the check says so
  (`unit: major`), multiplied by the currency's exponent.
- Decimal strings, MongoDB `Decimal128` and PostgreSQL `numeric` are parsed digit by
  digit using the currency's exponent from ISO 4217 (JPY 0, EUR 2, KWD 3). More
  decimal places than the currency allows is an error for that check, unless the
  extra places are zeros.
- A double (common in MongoDB documents written from PHP or JavaScript) is converted
  to its shortest decimal representation (`strconv.FormatFloat(v, 'f', -1, 64)`) and
  then parsed like a string. `10.1` becomes 1010 cents. A double that does not fit
  the currency, such as `10.105` for USD, is an error for that check and names the
  document. It is never rounded.
- Totals are kept per currency. money-checks never converts between currencies.

**What leaves the database is limited.** A report and a Slack message contain the id
column, the amount and the columns the check lists in `show`. Values of `show`
columns listed in `mask` are masked. Other columns returned by the query are dropped
before anything is written.

## Consequences

- A user with write privileges cannot run checks against MongoDB until they create a
  read-only user or say explicitly that they accept the risk. That is friction on
  purpose: it is the first thing a security review of this tool would ask about.
- A collection with doubles that are not exact cents makes a check fail loudly
  instead of reporting a total that is off. The error says which document and which
  value, so the check can be fixed (for example by rounding in the pipeline, where
  the decision is visible) or the data can be looked at.
- Reports can be forwarded without sending personal data along with them, as long as
  `show` is written with care. The default shows only ids and amounts.
