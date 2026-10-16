package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

const chEnv = "MONEY_CHECKS_TEST_CLICKHOUSE_URL"

// chAdmin runs statements as the user from MONEY_CHECKS_TEST_CLICKHOUSE_URL, who
// can create databases and users.
type chAdmin struct {
	t   *testing.T
	url string
}

func (a chAdmin) exec(q string) string {
	a.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, a.url, strings.NewReader(q))
	if err != nil {
		a.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		a.t.Fatalf("%s: %s", q, b)
	}
	return strings.TrimSpace(string(b))
}

// chDatabase creates a database with a payments table and returns the admin and
// the database name. After the test it checks that no row changed.
func chDatabase(t *testing.T) (chAdmin, string) {
	t.Helper()
	u := os.Getenv(chEnv)
	if u == "" {
		skipWithoutDB(t, chEnv+" is not set; make clickhouse-up starts a server for it")
	}
	a := chAdmin{t: t, url: u}
	db := fmt.Sprintf("mc_%d", time.Now().UnixNano())
	a.exec("CREATE DATABASE " + db)
	a.exec("CREATE TABLE " + db + ".payments (id String, amount Decimal(18, 2), amount_f Float64, big UInt64, " +
		"note Nullable(String), paid_at DateTime64(3, 'UTC')) ENGINE = MergeTree ORDER BY id")
	a.exec("INSERT INTO " + db + ".payments VALUES ('p1', 10.99, 10.1, 18446744073709551615, 'tab\there\nand \\\\', '2026-10-13 10:00:00.123'), " +
		"('p2', 0.29, 0.29, 1, NULL, '2026-10-01 00:00:00')")
	t.Cleanup(func() {
		if n := a.exec("SELECT count() FROM " + db + ".payments"); n != "2" {
			t.Errorf("payments changed: %s rows", n)
		}
		a.exec("DROP DATABASE " + db)
	})
	return a, db
}

// user creates a user with the given grants and returns its name and a URL for it.
func (a chAdmin) user(db, settings string, grants ...string) (string, string) {
	a.t.Helper()
	name := fmt.Sprintf("mc_user_%d", time.Now().UnixNano())
	a.exec("CREATE USER " + name + " IDENTIFIED BY 'secret-pass' " + settings)
	a.t.Cleanup(func() { a.exec("DROP USER IF EXISTS " + name) })
	for _, g := range grants {
		a.exec("GRANT " + g + " ON " + db + ".* TO " + name)
	}
	u, err := url.Parse(a.url)
	if err != nil {
		a.t.Fatal(err)
	}
	u.User = url.UserPassword(name, "secret-pass")
	return name, u.String()
}

func openCH(t *testing.T, rawURL, db string, requireReadOnly bool) (Source, []string, error) {
	t.Helper()
	t.Setenv("MC_CH", rawURL)
	var warns []string
	src, err := Open(context.Background(), "stats", config.Source{ClickHouse: &config.ClickHouse{
		URLEnv: "MC_CH", Database: db, RequireReadOnly: &requireReadOnly,
	}}, func(m string) { warns = append(warns, m) })
	if err == nil {
		t.Cleanup(func() { _ = src.Close(context.Background()) })
	}
	return src, warns, err
}

// openCHAdmin connects as the admin, who can write, so the setting on each query
// is the only thing that stops writes.
func openCHAdmin(t *testing.T, a chAdmin, db string) Source {
	t.Helper()
	src, warns, err := openCH(t, a.url, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "the user can write to "+db) {
		t.Errorf("want a warning about write grants, got %q", warns)
	}
	return src
}

func chCheck(query string) config.Check {
	return config.Check{Name: "c", Query: query, Timeout: 5 * time.Second}
}

