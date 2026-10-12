# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). Before 1.0, minor versions may break the
configuration, the report format or the metric names; such changes are marked
**BREAKING**.

## [Unreleased]

## [0.2.0] - 2026-10-12

For the first run against a real database: rows that were looked at and accepted
go into a known file with a reason, and the alert is about new violations only. A
check can read the last day instead of the whole table, with a full pass once a day.

### Added

- `known_file`: violations that were looked at and accepted, each with a required
  reason, optionally with the amount it had and a date it stops applying. They do
  not count towards `violations`, the status, the exit code or Slack; the report
  lists them with their reasons and lists entries the check no longer finds. See
  `docs/adr/0002-known-violations.md`.
- The metric `money_check_known_violations{check}`.
- The JSON report has `known`, `known_totals`, `known_samples` and `resolved` for
  every check, and a `note` on samples that are in the known file but count again.
- `money-checks known add`: runs one check and appends its new violations to the
  known file, with a reason, the amounts and an optional `until`.
- `window` on a check: the query gets the start of the window as a bind parameter
  (`$1` on PostgreSQL, `$$since` on MongoDB 5.0+) and reads only recent rows. `serve`
  reads every row at start and every `serve.full_every` (24h), the window in
  between; `run --window` reads the window once. The JSON report has `scope`,
  `since` and `window` for every check.

### Changed

- **BREAKING** The gauges of what a check found, its last success and its duration
  have a `scope` label, `full` or `window`. Selectors keep working; a query that
  sums over checks should group by `scope` or take the `max`.
- **BREAKING** An ObjectId, a date or a decimal inside a document or an array (a
  compound `_id` from `$group`) is written as hex, RFC 3339 or a decimal string, as at
  the top level: `{"user":"652f..."}` instead of `{"user":{"$oid":"652f..."}}`. Ids in
  reports change for such checks.
- With a known file, `money_check_violations` and the totals count new violations
  only. Without one nothing changes.

## [0.1.0] - 2026-10-08

The first release: checks as MongoDB pipelines and PostgreSQL queries that must
return no rows, run once with a Markdown and JSON report or on a schedule with
Prometheus metrics and Slack messages. Read-only is enforced by the database, and
amounts never pass through a float. Checked end to end against the example shop in
CI, with the database tests required to run.

### Added

- `money-checks run -c checks.yaml`: runs every check once and writes a Markdown
  report (stdout or `--markdown`) and a JSON report (`--json`). Exit code 0 when no
  check with severity `error` found violations, 1 when one did, 2 when a check failed.
- `money-checks serve`: runs the checks at start and every `serve.interval`, serves
  `/metrics`, `/healthz` and `/readyz`, and posts to Slack when a check changes state.
- Sources: MongoDB aggregation pipelines (privileges checked at start, `$out` and
  `$merge` rejected, `maxTimeMS`, read preference from the URI) and PostgreSQL
  (`READ ONLY` transactions, extended protocol only, values read as text).
- Amounts as int64 minor units with ISO 4217 exponents, totals per currency; doubles
  are read exactly or the check fails. See
  [docs/adr/0001-read-only-and-money.md](docs/adr/0001-read-only-and-money.md).
- `show` and `mask` for the columns a report may contain.
- Docker image and the example in `examples/subscriptions`.

[Unreleased]: https://github.com/IanFoxDev/money-checks/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/IanFoxDev/money-checks/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/IanFoxDev/money-checks/releases/tag/v0.1.0
