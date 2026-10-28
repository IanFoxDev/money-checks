package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestExplainArgs(t *testing.T) {
	path := writeConfig(t, `
version: 1
sources: {ledger: {postgres: {dsn_env: MC_CLI_EMPTY}}}
checks:
  - {name: refund_twice, source: ledger, query: select 1, id: id}
`)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--check", "nope"}, `explain: no check is named "nope"`},
		{[]string{"--max-rows", "-1"}, "--max-rows must be 0 or more"},
		{[]string{"--nope"}, "flag provided but not defined"},
	} {
		code, out, errOut := runCLI(t, append([]string{"explain", "-c", path}, c.args...)...)
		if code != exitError || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
	// A source that cannot be opened fails its queries, as in run.
	t.Setenv("MC_CLI_EMPTY", "")
	code, out, _ := runCLI(t, "explain", "-c", path)
	if code != exitError || !strings.Contains(out, "error: source ledger: environment variable MC_CLI_EMPTY is empty") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// On PostgreSQL: a window lets the index on the date work, the full pass reads
// the table, and --max-rows turns that into exit code 1.
func TestExplainPostgres(t *testing.T) {
	dsn := os.Getenv("MONEY_CHECKS_TEST_PG_DSN")
	if dsn == "" {
		skipWithoutDB(t, "MONEY_CHECKS_TEST_PG_DSN is not set; make postgres-up starts a database for it")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	s := fmt.Sprintf("mc_explain_%d", time.Now().UnixNano())
	for _, q := range []string{
		"create schema " + s,
		"create table " + s + ".refunds (id int, amount numeric(12,2), created_at timestamptz)",
		"insert into " + s + ".refunds select i, i % 100, timestamptz '2026-10-08 06:00Z' - i * interval '1 minute' from generate_series(1, 5000) i",
		"create index refunds_created_at on " + s + ".refunds (created_at)",
		"analyze " + s + ".refunds",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = conn.Exec(ctx, "drop schema "+s+" cascade") }()
	t.Setenv("MC_CLI_PG", dsn)
	path := writeConfig(t, `
version: 1
sources: {ledger: {postgres: {dsn_env: MC_CLI_PG}}}
checks:
  - name: big_refund
    source: ledger
    window: 1h
    query: select id from `+s+`.refunds where created_at >= $1 and amount > 97.50
    id: id
`)
	code, out, errOut := runCLI(t, "explain", "-c", path, "--window", "--max-rows", "1000")
	if code != exitOK || strings.Contains(out, "full scan") || !strings.Contains(out, "index: refunds_created_at on "+s+".refunds") {
		t.Errorf("window: exit %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "explain", "-c", path, "--max-rows", "1000")
	if code != exitFound || !strings.Contains(out, "full scan: "+s+".refunds") || !strings.Contains(out, "rows estimated: 5000") || !strings.Contains(out, "over --max-rows 1000") {
		t.Errorf("full: exit %d\n%s\n%s", code, out, errOut)
	}
	// The constants of the query are in the plan, which stays out of the text.
	if strings.Contains(out, "97.50") {
		t.Errorf("the plan is in the text output\n%s", out)
	}
	code, out, errOut = runCLI(t, "explain", "-c", path, "--analyze")
	if code != exitOK || !strings.Contains(out, "rows read: 5000") {
		t.Errorf("analyze: exit %d\n%s\n%s", code, out, errOut)
	}
}
