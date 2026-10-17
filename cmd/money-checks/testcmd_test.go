package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// byName is a source that returns the rows set for each check name.
type byName map[string][]source.Row

func (s byName) Query(_ context.Context, ch config.Check, _, _ time.Time, each func(source.Row) error) error {
	for _, r := range s[ch.Name] {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

func (byName) Close(context.Context) error { return nil }

const testConf = `
version: 1
known_file: known.yaml
sources: {ledger: {postgres: {dsn_env: MC_TEST_UNUSED}}}
checks:
  - name: refund_twice
    source: ledger
    query: q
    id: id
    amount: {field: amount, unit: major, currency_field: currency}
  - name: clean
    source: ledger
    query: q where at >= $1
    id: id
    window: 24h
    expect: {query: select count(*) from t where at >= $1}
  - name: untested
    source: ledger
    query: q
    id: id
`

func testCLI(t *testing.T, src byName, tests string) (code int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"checks.yaml": testConf, "tests.yaml": tests} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	var out, errOut bytes.Buffer
	code = run(context.Background(), []string{"test", "-c", filepath.Join(dir, "checks.yaml"), "-t", filepath.Join(dir, "tests.yaml")}, &out, &errOut, check.Opener(open), fixed)
	return code, out.String(), errOut.String()
}

const tests = `
version: 1
tests:
  - check: refund_twice
    violations:
      - {id: p1, amount: 10.00 USD}
      - {id: p2}
  - check: clean
    violations: []
`

func TestTestPasses(t *testing.T) {
	src := byName{
		"refund_twice": {refund("p2", "3.50", "EUR"), refund("p1", "10", "USD")},
		"clean expect": {source.Row{Columns: []string{"count"}, Values: []any{"12"}}},
		"untested":     {refund("u1", "1", "USD")},
	}
	// No known file exists: tests do not read it.
	code, out, errOut := testCLI(t, src, tests)
	want := "ok    refund_twice: 2 violations\nok    clean: 0 violations\n\nnot tested: untested\n\n2 tests: 2 passed, 0 failed, 0 could not run\n"
	if code != exitOK || out != want {
		t.Errorf("exit %d\n%s\nstderr %s", code, out, errOut)
	}
}

func TestTestFails(t *testing.T) {
	src := byName{
		"refund_twice": {refund("p1", "9.00", "USD"), refund("p9", "3.00", "USD")},
		"clean expect": {source.Row{Columns: []string{"count"}, Values: []any{"12"}}},
		"clean":        {refund("c1", "1", "USD")},
	}
	code, out, _ := testCLI(t, src, tests)
	for _, want := range []string{
		"FAIL  refund_twice: 2 violations, want 2 violations\n" +
			"        amount  p1 9.00 USD, want 10.00 USD\n" +
			"        missing p2\n" +
			"        extra   p9 3.00 USD\n",
		"FAIL  clean: 1 violation, want 0 violations\n        extra   c1\n",
		"2 tests: 0 passed, 2 failed, 0 could not run\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if code != exitFound {
		t.Errorf("exit %d", code)
	}
}

// A test database without data for an expect fails the test: the expect query is
// tested too.
func TestTestExpectFails(t *testing.T) {
	src := byName{"refund_twice": {refund("p1", "10", "USD"), refund("p2", "1", "USD")}}
	code, out, _ := testCLI(t, src, tests)
	if code != exitError || !strings.Contains(out, "ERROR clean: expect found 0, at least 1 required\n") || !strings.Contains(out, "1 passed, 0 failed, 1 could not run") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestTestTooManyRows(t *testing.T) {
	many := make([]source.Row, maxTestRows+1)
	for i := range many {
		many[i] = refund("p"+strconv.Itoa(i), "1", "USD")
	}
	src := byName{"refund_twice": many, "clean expect": {source.Row{Columns: []string{"count"}, Values: []any{"1"}}}}
	code, out, _ := testCLI(t, src, tests)
	if code != exitError || !strings.Contains(out, "ERROR refund_twice: more than 10000 rows; a test database should be small") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestTestBadFile(t *testing.T) {
	code, out, errOut := testCLI(t, byName{}, "version: 1\ntests: [{check: nope, violations: []}]\n")
	if code != exitError || out != "" || !strings.Contains(errOut, `tests[0]: check: no check is named "nope"`) {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// End to end on PostgreSQL: a window check is tested in full, with $1 at the
// start of a full pass, and its expect query runs as in production.
func TestTestPostgres(t *testing.T) {
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
	s := fmt.Sprintf("mc_test_%d", time.Now().UnixNano())
	for _, q := range []string{
		"create schema " + s,
		"create table " + s + ".refunds (payment_id text, amount numeric(12,2), currency text, created_at timestamptz)",
		"insert into " + s + ".refunds values ('p1', 10.99, 'USD', '2025-01-01'), ('p1', 10.99, 'USD', '2025-01-02'), ('p2', 5, 'EUR', '2025-01-01')",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _, _ = conn.Exec(ctx, "drop schema "+s+" cascade") }()
	t.Setenv("MC_TEST_PG", dsn)
	dir := t.TempDir()
	conf := `
version: 1
sources: {ledger: {postgres: {dsn_env: MC_TEST_PG}}}
checks:
  - name: refund_twice
    source: ledger
    window: 24h
    query: |
      select payment_id, sum(amount) as amount, currency from ` + s + `.refunds
      where payment_id in (select payment_id from ` + s + `.refunds where created_at >= $1)
      group by payment_id, currency having count(*) > 1
    id: payment_id
    amount: {field: amount, unit: major, currency_field: currency}
    expect: {query: select count(*) from ` + s + `.refunds where created_at >= $1}
`
	for name, body := range map[string]string{
		"checks.yaml": conf,
		"tests.yaml":  "version: 1\ntests:\n  - check: refund_twice\n    violations: [{id: p1, amount: 21.98 USD}]\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := runCLI(t, "test", "-c", filepath.Join(dir, "checks.yaml"), "-t", filepath.Join(dir, "tests.yaml"))
	if code != exitOK || !strings.Contains(out, "ok    refund_twice: 1 violation\n") {
		t.Errorf("exit %d\n%s\nstderr %s", code, out, errOut)
	}
}
