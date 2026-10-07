package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ianfoxdev/money-checks/internal/config"
)

const pgEnv = "MONEY_CHECKS_TEST_PG_DSN"

// pgSchema creates a schema with a payments table owned by the test and returns
// its name. The source connects with the same DSN: the point is that the
// transaction, not the user, stops writes.
func pgSchema(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(pgEnv)
	if dsn == "" {
		t.Skip(pgEnv + " is not set; make postgres-up starts a database for it")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	s := fmt.Sprintf("mc_%d", time.Now().UnixNano())
	for _, q := range []string{
		"create schema " + s,
		"create table " + s + ".payments (id text primary key, amount_minor bigint, amount numeric(12,2), amount_f float8, currency text)",
		"insert into " + s + ".payments values ('p1', 1099, 10.99, 10.1, 'USD'), ('p2', null, 0.29, 0.29, 'EUR')",
		"create function " + s + ".sneaky() returns int language plpgsql as $$ begin delete from " + s + ".payments; return 1; end $$",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		var n int
		if err := conn.QueryRow(ctx, "select count(*) from "+s+".payments").Scan(&n); err != nil || n != 2 {
			t.Errorf("payments changed: %d rows, %v", n, err)
		}
		_, _ = conn.Exec(ctx, "drop schema "+s+" cascade")
	})
	t.Setenv("MC_PG", dsn)
	return s
}

func openPG(t *testing.T) Source {
	t.Helper()
	src, err := Open(context.Background(), "ledger", config.Source{Postgres: &config.Postgres{DSNEnv: "MC_PG"}}, noWarn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close(context.Background()) })
	return src
}

func noWarn(string) {}

func pgCheck(query string) config.Check {
	return config.Check{Name: "c", Query: query, Timeout: 5 * time.Second}
}

func collect(t *testing.T, src Source, ch config.Check) ([]Row, error) {
	t.Helper()
	var rows []Row
	err := src.Query(context.Background(), ch, func(r Row) error {
		rows = append(rows, r)
		return nil
	})
	return rows, err
}

func TestPostgresReadsValuesAsText(t *testing.T) {
	s := pgSchema(t)
	rows, err := collect(t, openPG(t), pgCheck("select id, amount_minor, amount, amount_f, currency from "+s+".payments order by id"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	want := [][]any{{"p1", "1099", "10.99", "10.1", "USD"}, {"p2", nil, "0.29", "0.29", "EUR"}}
	for i, r := range rows {
		if strings.Join(r.Columns, ",") != "id,amount_minor,amount,amount_f,currency" {
			t.Fatalf("columns %v", r.Columns)
		}
		for j, v := range r.Values {
			if v != want[i][j] {
				t.Errorf("row %d column %s = %#v, want %#v", i, r.Columns[j], v, want[i][j])
			}
		}
	}
	v, _ := rows[0].Get("amount")
	if n, err := Minor(v, "major", 2); err != nil || n != 1099 {
		t.Errorf("Minor(numeric) = %d, %v", n, err)
	}
	v, _ = rows[0].Get("amount_f")
	if n, err := Minor(v, "major", 2); err != nil || n != 1010 {
		t.Errorf("Minor(float8) = %d, %v", n, err)
	}
}

func TestPostgresRefusesWrites(t *testing.T) {
	s := pgSchema(t)
	src := openPG(t)
	for _, q := range []string{
		"delete from " + s + ".payments returning id",
		"with d as (delete from " + s + ".payments returning id) select id from d",
		"select " + s + ".sneaky()",
		"insert into " + s + ".payments (id) values ('p3') returning id",
	} {
		_, err := collect(t, src, pgCheck(q))
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "25006" { // read_only_sql_transaction
			t.Errorf("%s: want a read-only error, got %v", q, err)
		}
	}
}

// With the simple protocol, "select 1; commit; delete" would commit the read-only
// transaction and delete in autocommit. The source forces the extended protocol,
// which takes one statement.
func TestPostgresIgnoresSimpleProtocolInDSN(t *testing.T) {
	s := pgSchema(t)
	dsn := os.Getenv("MC_PG")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	t.Setenv("MC_PG", dsn+sep+"default_query_exec_mode=simple_protocol")
	_, err := collect(t, openPG(t), pgCheck("select 1; commit; delete from "+s+".payments"))
	if err == nil {
		t.Fatal("several statements ran")
	}
}

func TestPostgresTimeout(t *testing.T) {
	pgSchema(t)
	ch := pgCheck("select pg_sleep(5)")
	ch.Timeout = 200 * time.Millisecond
	start := time.Now()
	if _, err := collect(t, openPG(t), ch); err == nil {
		t.Fatal("no error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestPostgresStopsOnCallbackError(t *testing.T) {
	s := pgSchema(t)
	stop := errors.New("stop")
	n := 0
	err := openPG(t).Query(context.Background(), pgCheck("select id from "+s+".payments"), func(Row) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Errorf("err %v after %d rows", err, n)
	}
}

func TestOpenNamesMissingVariable(t *testing.T) {
	t.Setenv("MC_PG_EMPTY", "")
	_, err := Open(context.Background(), "ledger", config.Source{Postgres: &config.Postgres{DSNEnv: "MC_PG_EMPTY"}}, noWarn)
	if err == nil || !strings.Contains(err.Error(), "source ledger") || !strings.Contains(err.Error(), "MC_PG_EMPTY") {
		t.Errorf("got %v", err)
	}
}
