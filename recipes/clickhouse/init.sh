#!/bin/sh
# Loads analytics.sql into the analytics database and creates the user
# money-checks connects as, with SELECT on analytics only. Runs once, from
# docker-entrypoint-initdb.d.
set -e
client() { clickhouse-client --host 127.0.0.1 -u "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" "$@"; }
client -q "create database analytics"
client --database analytics --multiquery < /analytics.sql
client --multiquery -q "create user checks identified by 'checks'; grant select on analytics.* to checks"
