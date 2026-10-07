# money-checks

Money invariants as code. Each check is a named query against your own database (a
MongoDB aggregation pipeline or SQL for PostgreSQL) that should return no rows. Every
row it does return is a violation, reported with its id and its amount in money.

Work in progress, nothing is released yet. The design decisions are in
[docs/adr](docs/adr).

## License

MIT
