#!/bin/sh
# Loads stats.sql into the stats database and creates the user money-checks
# connects as, with SELECT on stats only. Runs once, from docker-entrypoint-initdb.d.
set -e
client() { clickhouse-client --host 127.0.0.1 -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" "$@"; }
client -q "create database stats"
client --database stats --multiquery < /stats.sql
client --multiquery -q "create user checks identified by 'checks'; grant select on stats.* to checks"
