package source

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// mysqlEvents adds to the test database a table of 5000 events with an index on
// created_at and statistics, and opens it as the user who can only read.
func mysqlEvents(t *testing.T) Source {
	t.Helper()
	db := newMySQL(t)
	for _, q := range []string{
		"CREATE TABLE " + db.name + ".events (id int PRIMARY KEY, amount int, created_at datetime(6), KEY created_at (created_at))",
		"INSERT /*+ SET_VAR(cte_max_recursion_depth = 10000) */ INTO " + db.name + ".events " +
			"WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 5000) " +
			"SELECT i, i % 100, utc_timestamp(6) - interval i minute FROM n",
		"ANALYZE TABLE " + db.name + ".events",
	} {
		if _, err := db.admin.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return openMySQLEnv(t, "MC_MYSQL_READ", true)
}

func explainMySQL(t *testing.T, src Source, ch config.Check, since time.Time, analyze bool) Plan {
	t.Helper()
	p, err := src.(Explainer).Explain(context.Background(), ch, since, time.Time{}, analyze)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMySQLExplainWindowUsesTheIndex(t *testing.T) {
	src := mysqlEvents(t)
	ch := pgCheck("SELECT id FROM events WHERE created_at >= @since")
	ch.Window = time.Hour
	for _, analyze := range []bool{false, true} {
		p := explainMySQL(t, src, ch, time.Now().Add(-time.Hour), analyze)
		if len(p.Scans) != 0 || !slices.Equal(p.Indexes, []string{"created_at on events"}) {
			t.Fatalf("analyze %v: scans %v, indexes %v; want the index on created_at", analyze, p.Scans, p.Indexes)
		}
		if p.Rows < 50 || p.Rows > 70 || p.Analyzed != analyze || p.Stopped {
			t.Fatalf("analyze %v: rows %d, analyzed %v, stopped %v; want about 60", analyze, p.Rows, p.Analyzed, p.Stopped)
		}
	}
}

func TestMySQLExplainFullScan(t *testing.T) {
	src := mysqlEvents(t)
	ch := pgCheck("SELECT id FROM events WHERE amount = 7")
	p := explainMySQL(t, src, ch, time.Time{}, false)
	if !slices.Equal(p.Scans, []string{"events"}) || p.Rows < 4000 || p.Rows > 6000 {
		t.Fatalf("scans %v, rows %d; want events read in full, about 5000 rows", p.Scans, p.Rows)
	}
	if !strings.Contains(p.Raw, `"access_type": "ALL"`) {
		t.Fatalf("raw plan %s", p.Raw)
	}
	if p = explainMySQL(t, src, ch, time.Time{}, true); !slices.Equal(p.Scans, []string{"events"}) || p.Rows != 5000 {
		t.Fatalf("with analyze: scans %v, rows %d; want 5000 rows read", p.Scans, p.Rows)
	}
}

func TestMySQLExplainCountsLoops(t *testing.T) {
	src := mysqlEvents(t)
	// A dependent subquery for each of about 60 rows, each by the primary key.
	ch := pgCheck("SELECT e.id, (SELECT f.amount FROM events f WHERE f.id = e.id + 1) AS next FROM events e WHERE e.created_at >= @since")
	ch.Window = time.Hour
	for _, analyze := range []bool{false, true} {
		if p := explainMySQL(t, src, ch, time.Now().Add(-time.Hour), analyze); p.Rows < 100 || p.Rows > 140 || len(p.Scans) != 0 {
			t.Fatalf("analyze %v: rows %d, scans %v; want about 60 by the window and 60 by the key", analyze, p.Rows, p.Scans)
		}
	}
}

func TestMySQLExplainDoesNotRunTheQuery(t *testing.T) {
	src := mysqlEvents(t)
	ch := pgCheck("SELECT SLEEP(5) AS s FROM events")
	ch.Timeout = time.Second
	start := time.Now()
	explainMySQL(t, src, ch, time.Time{}, false)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("took %s; EXPLAIN alone must not run the query", time.Since(start))
	}
}

func TestMySQLExplainAnalyzeStoppedByTheTimeout(t *testing.T) {
	src := mysqlEvents(t)
	ch := pgCheck("SELECT count(*) AS n FROM events a, events b WHERE SLEEP(0.001) = 0")
	ch.Timeout = 500 * time.Millisecond
	start := time.Now()
	p := explainMySQL(t, src, ch, time.Time{}, true)
	if !p.Stopped {
		t.Fatalf("not stopped after %s; plan %s", time.Since(start), p.Raw)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestMySQLExplainSkipsMaterializedTables(t *testing.T) {
	src := mysqlEvents(t)
	ch := pgCheck("WITH recent AS (SELECT id FROM events WHERE created_at >= @since) SELECT count(*) AS n FROM recent")
	ch.Window = time.Hour
	for _, analyze := range []bool{false, true} {
		if p := explainMySQL(t, src, ch, time.Now().Add(-time.Hour), analyze); len(p.Scans) != 0 {
			t.Fatalf("analyze %v: scans %v; the CTE is not a table of the database\n%s", analyze, p.Scans, p.Raw)
		}
	}
}

// Both plans name a table by its alias. The connections go back to the pool
// with version 1 of EXPLAIN JSON, so the next plans read the same.
func TestMySQLExplainNamesTablesNotAliases(t *testing.T) {
	src := mysqlEvents(t)
	ch := pgCheck("SELECT e.id FROM events e JOIN events nxt ON nxt.id = e.id + 1 WHERE e.created_at >= @since")
	ch.Window = time.Hour
	for i := range 6 {
		analyze := i%2 == 1
		p := explainMySQL(t, src, ch, time.Now().Add(-time.Hour), analyze)
		if !slices.Contains(p.Indexes, "created_at on events") || !slices.Contains(p.Indexes, "PRIMARY on events") || len(p.Indexes) != 2 {
			t.Fatalf("run %d, analyze %v: indexes %v; want those of events", i, analyze, p.Indexes)
		}
	}
}
