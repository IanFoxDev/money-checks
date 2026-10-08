# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). Before 1.0, minor versions may break the
configuration, the report format or the metric names; such changes are marked
**BREAKING**.

## [Unreleased]

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

[Unreleased]: https://github.com/IanFoxDev/money-checks/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/IanFoxDev/money-checks/releases/tag/v0.1.0
