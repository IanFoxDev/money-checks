package source

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// mysqlWrites are the privileges that change data or tables. See
// docs/adr/0008-mysql-read-only.md.
var mysqlWrites = map[string]bool{
	"ALL": true, "ALL PRIVILEGES": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"CREATE": true, "DROP": true, "ALTER": true, "INDEX": true, "CREATE VIEW": true,
	"CREATE ROUTINE": true, "ALTER ROUTINE": true, "TRIGGER": true, "EVENT": true, "FILE": true,
}

// mysqlGrant splits a line of SHOW GRANTS into the privileges and the object.
// A line that grants a role has no ON and does not match.
var mysqlGrant = regexp.MustCompile(`^GRANT (.+?) ON (?:TABLE |FUNCTION |PROCEDURE )?(\S+) TO `)

// mysqlColumns removes the column lists of privileges such as SELECT (a, b).
var mysqlColumns = regexp.MustCompile(`\([^)]*\)`)

type mysqlSource struct {
	db *sql.DB
}

func openMySQL(ctx context.Context, m config.MySQL, warn func(string)) (*mysqlSource, error) {
	dsn, err := config.Getenv(m.DSNEnv)
	if err != nil {
		return nil, err
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		// The error may quote the DSN, which holds the password.
		return nil, fmt.Errorf("mysql: %s is not a DSN, such as user:password@tcp(host:3306)/shop", m.DSNEnv)
	}
	// One statement per query, parameters sent apart from the query, values as
	// text, whatever the DSN asks for.
	cfg.MultiStatements = false
	cfg.InterpolateParams = false
	cfg.ParseTime = false
	cfg.AllowAllFiles = false
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("mysql: %w", err)
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(2)
	src := &mysqlSource{db: db}
	fail := func(err error) (*mysqlSource, error) {
		_ = db.Close()
		return nil, fmt.Errorf("mysql: %w", err)
	}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return fail(err)
	}
	if err := mysqlVersion(version); err != nil {
		return fail(err)
	}
	problem, err := src.writeProblem(ctx, cfg.DBName)
	if err != nil {
		return fail(err)
	}
	if problem != "" {
		if m.RequireReadOnly == nil || *m.RequireReadOnly {
			return fail(fmt.Errorf("%s; connect as a user with SELECT only, or set require_read_only: false to accept the risk", problem))
		}
		warn("mysql: " + problem + "; running anyway because require_read_only is false")
	}
	return src, nil
}

// mysqlVersion accepts MySQL 8.0 and later and nothing else.
func mysqlVersion(v string) error {
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return fmt.Errorf("server %s is MariaDB, which is not supported: it has no max_execution_time", v)
	}
	major, _, _ := strings.Cut(v, ".")
	if n, err := strconv.Atoi(major); err != nil || n < 8 {
		return fmt.Errorf("server %s: MySQL 8.0 or later is required", v)
	}
	return nil
}

// writeProblem says which privileges that write the user has, or "". It reads
// SHOW GRANTS, which includes the privileges of the active roles.
func (m *mysqlSource) writeProblem(ctx context.Context, database string) (string, error) {
	rows, err := m.db.QueryContext(ctx, "SHOW GRANTS")
	if err != nil {
		return "", fmt.Errorf("checking the grants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var found []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", fmt.Errorf("checking the grants: %w", err)
		}
		found = append(found, mysqlWriteGrants(line, database)...)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("checking the grants: %w", err)
	}
	if len(found) == 0 {
		return "", nil
	}
	return "the user can write: " + strings.Join(found, "; "), nil
}

// mysqlWriteGrants returns the privileges of one line of SHOW GRANTS that write,
// as "INSERT ON `shop`.*", when the object is *.*, the database or a table in
// it. With no database in the DSN, every object counts.
func mysqlWriteGrants(line, database string) []string {
	m := mysqlGrant.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	db, _, _ := strings.Cut(m[2], ".")
	if database != "" && db != "*" && !mysqlLike(strings.Trim(db, "`"), database) {
		return nil
	}
	var found []string
	for _, p := range strings.Split(mysqlColumns.ReplaceAllString(m[1], ""), ",") {
		p = strings.ToUpper(strings.Join(strings.Fields(p), " "))
		if mysqlWrites[p] {
			found = append(found, p+" ON "+m[2])
		}
	}
	return found
}

// mysqlLike matches a database name against the name in a grant, where % and _
// are wildcards unless escaped with a backslash.
func mysqlLike(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '\\' && i+1 < len(pattern):
			i++
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		case c == '%':
			b.WriteString(".*")
		case c == '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	ok, _ := regexp.MatchString(b.String(), name)
	return ok
}

// mysqlMinTime is the start of the window on a full pass, the earliest DATETIME.
var mysqlMinTime = time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)

// mysqlTimeFormat is how since and until are sent, to CAST(? AS DATETIME(6)).
const mysqlTimeFormat = "2006-01-02 15:04:05.000000"

// mysqlGrace is how much longer than the check's timeout the server lets a
// query run. The check's own deadline decides: SLEEP and BENCHMARK stopped by
// max_execution_time return a value, not an error. The server limit stops a
// query the client has given up on.
const mysqlGrace = time.Second

// Query takes a connection, makes its session read-only, in UTC and with
// max_execution_time just over the check's timeout, sets @since and @until, and
// runs the query on it. The session is set again before every check, so nothing a check
// did to it carries over. Values come back as text; NULL is nil.
func (m *mysqlSource) Query(ctx context.Context, ch config.Check, since, until time.Time, each func(Row) error) error {
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	conn, err := m.session(ctx, ch, since, until, ch.Timeout+mysqlGrace)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// Without arguments the driver sends the query as text, and every value
	// comes back as text.
	rows, err := conn.QueryContext(ctx, ch.Query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	raw := make([]sql.RawBytes, len(columns))
	dest := make([]any, len(columns))
	for i := range raw {
		dest[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		values := make([]any, len(raw))
		for i, v := range raw {
			if v != nil {
				values[i] = string(v)
			}
		}
		if err := each(Row{Columns: columns, Values: values}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// session takes a connection and sets its session for a check: read-only, in
// UTC, with limit as max_execution_time, and @since and @until.
func (m *mysqlSource) session(ctx context.Context, ch config.Check, since, until time.Time, limit time.Duration) (*sql.Conn, error) {
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	// Parameters, never text in the query. A variable the check does not use
	// is NULL, not what the previous check left in it.
	var sinceArg, untilArg any
	if ch.Window > 0 {
		since = since.UTC()
		if since.Before(mysqlMinTime) {
			since = mysqlMinTime
		}
		sinceArg = since.Format(mysqlTimeFormat)
	}
	if ch.Settle > 0 {
		untilArg = until.UTC().Format(mysqlTimeFormat)
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION transaction_read_only = ON, max_execution_time = ?, time_zone = '+00:00', "+
		"@since = CAST(? AS DATETIME(6)), @until = CAST(? AS DATETIME(6))",
		int64(math.Ceil(float64(limit)/float64(time.Millisecond))), sinceArg, untilArg); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (m *mysqlSource) Close(context.Context) error {
	return m.db.Close()
}