func TestClickHouseReadsValuesAsText(t *testing.T) {
	a, db := chDatabase(t)
	rows, err := collect(t, openCHAdmin(t, a, db), chCheck("SELECT id, amount, amount_f, big, note, paid_at FROM payments ORDER BY id"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]any{
		{"p1", "10.99", "10.1", "18446744073709551615", "tab\there\nand \\", "2026-10-13 10:00:00.123"},
		{"p2", "0.29", "0.29", "1", nil, "2026-10-01 00:00:00.000"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	for i, r := range rows {
		if strings.Join(r.Columns, ",") != "id,amount,amount_f,big,note,paid_at" {
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
		t.Errorf("Minor(Decimal) = %d, %v", n, err)
	}
	v, _ = rows[0].Get("amount_f")
	if n, err := Minor(v, "major", 2); err != nil || n != 1010 {
		t.Errorf("Minor(Float64) = %d, %v", n, err)
	}
}

func TestClickHouseRefusesWrites(t *testing.T) {
	a, db := chDatabase(t)
	// A readonly=0 in the URL must not win over the source's own setting.
	sep := "?"
	if strings.Contains(a.url, "?") {
		sep = "&"
	}
	for _, rawURL := range []string{a.url, a.url + sep + "readonly=0"} {
		src, _, err := openCH(t, rawURL, db, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			"INSERT INTO payments (id) VALUES ('p3')",
			"ALTER TABLE payments DELETE WHERE 1",
			"DELETE FROM payments WHERE 1",
			"TRUNCATE TABLE payments",
			"DROP TABLE payments",
			"SELECT 1 SETTINGS readonly = 0",
			"SET readonly = 0",
		} {
			_, err := collect(t, src, chCheck(q))
			if err == nil || !strings.Contains(err.Error(), "readonly") {
				t.Errorf("%s with %s: want a readonly error, got %v", q, rawURL, err)
			}
		}
		if _, err := collect(t, src, chCheck("SELECT 1; DROP TABLE payments")); err == nil {
			t.Error("two statements ran")
		}
	}
}

func TestClickHouseGrants(t *testing.T) {
	a, db := chDatabase(t)

	_, selectOnly := a.user(db, "", "SELECT")
	_, warns, err := openCH(t, selectOnly, db, true)
	if err != nil || len(warns) != 0 {
		t.Errorf("SELECT only: %v, %q", err, warns)
	}

	role := fmt.Sprintf("mc_role_%d", time.Now().UnixNano())
	a.exec("CREATE ROLE " + role)
	t.Cleanup(func() { a.exec("DROP ROLE IF EXISTS " + role) })
	a.exec("GRANT ALTER DELETE ON " + db + ".* TO " + role)
	name, viaRole := a.user(db, "", "SELECT")
	a.exec("GRANT " + role + " TO " + name)
	a.exec("SET DEFAULT ROLE " + role + " TO " + name)
	_, _, err = openCH(t, viaRole, db, true)
	if err == nil || !strings.Contains(err.Error(), "the user can write to "+db+" (ALTER DELETE") {
		t.Errorf("grant through a role: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret-pass") {
		t.Errorf("the password is in the error: %v", err)
	}
	_, warns, err = openCH(t, viaRole, db, false)
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "running anyway") {
		t.Errorf("require_read_only false: %v, %q", err, warns)
	}
}

// A profile with readonly=1 forbids sending any setting, max_execution_time too.
func TestClickHouseReadonlyProfile(t *testing.T) {
	a, db := chDatabase(t)
	_, readonly := a.user(db, "SETTINGS readonly = 1", "SELECT", "INSERT")
	src, warns, err := openCH(t, readonly, db, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "max_execution_time") {
		t.Errorf("want a warning about the timeout, got %q", warns)
	}
	rows, err := collect(t, src, chCheck("SELECT id FROM payments ORDER BY id"))
	if err != nil || len(rows) != 2 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	if _, err := collect(t, src, chCheck("INSERT INTO payments (id) VALUES ('p3')")); err == nil {
		t.Error("insert ran")
	}
}

func TestClickHouseTimeout(t *testing.T) {
	a, db := chDatabase(t)
	ch := chCheck("SELECT sleep(3)")
	ch.Timeout = time.Second
	start := time.Now()
	_, err := collect(t, openCHAdmin(t, a, db), ch)
	if err == nil {
		t.Fatal("no error")
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("took %s", d)
	}
}

// The start of the window is a server parameter in UTC with microseconds, whatever
// the time zone of the session. The zero time reads every row.
func TestClickHouseWindow(t *testing.T) {
	a, db := chDatabase(t)
	u, err := url.Parse(a.url)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("session_timezone", "Asia/Nicosia")
	u.RawQuery = q.Encode()
	src, _, err := openCH(t, u.String(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	_, readonly := a.user(db, "SETTINGS readonly = 1", "SELECT")
	ro, _, err := openCH(t, readonly, db, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"SELECT id FROM payments WHERE paid_at >= {since:DateTime64(6, 'UTC')} ORDER BY id",
		// A DateTime column, which starts in 1970.
		"SELECT id FROM payments WHERE toDateTime(paid_at) >= { since : DateTime64(6, 'UTC') } ORDER BY id",
		// toDate of a time before 1970 wraps around to 2079.
		"SELECT id FROM payments WHERE toDate(paid_at) >= toDate({since:DateTime64(6, 'UTC')}) ORDER BY id",
	} {
		ch := chCheck(query)
		ch.Window = 24 * time.Hour
		ids := func(src Source, since time.Time) string {
			var got []string
			err := src.Query(context.Background(), ch, since, time.Time{}, func(r Row) error {
				got = append(got, r.Values[0].(string))
				return nil
			})
			if err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			return strings.Join(got, ",")
		}
		for _, tc := range []struct {
			since time.Time
			want  string
		}{
			{time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC), "p1"},
			// The same instant in another zone.
			{time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("CYT", 3*3600)), "p1"},
			{time.Time{}, "p1,p2"},
		} {
			if got := ids(src, tc.since); got != tc.want {
				t.Errorf("%s, since %s: %q, want %q", query, tc.since, got, tc.want)
			}
			if got := ids(ro, tc.since); got != tc.want {
				t.Errorf("%s, readonly=1, since %s: %q, want %q", query, tc.since, got, tc.want)
			}
		}
	}
	// Microseconds reach the server.
	ch := chCheck("SELECT id FROM payments WHERE paid_at >= {since:DateTime64(6, 'UTC')}")
	ch.Window = time.Hour
	for since, want := range map[time.Time]int{
		time.Date(2026, 10, 13, 10, 0, 0, 123000000, time.UTC): 1,
		time.Date(2026, 10, 13, 10, 0, 0, 123001000, time.UTC): 0,
	} {
		n := 0
		err := src.Query(context.Background(), ch, since, time.Time{}, func(Row) error { n++; return nil })
		if err != nil || n != want {
			t.Errorf("since %s: %d rows, %v; want %d", since.Format(time.RFC3339Nano), n, err, want)
		}
	}
}

// settle: the end of the rows is {until:...}, in UTC whatever the session zone, and
// works under readonly=1.
func TestClickHouseUntil(t *testing.T) {
	a, db := chDatabase(t)
	u, err := url.Parse(a.url)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("session_timezone", "Asia/Nicosia")
	u.RawQuery = q.Encode()
	src, _, err := openCH(t, u.String(), db, false)
	if err != nil {
		t.Fatal(err)
	}
	_, readonly := a.user(db, "SETTINGS readonly = 1", "SELECT")
	ro, _, err := openCH(t, readonly, db, true)
	if err != nil {
		t.Fatal(err)
	}
	// p1 is paid at 2026-10-13 10:00:00.123, p2 on 2026-10-01.
	for _, tc := range []struct {
		query  string
		window time.Duration
		until  time.Time
		want   string
	}{
		{"SELECT id FROM payments WHERE paid_at < {until:DateTime64(6, 'UTC')} ORDER BY id", 0,
			time.Date(2026, 10, 13, 10, 0, 0, 123001000, time.UTC), "p1,p2"},
		{"SELECT id FROM payments WHERE paid_at < {until:DateTime64(6, 'UTC')} ORDER BY id", 0,
			time.Date(2026, 10, 13, 13, 0, 0, 123000000, time.FixedZone("CYT", 3*3600)), "p2"},
		{"SELECT id FROM payments WHERE paid_at >= {since:DateTime64(6, 'UTC')} AND paid_at < {until:DateTime64(6, 'UTC')} ORDER BY id", 24 * time.Hour,
			time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), "p1"},
	} {
		ch := chCheck(tc.query)
		ch.Window, ch.Settle = tc.window, 15*time.Minute
		for _, s := range []Source{src, ro} {
			var got []string
			err := s.Query(context.Background(), ch, time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), tc.until, func(r Row) error {
				got = append(got, r.Values[0].(string))
				return nil
			})
			if err != nil || strings.Join(got, ",") != tc.want {
				t.Errorf("%s, until %s: %q, %v; want %q", tc.query, tc.until, got, err, tc.want)
			}
		}
	}
}

// An error after some rows were computed must fail the check, not end the result.
func TestClickHouseErrorInTheMiddle(t *testing.T) {
	a, db := chDatabase(t)
	_, err := collect(t, openCHAdmin(t, a, db), chCheck("SELECT number AS id, throwIf(number = 70000) FROM numbers(100000)"))
	if err == nil || !strings.Contains(err.Error(), "Code: 395") {
		t.Errorf("got %v", err)
	}
}

func TestClickHouseFormatClause(t *testing.T) {
	a, db := chDatabase(t)
	_, err := collect(t, openCHAdmin(t, a, db), chCheck("SELECT id FROM payments FORMAT JSON"))
	if err == nil || !strings.Contains(err.Error(), "remove the FORMAT clause") {
		t.Errorf("got %v", err)
	}
}

func TestClickHouseURL(t *testing.T) {
	for _, raw := range []string{"clickhouse://u:secret-pass@host:9000/", "host:8123", "http:///x"} {
		_, _, err := openCH(t, raw, "stats", true)
		if err == nil || !strings.Contains(err.Error(), "MC_CH must be an http or https URL") || strings.Contains(err.Error(), "secret-pass") {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestUnescapeTSV(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:             "plain",
		`a\tb\nc`:           "a\tb\nc",
		`back\\s`:           `back\s`,
		`q\'x`:              "q'x",
		`trailing\`:         `trailing\`,
		`\0\b\f\r`:          "\x00\b\f\r",
		`DateTime(\'UTC\')`: "DateTime('UTC')",
	} {
		if got := unescapeTSV(in); got != want {
			t.Errorf("unescapeTSV(%q) = %q, want %q", in, got, want)
		}
	}
}
