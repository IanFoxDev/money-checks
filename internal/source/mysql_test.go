package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/ianfoxdev/money-checks/internal/config"
)

const mysqlEnv = "MONEY_CHECKS_TEST_MYSQL_DSN"

// mysqlDB is a database with a payments table, and two users: MC_MYSQL_READ
// can only SELECT, MC_MYSQL_WRITE has ALL on the database. The point of the
// writer is that the session, not the user, stops writes.
type mysqlDB struct {
	name  string
	admin *sql.DB
}

func newMySQL(t *testing.T) mysqlDB {
	t.Helper()
	dsn := os.Getenv(mysqlEnv)
	if dsn == "" {
		skipWithoutDB(t, mysqlEnv+" is not set; make mysql-up starts a database for it")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx := context.Background()
	name := fmt.Sprintf("mc_%d", time.Now().UnixNano())
	reader, writer := name+"_r", name+"_w"
	for _, q := range []string{
		"CREATE DATABASE " + name,
		"CREATE TABLE " + name + ".payments (id varchar(10) PRIMARY KEY, amount_minor bigint, amount decimal(12,2), " +
			"amount_f double, currency char(3), paid_at timestamp(6) NULL, booked_at datetime(6) NULL)",
		"INSERT INTO " + name + ".payments VALUES ('p1', 1099, 10.99, 10.1, 'USD', '2026-10-01 10:00:00', '2026-10-01 10:00:00'), " +
			"('p2', NULL, 0.29, 0.29, 'EUR', NULL, NULL)",
		"CREATE FUNCTION " + name + ".sneaky() RETURNS int DETERMINISTIC MODIFIES SQL DATA BEGIN DELETE FROM " + name + ".payments; RETURN 1; END",
		"CREATE USER '" + reader + "'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON " + name + ".* TO '" + reader + "'@'%'",
		"CREATE USER '" + writer + "'@'%' IDENTIFIED BY 'pw'",
		"GRANT ALL ON " + name + ".* TO '" + writer + "'@'%'",
	} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		var n int
		if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM "+name+".payments").Scan(&n); err != nil || n != 2 {
			t.Errorf("payments changed: %d rows, %v", n, err)
		}
		for _, q := range []string{"DROP DATABASE " + name, "DROP USER '" + reader + "'@'%'", "DROP USER '" + writer + "'@'%'"} {
			_, _ = admin.ExecContext(ctx, q)
		}
	})
	as := func(user string) string {
		c := cfg.Clone()
		c.User, c.Passwd, c.DBName = user, "pw", name
		return c.FormatDSN()
	}
	t.Setenv("MC_MYSQL_READ", as(reader))
	t.Setenv("MC_MYSQL_WRITE", as(writer))
	return mysqlDB{name: name, admin: admin}
}

func openMySQLEnv(t *testing.T, env string, require bool) Source {
	t.Helper()
	src, err := Open(context.Background(), "shop", config.Source{MySQL: &config.MySQL{DSNEnv: env, RequireReadOnly: &require}}, noWarn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close(context.Background()) })
	return src
}

