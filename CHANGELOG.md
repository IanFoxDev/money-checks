# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). Before 1.0, minor versions may break the
configuration, the report format or the metric names; such changes are marked
**BREAKING**.

## [Unreleased]

### Added

- Two recipes for refunds and chargebacks, for PostgreSQL, MySQL and MongoDB.
  `refund_and_chargeback`: a payment refunded and charged back, or charged back
  twice because the reversal of a chargeback was written as a new one; a
  chargeback the merchant won does not count. `refund_on_wrong_charge`: a refund
  linked to another payment than the charge the provider named, such as the
  first charge of a subscription instead of the renewal it refunded.

## [0.7.0] - 2026-10-27

### Added

- MySQL source, 8.0 and later: `mysql: {dsn_env: ...}`. Before every check its
  connection is set again to `transaction_read_only = ON`, the check's timeout and
  UTC, because any user can turn these off and `START TRANSACTION READ ONLY` lets
  CREATE, DROP and TRUNCATE through. The window is `@since` and `@until`. A user
  with a privilege that writes stops the run unless `require_read_only: false`.
  See `docs/adr/0008-mysql-read-only.md`.
- `recipes/`: eleven checks to copy for PostgreSQL, MySQL, MongoDB and ClickHouse, a
  negative balance, an unbalanced ledger transaction, a balance that is not the
  sum of its entries, a provider transaction recorded twice, refunds over the
  payment, a payment in another currency than its order, a payment stuck in
  pending, an order paid and not fulfilled, a subscription active after its
  period, daily totals that differ between the app and ClickHouse, and duplicate
  rows in ClickHouse. Each is tested in CI against data with the bug planted and
  rows at the edge that must not fire.

## [0.6.1] - 2026-10-22

### Fixed

- A check that failed on its own, a timeout or a query error, and then ran again
  posted every violation it finds, as if they were new: "found 158 violations".
  It now posts what changed while it failed: "runs again, no new violations, 158
  in all", or the new and fixed ones. A state file written by 0.5, where the
  checks of a database that was away are failed, posts this once.

## [0.6.0] - 2026-10-22

### Changed

- `serve` posts one message per run instead of one per check: a single change as
  before, several under a first line such as "money-checks: 2 checks changed".
  Above 20 lines the message ends with "and N more". The first start with nine
  checks that are not clean was nine messages, now it is one. See ADR 0007.
- A source that cannot be reached is one line, "source stats is down, 4 checks did
  not run: ...", and "source stats is up again" when it is back, instead of a
  message per check. Its checks keep their violations meanwhile, so the return of
  the database does not post them again. After a query loses its connection, the
  other checks of that source do not run in that run; their error is "source stats
  is down: ...", in `run` too. Errors in Slack messages are cut to 200 characters.
- `serve` logs a warning of a source once, not on every run, and the first full
  run no longer logs every id as a new violation.

### Added

- `examples/kubernetes`: `serve` as one Deployment with the state file on a
  volume, a Secret for the URIs, a ServiceMonitor and the alerts of the README as
  a PrometheusRule. CI validates the manifests with kubeconform.
- `serve.retry`, 1m by default: the checks of a source that was down run again
  after 1m, 2m, 4m and so on up to `interval`, instead of waiting for the next
  run. Only those checks run, and the schedule of the others does not move.
- `money_check_source_up{source}` and the alert `MoneyCheckSourceDown` in the
  README.
- The example stops ClickHouse under `serve` in CI: one line when it goes down,
  one when a retry finds it back, and the old violations are not posted again.

## [0.5.0] - 2026-10-21

### Changed

- `serve` remembers the violations of each check by id and posts new ones also
  when the check already finds violations: "found 2 new violations (59.98 USD),
  151 in all". A changed amount makes a violation new. A full run posts the ones
  that are gone: "1 violation fixed (9.99 USD), 150 left". Before, a check that
  already had violations stayed silent about new ones. See ADR 0006.
- The alert in the README on checks that stopped running compares with
  `money_check_interval_seconds` instead of a fixed hour, so a check with `every`
  does not fire it.

### Added

- `serve.state_file`: the violations `serve` has seen are kept in a JSON file
  (mode 0600, written by rename), so a restart posts only what changed while it
  was down instead of every check that is not clean.
- Metrics `money_check_new_violations_total{check}` and
  `money_check_oldest_violation_age_seconds{check}`, for an alert on a violation
  that nobody fixes.
- `every` on a check: `serve` runs a heavy check every `every`, a multiple of
  `serve.interval`, instead of on every run; a full run still runs it.
  `money_check_interval_seconds{check}` says how often each check runs.
- The example restarts `serve` with a state file around a planted double charge in
  CI: the restart posts that one violation and nothing else.

