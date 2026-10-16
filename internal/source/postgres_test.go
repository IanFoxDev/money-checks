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
		skipWithoutDB(t, pgEnv+" is not set; make postgres-up starts a database for it")
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
	err := src.Query(context.Background(), ch, time.Time{}, time.Time{}, func(r Row) error {
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
// which takes one statement, whatever the DSN asks for. In pgx, Exec without
// arguments always uses the simple protocol, so this also fails if the check query
// ever goes through Exec. pgSchema checks after the test that no row is gone.
func TestPostgresRunsOneStatement(t *testing.T) {
	s := pgSchema(t)
	dsn := os.Getenv("MC_PG")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	for _, mode := range []string{"", "simple_protocol", "exec", "describe_exec", "cache_describe", "cache_statement"} {
		t.Run("mode="+mode, func(t *testing.T) {
			if mode == "" {
				t.Setenv("MC_PG", dsn)
			} else {
				t.Setenv("MC_PG", dsn+sep+"default_query_exec_mode="+mode)
			}
			src := openPG(t)
			for _, q := range []string{
				"select 1; commit; delete from " + s + ".payments",
				"commit; delete from " + s + ".payments returning id",
				"rollback; start transaction read write; delete from " + s + ".payments; commit",
			} {
				if _, err := collect(t, src, pgCheck(q)); err == nil {
					t.Errorf("%s: several statements ran", q)
				}
			}
		})
	}
}

// The start of the window is a bind parameter; PostgreSQL infers its type from
// the column, timestamptz or timestamp (read as UTC). The zero time reads every row.
func TestPostgresWindow(t *testing.T) {
	pgSchema(t)
	src := openPG(t)
	for _, typ := range []string{"timestamptz", "timestamp"} {
		ch := pgCheck(`select id from (values
			('old', ` + typ + ` '2026-10-01 00:00:00'),
			('new', ` + typ + ` '2026-10-08 05:00:00')
		) v(id, at) where at >= $1 order by id`)
		ch.Window = 24 * time.Hour
		ids := func(since time.Time) string {
			var got []string
			err := src.Query(context.Background(), ch, since, time.Time{}, func(r Row) error {
				got = append(got, r.Values[0].(string))
				return nil
			})
			if err != nil {
				t.Fatalf("%s: %v", typ, err)
			}
			return strings.Join(got, ",")
		}
		if got := ids(time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC)); got != "new" {
			t.Errorf("%s, window: %q", typ, got)
		}
		// The same instant in another zone.
		if got := ids(time.Date(2026, 10, 8, 7, 30, 0, 0, time.FixedZone("CYT", 3*3600))); got != "new" {
			t.Errorf("%s, window in +03: %q", typ, got)
		}
		if got := ids(time.Time{}); got != "new,old" {
			t.Errorf("%s, full: %q", typ, got)
		}
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
	err := openPG(t).Query(context.Background(), pgCheck("select id from "+s+".payments"), time.Time{}, time.Time{}, func(Row) error {
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

// skipWithoutDB skips a test that needs a database, or fails it when
// MONEY_CHECKS_REQUIRE_DB is set, as in CI: a green run must mean the database
// tests ran.
func skipWithoutDB(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("MONEY_CHECKS_REQUIRE_DB") != "" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

// settle: the end of the rows is $2, and $1 is sent even without a window.
func TestPostgresUntil(t *testing.T) {
	pgSchema(t)
	src := openPG(t)
	ch := pgCheck(`select id from (values
		('old', timestamptz '2026-10-01 00:00:00+00'),
		('new', timestamptz '2026-10-08 05:00:00+00'),
		('fresh', timestamptz '2026-10-08 05:55:00+00')
	) v(id, at) where at >= $1 and at < $2 order by id`)
	ch.Settle = 15 * time.Minute
	until := time.Date(2026, 10, 8, 8, 45, 0, 0, time.FixedZone("CYT", 3*3600))
	for _, tc := range []struct {
		window time.Duration
		since  time.Time
		want   string
	}{
		{0, time.Time{}, "new,old"},
		{24 * time.Hour, time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC), "new"},
	} {
		ch.Window = tc.window
		var got []string
		err := src.Query(context.Background(), ch, tc.since, until, func(r Row) error {
			got = append(got, r.Values[0].(string))
			return nil
		})
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Errorf("window %s: %q, %v; want %q", tc.window, got, err, tc.want)
		}
	}
}
