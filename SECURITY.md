# Security

money-checks runs queries from its configuration against your databases, usually
production ones, with credentials taken from environment variables. It is built so
that it cannot change data:

- PostgreSQL checks run in `READ ONLY` transactions over the extended protocol.
- MongoDB pipelines with `$out` or `$merge` are rejected, and a user who can write is
  refused unless `require_read_only: false` is set.

Still, give it a user that can only read the collections and tables the checks use.

money-checks writes only the report files you name and, with Slack configured, posts
check names, counts and totals to the webhook. It listens on `serve.listen` for
`/metrics`, `/healthz` and `/readyz`; the metrics contain check names and totals, so
do not expose that port to the internet.

If you find a vulnerability, for example a query or a pipeline that changes data
through money-checks, a way around the privilege check, or a secret that ends up in
logs or reports, do not open a public issue. Report it privately through
[GitHub](https://github.com/IanFoxDev/money-checks/security/advisories/new), or write
to ianfoxdeveloper@gmail.com.

Reports contain ids and amounts from your data, and the columns you list under
`show`. Treat them as you treat the data they were built from.

## Supported versions

Fixes go into the latest release only. Until 1.0 that is the latest `0.x` tag.