### Fixed

- An entry of the known file for a check the configuration does not have is a
  warning and is left out, instead of an error that stops the run. One known file
  can now serve several configurations.
- An absolute path in `known_file`, `query_file` and `pipeline_file` was joined to
  the directory of the configuration and not found.
- A variable of the pipeline named `since` or `until`, bound by `let` of `$lookup`,
  `vars` of `$let` or `as` of `$map`, `$filter` and `$reduce`, was taken for the
  parameter of the window or of `settle`, and the configuration was refused.

## [0.4.1] - 2026-10-18

### Fixed

- `known add` refused to write when the known file had entries of other checks
  ("no check is named ..."): it checked the file against a configuration with the
  one check it ran. It now checks it against all of them. Affected every version
  since 0.2.0 with a known file shared by several checks.

## [0.4.0] - 2026-10-17

A check that sees no data no longer passes: an `expect` query must find rows first.
Checks can be tested against a database with known bugs, and every violation of a
check can be written to a CSV file for the people who fix them.

### Added

- `expect` on a check: a query that returns one number, such as a count of the rows
  the check reads, and must reach `min` (1 by default). Below it the check fails
  before its query runs, so a check whose query no longer sees any data does not
  pass. It runs against the same source with the same read-only rules and timeout,
  and gets the start of the window and the end of `settle` when it uses them. Each
  side of a `compare` check has its own. See `docs/adr/0005-expect-and-test.md`.
- `money_check_expect_value{check, scope, side}`, the Expect column of the Markdown
  report and `expect` in the JSON report: what the expect query found.
- The report warns about checks without `expect`; `defaults.require_expect: true`
  makes them a configuration error.
- `money-checks test -c checks.yaml -t tests.yaml`: runs the checks a tests file
  lists against a test database and compares the ids and amounts they find with
  the ones they must find. Exit code 0, 1 when a test fails, 2 when a check could
  not run. It only reads; the known file is not used, and checks run in full.
- `run --check NAME` runs one check; with `--csv file` or `--jsonl file` it writes
  every violation of that check, not only the samples, streamed as the database
  returns them. Values that a spreadsheet would run as a formula are prefixed with
  `'`. A failed check leaves no file.
- The example shop has an `expect` on every check and a `tests.yaml` with every
  planted violation; CI runs `money-checks test` with it, and shows that a check
  whose status value changed fails instead of passing.

### Changed

- The JSON report has `expect` in every check, an empty list for a check without
  one.
- `examples/subscriptions/expected.json` is replaced by `tests.yaml`.

## [0.3.0] - 2026-10-16

Money that lives in two databases: a check can compare the totals of MongoDB,
PostgreSQL or ClickHouse against each other, key by key, and wait for the rows that
are still on their way from one to the other. ClickHouse is a source of its own.

### Added

- ClickHouse sources (`clickhouse` with `url_env` and `database`), read over HTTP.
  Every check is sent with `readonly=2` and `max_execution_time`; at start the
  user's grants are checked with `CHECK GRANT`, and a user that can write stops the
  run unless `require_read_only: false`. A user whose profile sets `readonly=1` is
  accepted with a warning about the timeout. Values come back as text. See
  `docs/adr/0003-clickhouse.md`.
- `window` for ClickHouse checks: the query gets the start of the window as
  `{since:DateTime64(6, 'UTC')}`, sent as a server parameter. Another type for
  `since` is a configuration error.
- `compare` checks: two sides, each a query or a pipeline against any source,
  return totals by key, and a key only one side has, an amount that differs by more
  than `tolerance_minor` or a count that differs is a violation. The report shows
  both sides and the difference; known file entries match by key. JSON reports have
  `right_source` for these checks. See `docs/adr/0004-compare.md`.
- `settle` for `compare` checks: both sides read only the rows before `settle` ago,
  given to the query as `$2`, `$$until` or `{until:DateTime64(6, 'UTC')}`, so rows
  still on their way from one side to the other are not violations. The report
  and the JSON report show the end (`until`).
- The example shop has daily payment totals in ClickHouse and a `compare` check
  against the payments in MongoDB, with three planted differences.

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

[Unreleased]: https://github.com/IanFoxDev/money-checks/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/IanFoxDev/money-checks/compare/v0.6.1...v0.7.0
[0.6.1]: https://github.com/IanFoxDev/money-checks/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/IanFoxDev/money-checks/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/IanFoxDev/money-checks/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/IanFoxDev/money-checks/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/IanFoxDev/money-checks/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/IanFoxDev/money-checks/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/IanFoxDev/money-checks/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/IanFoxDev/money-checks/releases/tag/v0.1.0
