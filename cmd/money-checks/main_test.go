package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ianfoxdev/money-checks/internal/source"
)

func fixed() time.Time { return time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC) }

func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, &out, &errOut, source.Open, fixed)
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	if code, out, _ := runCLI(t, "version"); code != exitOK || out != "dev\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}} {
		if code, _, errOut := runCLI(t, args...); code != exitError || !strings.Contains(errOut, "usage:") {
			t.Errorf("%v: exit %d, %q", args, code, errOut)
		}
	}
}

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "checks.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunBadConfig(t *testing.T) {
	path := writeConfig(t, "version: 1\nsurces: {}\n")
	code, out, errOut := runCLI(t, "run", "-c", path)
	if code != exitError || out != "" || !strings.Contains(errOut, "surces") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// A source that cannot be opened fails its checks; the report is still written.
func TestRunFailedSource(t *testing.T) {
	t.Setenv("MC_CLI_EMPTY", "")
	path := writeConfig(t, `
version: 1
sources: {ledger: {postgres: {dsn_env: MC_CLI_EMPTY}}}
checks:
  - {name: refund_twice, source: ledger, query: select 1, id: id}
`)
	jsonPath := filepath.Join(t.TempDir(), "r.json")
	code, out, errOut := runCLI(t, "run", "-c", path, "--json", jsonPath)
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(out, "| refund_twice | error | failed | - |  |") || !strings.Contains(errOut, "check refund_twice failed: source ledger: environment variable MC_CLI_EMPTY is empty") {
		t.Errorf("stdout %q\nstderr %q", out, errOut)
	}
	var r struct {
		Summary struct{ Failed int }
	}
	b, err := os.ReadFile(jsonPath)
	if err != nil || json.Unmarshal(b, &r) != nil || r.Summary.Failed != 1 {
		t.Errorf("json %s, %v", b, err)
	}
}

// End to end on PostgreSQL: the exit code follows the severity of what was found.
func TestRunPostgres(t *testing.T) {
	dsn := os.Getenv("MONEY_CHECKS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("MONEY_CHECKS_TEST_PG_DSN is not set; make postgres-up starts a database for it")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	s := fmt.Sprintf("mc_cli_%d", time.Now().UnixNano())
	for _, q := range []string{
		"create schema " + s,
		"create table " + s + ".refunds (id text, payment_id text, amount numeric(12,2), currency text, email text)",
		"insert into " + s + ".refunds values ('r1', 'p1', 10.99, 'USD', 'anna@example.com'), ('r2', 'p1', 10.99, 'USD', 'anna@example.com'), ('r3', 'p2', 5, 'EUR', 'bob@example.com')",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = conn.Exec(ctx, "drop schema "+s+" cascade") }()
	t.Setenv("MC_CLI_PG", dsn)
	conf := `
version: 1
sources: {ledger: {postgres: {dsn_env: MC_CLI_PG}}}
checks:
  - name: refund_twice
    source: ledger
    severity: SEVERITY
    query: |
      select payment_id, sum(amount) as amount, currency, min(email) as email
      from ` + s + `.refunds group by payment_id, currency having count(*) > 1
    id: payment_id
    amount: {field: amount, unit: major, currency_field: currency}
    show: [email]
    mask: [email]
`
	code, out, errOut := runCLI(t, "run", "-c", writeConfig(t, strings.Replace(conf, "SEVERITY", "error", 1)))
	if code != 1 {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"| refund_twice | error | violations | 1 | 21.98 USD |",
		"| p1 | 21.98 USD | a***@example.com |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "anna@") {
		t.Error("the email is not masked")
	}
	if code, _, _ := runCLI(t, "run", "-c", writeConfig(t, strings.Replace(conf, "SEVERITY", "warn", 1))); code != exitOK {
		t.Errorf("warn: exit %d", code)
	}
}

func TestServeConfigErrors(t *testing.T) {
	if code, _, errOut := runCLI(t, "serve", "-c", writeConfig(t, "version: 1\nsurces: {}\n")); code != exitError || !strings.Contains(errOut, "surces") {
		t.Errorf("bad config: exit %d, %q", code, errOut)
	}
	t.Setenv("MC_CLI_SLACK", "")
	path := writeConfig(t, `
version: 1
sources: {ledger: {postgres: {dsn_env: MC_CLI_EMPTY}}}
checks:
  - {name: refund_twice, source: ledger, query: select 1, id: id}
serve: {slack: {webhook_env: MC_CLI_SLACK}}
`)
	if code, _, errOut := runCLI(t, "serve", "-c", path); code != exitError || !strings.Contains(errOut, "serve.slack: environment variable MC_CLI_SLACK is empty") {
		t.Errorf("missing webhook: exit %d, %q", code, errOut)
	}
}
