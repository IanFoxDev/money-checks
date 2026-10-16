package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// tsvFormat sends every value as text and needs no setting, so it works for a user
// whose profile sets readonly=1. See docs/adr/0003-clickhouse.md.
const tsvFormat = "TabSeparatedWithNames"

// writeGrants change data or tables. CHECK GRANT ALTER is 0 for a user who has only
// ALTER DELETE, so the parts are checked one by one.
var writeGrants = []string{
	"INSERT", "ALTER UPDATE", "ALTER DELETE", "ALTER DROP COLUMN", "ALTER MODIFY COLUMN",
	"ALTER MOVE PARTITION", "TRUNCATE", "DROP TABLE", "CREATE TABLE", "OPTIMIZE",
}

type clickhouse struct {
	client   *http.Client
	endpoint string     // the URL without credentials and query
	params   url.Values // the query of the URL, without credentials
	user     string
	password string
	auth     bool
	database string
	// readonly is the user's own setting: "1" means no setting can be sent.
	readonly string
}

func openClickHouse(ctx context.Context, c config.ClickHouse, warn func(string)) (*clickhouse, error) {
	raw, err := config.Getenv(c.URLEnv)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		// The value may hold a password, so it is not printed.
		return nil, fmt.Errorf("clickhouse: %s must be an http or https URL, such as https://user:password@host:8443/", c.URLEnv)
	}
	src := &clickhouse{client: &http.Client{}, params: u.Query(), database: c.Database}
	// Credentials go in a header, never in a URL that an error message could print.
	if u.User != nil {
		src.user = u.User.Username()
		src.password, _ = u.User.Password()
		src.auth = true
	}
	if v := src.params.Get("user"); v != "" {
		src.user, src.auth = v, true
	}
	if v := src.params.Get("password"); v != "" {
		src.password, src.auth = v, true
	}
	src.params.Del("user")
	src.params.Del("password")
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	src.endpoint = u.String()

	fail := func(err error) (*clickhouse, error) {
		src.client.CloseIdleConnections()
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	if src.readonly, err = src.value(ctx, "SELECT getSetting('readonly')"); err != nil {
		return fail(err)
	}
	switch src.readonly {
	case "1":
		warn("clickhouse: the user's profile sets readonly=1, so the check timeout cannot be sent as max_execution_time; " +
			"a query keeps running on the server after the check times out unless the profile sets max_execution_time")
	case "0":
		// readonly=2 on every query stops writes. The grants say whether the
		// credentials are wider than they need to be.
		problem, err := src.writeProblem(ctx)
		if err != nil {
			return fail(err)
		}
		if problem != "" {
			if c.RequireReadOnly == nil || *c.RequireReadOnly {
				return fail(fmt.Errorf("%s; connect as a user with SELECT only, or set require_read_only: false to accept the risk", problem))
			}
			warn("clickhouse: " + problem + "; running anyway because require_read_only is false")
		}
	}
	return src, nil
}

// writeProblem says which write grants the user has on the database, or "". It
// runs without readonly: under readonly, CHECK GRANT answers 0 for everything.
func (c *clickhouse) writeProblem(ctx context.Context) (string, error) {
	var found []string
	for _, g := range writeGrants {
		v, err := c.value(ctx, "CHECK GRANT "+g+" ON "+quoteIdent(c.database)+".*")
		if err != nil {
			return "", fmt.Errorf("checking the grants: %w", err)
		}
		if v == "1" {
			found = append(found, g)
		}
	}
	if len(found) == 0 {
		return "", nil
	}
	return fmt.Sprintf("the user can write to %s (%s)", c.database, strings.Join(found, ", ")), nil
}

// quoteIdent writes a ClickHouse identifier in backquotes.
func quoteIdent(s string) string {
	return "`" + strings.NewReplacer(`\`, `\\`, "`", "\\`").Replace(s) + "`"
}

// value runs a fixed statement that returns one value.
func (c *clickhouse) value(ctx context.Context, query string) (string, error) {
	resp, err := c.post(ctx, query, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("%s: unexpected answer %q", query, b)
	}
	return unescapeTSV(lines[1]), nil
}

// post sends a query. The settings replace values with the same name in the URL:
// with ?readonly=2&readonly=0 ClickHouse takes the last one. wait_end_of_query
// makes an error in the middle of the result a 500 instead of text after the rows.
func (c *clickhouse) post(ctx context.Context, query string, settings url.Values) (*http.Response, error) {
	q := url.Values{}
	for k, v := range c.params {
		q[k] = v
	}
	for k, v := range settings {
		q[k] = v
	}
	q.Set("database", c.database)
	q.Set("default_format", tsvFormat)
	q.Set("wait_end_of_query", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"?"+q.Encode(), strings.NewReader(query))
	if err != nil {
		return nil, err
	}
	if c.auth {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = resp.Status
		}
		return nil, errors.New(msg)
	}
	return resp, nil
}

// chMinTime is the start of the window on a full pass: the zero DateTime and
// DateTime64, and the earliest Date. Not 1900, the earliest DateTime64:
// toDate() of a time before 1970 wraps around to 2079, and a full pass of a
// query with toDate({since:...}) would read nothing.
var chMinTime = time.Unix(0, 0).UTC()

// Query runs the check with readonly=2 and the check's timeout as
// max_execution_time, or with readonly=1 alone when the user's profile allows no
// settings. Values come back as text, as from PostgreSQL; \N is nil. A check with
// a window gets since as the server parameter {since:DateTime64(6, 'UTC')}.
func (c *clickhouse) Query(ctx context.Context, ch config.Check, since time.Time, each func(Row) error) error {
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	settings := url.Values{}
	if c.readonly == "1" {
		settings.Set("readonly", "1")
	} else {
		settings.Set("readonly", "2")
		settings.Set("max_execution_time", strconv.FormatInt(int64(math.Ceil(ch.Timeout.Seconds())), 10))
		settings.Set("cancel_http_readonly_queries_on_client_close", "1")
	}
	if ch.Window > 0 {
		// A query parameter, never text in the query. It is allowed under
		// readonly=1 too.
		since = since.UTC()
		if since.Before(chMinTime) {
			since = chMinTime
		}
		settings.Set("param_since", since.Format("2006-01-02 15:04:05.000000"))
	}
	resp, err := c.post(ctx, ch.Query, settings)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if f := resp.Header.Get("X-ClickHouse-Format"); f != tsvFormat {
		return fmt.Errorf("the answer is in %s; remove the FORMAT clause from the query", f)
	}
	r := bufio.NewReader(resp.Body)
	var columns []string
	for n := 0; ; n++ {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		if string(line) == "__exception__" {
			rest, _ := io.ReadAll(io.LimitReader(r, 4096))
			return errors.New(strings.TrimSpace(string(rest)))
		}
		fields := strings.Split(string(line), "\t")
		if n == 0 {
			for _, f := range fields {
				columns = append(columns, unescapeTSV(f))
			}
			continue
		}
		if len(fields) != len(columns) {
			return fmt.Errorf("row %d has %d values for %d columns", n, len(fields), len(columns))
		}
		values := make([]any, len(fields))
		for i, f := range fields {
			if f != `\N` {
				values[i] = unescapeTSV(f)
			}
		}
		if err := each(Row{Columns: columns, Values: values}); err != nil {
			return err
		}
	}
	return nil
}

// unescapeTSV undoes the escaping of TabSeparated: \t, \n, \\ and the rest.
func unescapeTSV(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func (c *clickhouse) Close(context.Context) error {
	c.client.CloseIdleConnections()
	return nil
}
