package source

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// pgEvents creates a schema with a table of 5000 events, an index on created_at
// and statistics, and a table "fresh" that was never analyzed.
func pgEvents(t *testing.T) string {
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
		"create table " + s + ".events (id int primary key, amount int, created_at timestamptz)",
		"insert into " + s + ".events select i, i % 100, now() - i * interval '1 minute' from generate_series(1, 5000) i",
		"create index on " + s + ".events (created_at)",
		"analyze " + s + ".events",
		"create table " + s + ".fresh (id int)",
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = conn.Exec(ctx, "drop schema "+s+" cascade") })
	t.Setenv("MC_PG", dsn)
	return s
}

func explainPG(t *testing.T, src Source, ch config.Check, since time.Time, analyze bool) Plan {
	t.Helper()
	p, err := src.(Explainer).Explain(context.Background(), ch, since, time.Time{}, analyze)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPostgresExplainWindowUsesTheIndex(t *testing.T) {
	s := pgEvents(t)
	src := openPG(t)
	ch := pgCheck("select id from " + s + ".events where created_at >= $1")
	ch.Window = time.Hour
	p := explainPG(t, src, ch, time.Now().Add(-time.Hour), false)
	if len(p.Scans) != 0 || !slices.Contains(p.Indexes, "events_created_at_idx on "+s+".events") {
		t.Fatalf("scans %v, indexes %v; want the index on created_at", p.Scans, p.Indexes)
	}
	if p.Rows < 30 || p.Rows > 200 || p.Analyzed || len(p.Notes) != 0 {
		t.Fatalf("rows %d, analyzed %v, notes %v; want about 60 estimated", p.Rows, p.Analyzed, p.Notes)
	}
	if !strings.Contains(p.Raw, `"Node Type"`) {
		t.Fatalf("raw plan %q", p.Raw)
	}
}

func TestPostgresExplainFullScan(t *testing.T) {
	s := pgEvents(t)
	src := openPG(t)
	ch := pgCheck("select id from " + s + ".events where amount = 7")
	p := explainPG(t, src, ch, time.Time{}, false)
	if !slices.Equal(p.Scans, []string{s + ".events"}) || p.Rows != 5000 {
		t.Fatalf("scans %v, rows %d; want events read in full, 5000 rows by its statistics", p.Scans, p.Rows)
	}
	p = explainPG(t, src, ch, time.Time{}, true)
	if !p.Analyzed || p.Rows != 5000 {
		t.Fatalf("analyzed %v, rows %d; want 5000 rows read", p.Analyzed, p.Rows)
	}
}

func TestPostgresExplainCountsLoops(t *testing.T) {
	s := pgEvents(t)
	src := openPG(t)
	// A correlated subquery for each of about 60 rows, each by the primary key.
	ch := pgCheck("select e.id, (select f.amount from " + s + ".events f where f.id = e.id + 1) from " + s + ".events e where e.created_at >= $1")
	ch.Window = time.Hour
	p := explainPG(t, src, ch, time.Now().Add(-time.Hour), true)
	if p.Rows < 100 || p.Rows > 130 || len(p.Scans) != 0 {
		t.Fatalf("rows %d, scans %v; want about 60 by the window and 60 by the key", p.Rows, p.Scans)
	}
}

func TestPostgresExplainSaysTheStatisticsAreMissing(t *testing.T) {
	s := pgEvents(t)
	src := openPG(t)
	p := explainPG(t, src, pgCheck("select id from "+s+".fresh"), time.Time{}, false)
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], s+".fresh has no statistics") {
		t.Fatalf("notes %v", p.Notes)
	}
	if p = explainPG(t, src, pgCheck("select id from "+s+".fresh"), time.Time{}, true); len(p.Notes) != 0 || p.Rows != 0 {
		t.Fatalf("with analyze: notes %v, rows %d", p.Notes, p.Rows)
	}
}

func TestPostgresExplainDoesNotRunTheQuery(t *testing.T) {
	s := pgEvents(t)
	src := openPG(t)
	ch := pgCheck("select pg_sleep(5) from " + s + ".fresh")
	ch.Timeout = time.Second
	start := time.Now()
	explainPG(t, src, ch, time.Time{}, false)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("took %s; EXPLAIN alone must not run the query", time.Since(start))
	}
	ch = pgCheck("select pg_sleep(5)")
	ch.Timeout = 300 * time.Millisecond
	if _, err := src.(Explainer).Explain(context.Background(), ch, time.Time{}, time.Time{}, true); err == nil {
		t.Fatal("EXPLAIN ANALYZE of a query longer than the timeout succeeded")
	}
}

func TestPostgresExplainRefusesWrites(t *testing.T) {
	s := pgEvents(t)
	src := openPG(t)
	ch := pgCheck("delete from " + s + ".events")
	if _, err := src.(Explainer).Explain(context.Background(), ch, time.Time{}, time.Time{}, true); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("EXPLAIN ANALYZE of a delete: %v; want the read-only transaction to refuse it", err)
	}
}