func TestMySQLReadsValuesAsText(t *testing.T) {
	newMySQL(t)
	rows, err := collect(t, openMySQLEnv(t, "MC_MYSQL_READ", true),
		pgCheck("SELECT id, amount_minor, amount, amount_f, currency, paid_at FROM payments ORDER BY id"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]any{
		{"p1", "1099", "10.99", "10.1", "USD", "2026-10-01 10:00:00.000000"},
		{"p2", nil, "0.29", "0.29", "EUR", nil},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	for i, r := range rows {
		if strings.Join(r.Columns, ",") != "id,amount_minor,amount,amount_f,currency,paid_at" {
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
		t.Errorf("Minor(decimal) = %d, %v", n, err)
	}
	v, _ = rows[0].Get("amount_f")
	if n, err := Minor(v, "major", 2); err != nil || n != 1010 {
		t.Errorf("Minor(double) = %d, %v", n, err)
	}
}

// The writer has ALL on the database. DDL is what START TRANSACTION READ ONLY
// lets through; the session setting stops it too. newMySQL checks afterwards
// that both rows are still there.
func TestMySQLRefusesWrites(t *testing.T) {
	db := newMySQL(t)
	src := openMySQLEnv(t, "MC_MYSQL_WRITE", false)
	for _, q := range []string{
		"DELETE FROM payments",
		"INSERT INTO payments (id) VALUES ('p3')",
		"SELECT sneaky()",
		"SELECT id FROM payments FOR UPDATE",
		"TRUNCATE TABLE payments",
		"DROP TABLE payments",
		"CREATE TABLE ev (id int)",
		"CREATE TABLE ev AS SELECT * FROM payments",
	} {
		_, err := collect(t, src, pgCheck(q))
		var myErr *mysql.MySQLError
		if !errors.As(err, &myErr) || myErr.Number != 1792 { // ER_CANT_EXECUTE_IN_READ_ONLY_TRANSACTION
			t.Errorf("%s: want a read-only error, got %v", q, err)
		}
	}
	var n int
	if err := db.admin.QueryRowContext(context.Background(), "SELECT count(*) FROM information_schema.tables WHERE table_schema = ?", db.name).Scan(&n); err != nil || n != 1 {
		t.Errorf("tables in %s: %d, %v", db.name, n, err)
	}
}

// Any user can turn the setting off. The next check gets it back on, also on
// the same connection: there is only one when MaxOpenConns would be 1, and two
// here, so run it a few times.
func TestMySQLSetsTheSessionEveryTime(t *testing.T) {
	newMySQL(t)
	src := openMySQLEnv(t, "MC_MYSQL_WRITE", false)
	for range 4 {
		if _, err := collect(t, src, pgCheck("SET SESSION transaction_read_only = OFF, time_zone = '+03:00', @since = '2000-01-01'")); err != nil {
			t.Fatal(err)
		}
		if _, err := collect(t, src, pgCheck("DELETE FROM payments")); err == nil {
			t.Fatal("DELETE ran after the previous check turned read-only off")
		}
		rows, err := collect(t, src, pgCheck("SELECT @@session.time_zone AS tz, @since AS since, @@session.transaction_read_only AS ro"))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(rows[0].Values); got != "[+00:00 <nil> 1]" {
			t.Errorf("session after a check changed it: %s", got)
		}
	}
}

// multiStatements in the DSN would let "SELECT 1; DELETE ..." run both. The
// source turns it off whatever the DSN says.
func TestMySQLRunsOneStatement(t *testing.T) {
	newMySQL(t)
	t.Setenv("MC_MYSQL_MULTI", os.Getenv("MC_MYSQL_WRITE")+"?multiStatements=true&interpolateParams=true")
	src := openMySQLEnv(t, "MC_MYSQL_MULTI", false)
	for _, q := range []string{
		"SELECT 1; DELETE FROM payments",
		"SELECT 1; SET SESSION transaction_read_only = OFF; DELETE FROM payments",
	} {
		var myErr *mysql.MySQLError
		if _, err := collect(t, src, pgCheck(q)); !errors.As(err, &myErr) || myErr.Number != 1064 { // syntax error
			t.Errorf("%s: want a syntax error, got %v", q, err)
		}
	}
}

func TestMySQLRefusesAWriter(t *testing.T) {
	db := newMySQL(t)
	_, err := Open(context.Background(), "shop", config.Source{MySQL: &config.MySQL{DSNEnv: "MC_MYSQL_WRITE"}}, noWarn)
	if err == nil || !strings.Contains(err.Error(), "ALL PRIVILEGES ON `"+db.name+"`.*") || !strings.Contains(err.Error(), "require_read_only") {
		t.Errorf("want the writer refused, got %v", err)
	}
	var warned []string
	no := false
	src, err := Open(context.Background(), "shop", config.Source{MySQL: &config.MySQL{DSNEnv: "MC_MYSQL_WRITE", RequireReadOnly: &no}},
		func(msg string) { warned = append(warned, msg) })
	if err != nil {
		t.Fatal(err)
	}
	_ = src.Close(context.Background())
	if len(warned) != 1 || !strings.HasPrefix(warned[0], "source shop: mysql: the user can write") {
		t.Errorf("warnings %q", warned)
	}
}

// A role that writes counts once it is active, which a default role is.
func TestMySQLRefusesAWritingRole(t *testing.T) {
	db := newMySQL(t)
	role := db.name + "_role"
	reader := db.name + "_r"
	for _, q := range []string{
		"CREATE ROLE '" + role + "'",
		"GRANT INSERT ON " + db.name + ".payments TO '" + role + "'",
		"GRANT '" + role + "' TO '" + reader + "'@'%'",
		"SET DEFAULT ROLE ALL TO '" + reader + "'@'%'",
	} {
		if _, err := db.admin.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { _, _ = db.admin.ExecContext(context.Background(), "DROP ROLE '"+role+"'") })
	_, err := Open(context.Background(), "shop", config.Source{MySQL: &config.MySQL{DSNEnv: "MC_MYSQL_READ"}}, noWarn)
	if err == nil || !strings.Contains(err.Error(), "INSERT ON `"+db.name+"`.`payments`") {
		t.Errorf("want the role refused, got %v", err)
	}
}

// The session is in UTC whatever the DSN or the server says, so @since is the
// same instant against TIMESTAMP and against a DATETIME written in UTC. p1 was
// paid at 10:00 UTC; with the session at +03:00 a window from 12:00 UTC would
// find it.
func TestMySQLWindow(t *testing.T) {
	newMySQL(t)
	t.Setenv("MC_MYSQL_TZ", os.Getenv("MC_MYSQL_READ")+"?time_zone=%27%2B03%3A00%27")
	src := openMySQLEnv(t, "MC_MYSQL_TZ", true)
	ch := pgCheck("SELECT id FROM payments WHERE paid_at >= @since OR booked_at >= @since")
	ch.Window = time.Hour
	ids := func(since, until time.Time, ch config.Check) string {
		t.Helper()
		var got []string
		err := src.Query(context.Background(), ch, since, until, func(r Row) error {
			got = append(got, r.Values[0].(string))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ",")
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 1, h, m, 0, 0, time.UTC) }
	if got := ids(at(12, 0), time.Time{}, ch); got != "" {
		t.Errorf("window from 12:00 UTC: %q, want nothing", got)
	}
	if got := ids(at(9, 59).In(time.FixedZone("x", 3*3600)), time.Time{}, ch); got != "p1" {
		t.Errorf("window from 09:59 UTC: %q, want p1", got)
	}
	// A full pass: the zero time is the earliest DATETIME.
	if got := ids(time.Time{}, time.Time{}, ch); got != "p1" {
		t.Errorf("full pass: %q, want p1", got)
	}
	settle := pgCheck("SELECT id FROM payments WHERE paid_at < @until")
	settle.Settle = time.Minute
	if got := ids(time.Time{}, at(10, 0), settle); got != "" {
		t.Errorf("until 10:00: %q, want nothing", got)
	}
	if got := ids(time.Time{}, at(10, 1), settle); got != "p1" {
		t.Errorf("until 10:01: %q, want p1", got)
	}
}

func TestMySQLTimeout(t *testing.T) {
	newMySQL(t)
	src := openMySQLEnv(t, "MC_MYSQL_READ", true)
	ch := pgCheck("SELECT BENCHMARK(10000000000, SHA2('x', 256)) AS b")
	ch.Timeout = 500 * time.Millisecond
	start := time.Now()
	_, err := collect(t, src, ch)
	if err == nil {
		t.Fatal("a slow query ran to the end")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("stopped after %s", d)
	}
}

func TestMySQLWriteGrants(t *testing.T) {
	for _, c := range []struct {
		line, db, want string
	}{
		{"GRANT USAGE ON *.* TO `u`@`%`", "shop", ""},
		{"GRANT SELECT ON `shop`.* TO `u`@`%`", "shop", ""},
		{"GRANT SELECT, INSERT ON `shop`.* TO `u`@`%`", "shop", "INSERT ON `shop`.*"},
		{"GRANT INSERT ON `other`.* TO `u`@`%`", "shop", ""},
		{"GRANT INSERT ON `other`.* TO `u`@`%`", "", "INSERT ON `other`.*"},
		{"GRANT ALL PRIVILEGES ON *.* TO `root`@`%` WITH GRANT OPTION", "shop", "ALL PRIVILEGES ON *.*"},
		{"GRANT FILE ON *.* TO `u`@`%`", "shop", "FILE ON *.*"},
		{"GRANT SELECT (`id`, `amount`), UPDATE (`note`) ON `shop`.`payments` TO `u`@`%`", "shop", "UPDATE ON `shop`.`payments`"},
		{"GRANT CREATE VIEW, SHOW VIEW ON `shop`.* TO `u`@`%`", "shop", "CREATE VIEW ON `shop`.*"},
		{"GRANT EXECUTE, ALTER ROUTINE ON PROCEDURE `shop`.`p` TO `u`@`%`", "shop", "ALTER ROUTINE ON `shop`.`p`"},
		{"GRANT DELETE ON `sh\\_%`.* TO `u`@`%`", "sh_op", "DELETE ON `sh\\_%`.*"},
		{"GRANT DELETE ON `sh\\_%`.* TO `u`@`%`", "shop", ""},
		{"GRANT `writer`@`%` TO `u`@`%`", "shop", ""},
		{"GRANT BACKUP_ADMIN ON *.* TO `u`@`%`", "shop", ""},
	} {
		if got := strings.Join(mysqlWriteGrants(c.line, c.db), "; "); got != c.want {
			t.Errorf("%s (%s): %q, want %q", c.line, c.db, got, c.want)
		}
	}
}

func TestMySQLVersion(t *testing.T) {
	for v, ok := range map[string]bool{
		"8.4.13": true, "8.0.40-0ubuntu0.22.04.1": true, "9.5.0": true,
		"5.7.44-log": false, "10.11.8-MariaDB-ubu2204": false, "11.4.2-MariaDB": false,
	} {
		if err := mysqlVersion(v); (err == nil) != ok {
			t.Errorf("%s: %v", v, err)
		}
	}
}
