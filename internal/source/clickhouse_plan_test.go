package source

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// chEvents adds 5000 events ordered by at, in granules of 128 rows, and opens
// the database.
func chEvents(t *testing.T) (Source, string) {
	t.Helper()
	a, db := chDatabase(t)
	a.exec("CREATE TABLE " + db + ".events (id UInt32, amount UInt32, at DateTime64(6, 'UTC')) ENGINE = MergeTree ORDER BY at SETTINGS index_granularity = 128")
	a.exec("INSERT INTO " + db + ".events SELECT number, number % 100, now64(6) - toIntervalMinute(number) FROM numbers(5000)")
	return openCHAdmin(t, a, db), db
}

func explainCH(t *testing.T, src Source, ch config.Check, since time.Time, analyze bool) Plan {
	t.Helper()
	p, err := src.(Explainer).Explain(context.Background(), ch, since, time.Time{}, analyze)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClickHouseExplainWindowUsesThePrimaryKey(t *testing.T) {
	src, db := chEvents(t)
	ch := chCheck("SELECT id FROM events WHERE at >= {since:DateTime64(6, 'UTC')}")
	ch.Window = time.Hour
	p := explainCH(t, src, ch, time.Now().Add(-time.Hour), false)
	if len(p.Scans) != 0 || !slices.Contains(p.Indexes, "primary key (at) on "+db+".events") {
		t.Fatalf("scans %v, indexes %v; want the primary key", p.Scans, p.Indexes)
	}
	if p.Rows < 60 || p.Rows > 3*128 || p.Analyzed || len(p.Notes) != 0 {
		t.Fatalf("rows %d, analyzed %v, notes %v; want the rows of one or two granules", p.Rows, p.Analyzed, p.Notes)
	}
	if !strings.Contains(p.Raw, "ReadFromMergeTree") {
		t.Fatalf("raw plan %s", p.Raw)
	}
}

func TestClickHouseExplainFullScan(t *testing.T) {
	src, db := chEvents(t)
	ch := chCheck("SELECT id FROM events WHERE amount = 7")
	for _, analyze := range []bool{false, true} {
		p := explainCH(t, src, ch, time.Time{}, analyze)
		if !slices.Equal(p.Scans, []string{db + ".events"}) || p.Rows != 5000 || p.Analyzed {
			t.Fatalf("analyze %v: scans %v, rows %d, analyzed %v; want events read in full by the estimate", analyze, p.Scans, p.Rows, p.Analyzed)
		}
		if analyze && (len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "cannot run a query in explain")) {
			t.Fatalf("notes %v", p.Notes)
		}
	}
}

// A count() that the metadata of the parts answers reads no rows, and the summary
// says why it names no table.
func TestClickHouseExplainCountFromMetadata(t *testing.T) {
	a, db := chDatabase(t)
	a.exec("CREATE TABLE " + db + ".days (d Date, p UInt8, v UInt32) ENGINE = MergeTree PARTITION BY (p, toYYYYMM(d)) ORDER BY d")
	a.exec("INSERT INTO " + db + ".days SELECT toDate('2026-01-01') + number % 300, number % 3, number FROM numbers(10000)")
	src := openCHAdmin(t, a, db)
	for _, q := range []string{"SELECT count() FROM days", "SELECT count() FROM days WHERE p = 1", "SELECT count() FROM days WHERE d >= '2026-05-01'"} {
		p := explainCH(t, src, chCheck(q), time.Time{}, false)
		if len(p.Scans) != 0 || len(p.Indexes) != 0 || p.Rows != 0 || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "metadata of the parts") {
			t.Errorf("%s: scans %v, indexes %v, rows %d, notes %v", q, p.Scans, p.Indexes, p.Rows, p.Notes)
		}
	}
	if p := explainCH(t, src, chCheck("SELECT count() FROM days WHERE v > 5"), time.Time{}, false); !slices.Equal(p.Scans, []string{db + ".days"}) || len(p.Notes) != 0 {
		t.Errorf("v > 5: scans %v, notes %v; want days read in full", p.Scans, p.Notes)
	}
}

// ClickHouse runs the subquery of IN to choose the parts to read. Without
// analyze it must not.
func TestClickHouseExplainDoesNotRunInSubqueries(t *testing.T) {
	src, _ := chEvents(t)
	ch := chCheck("SELECT id FROM events WHERE at IN (SELECT now64(6) - toIntervalSecond(number) FROM numbers(10000000000))")
	ch.Timeout = time.Second
	start := time.Now()
	p := explainCH(t, src, ch, time.Time{}, false)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %s; the subquery ran", d)
	}
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "subqueries did not run") {
		t.Fatalf("notes %v", p.Notes)
	}
	start = time.Now()
	if _, err := src.(Explainer).Explain(context.Background(), ch, time.Time{}, time.Time{}, true); err == nil {
		t.Fatal("with analyze the subquery ran past the timeout without an error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("with analyze: stopped after %s", d)
	}
}
