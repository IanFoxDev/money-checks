package source

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// Explain runs EXPLAIN FORMAT=JSON of the check's query in the same session as
// Query. With analyze it runs EXPLAIN ANALYZE, which runs the query and gives a
// tree as text. max_execution_time stops EXPLAIN ANALYZE without an error and
// with the plan of what ran until then, so with analyze the server limit is the
// check's timeout and the client waits a little longer to get that plan, which is
// then Stopped. See docs/adr/0009-explain.md.
func (m *mysqlSource) Explain(ctx context.Context, ch config.Check, since, until time.Time, analyze bool) (Plan, error) {
	limit, wait, explain := ch.Timeout+mysqlGrace, ch.Timeout, "EXPLAIN FORMAT=JSON "
	if analyze {
		limit, wait, explain = ch.Timeout, ch.Timeout+mysqlGrace, "EXPLAIN ANALYZE "
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	conn, err := m.session(ctx, ch, since, until, limit)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = conn.Close() }()
	start := time.Now()
	var raw string
	if err := conn.QueryRowContext(ctx, explain+ch.Query).Scan(&raw); err != nil {
		return Plan{}, err
	}
	// The server stops the query a little before its limit.
	stopped := analyze && time.Since(start) >= ch.Timeout*95/100
	var names map[string]string
	if !stopped {
		names = mysqlTables(ctx, conn, ch.Query)
	}
	if !analyze {
		return mysqlJSON(raw, names)
	}
	p := mysqlTree(raw, names)
	p.Stopped = stopped
	return p, nil
}

// mysqlTables maps the aliases of the query to their tables. Both plans name a
// table by its alias; version 2 of EXPLAIN JSON, from MySQL 8.3, also gives the
// table. On an older server the summary keeps the aliases.
func mysqlTables(ctx context.Context, conn *sql.Conn, query string) map[string]string {
	if _, err := conn.ExecContext(ctx, "SET SESSION explain_json_format_version = 2"); err != nil {
		return nil
	}
	// The connection goes back to the pool, where version 1 is expected; one
	// that cannot be set back is dropped.
	defer func() {
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), "SET SESSION explain_json_format_version = 1"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var raw string
	if err := conn.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query).Scan(&raw); err != nil {
		return nil
	}
	var doc any
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return nil
	}
	names := map[string]string{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			alias, _ := x["alias"].(string)
			table, _ := x["table_name"].(string)
			if alias != "" && table != "" {
				// The same alias for two tables, in two subqueries, stays.
				if t, ok := names[alias]; ok && t != table {
					table = alias
				}
				names[alias] = table
			}
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
	return names
}

// mysqlName is the table of an alias, when names has it.
func mysqlName(names map[string]string, alias string) string {
	if t, ok := names[alias]; ok {
		return t
	}
	return alias
}

// mysqlSubqueries are the keys of EXPLAIN JSON that hold subqueries of a query
// block.
var mysqlSubqueries = map[string]bool{
	"attached_subqueries": true, "select_list_subqueries": true, "having_subqueries": true,
	"order_by_subqueries": true, "group_by_subqueries": true, "optimized_away_subqueries": true,
}

// mysqlJSON sums up EXPLAIN FORMAT=JSON. A table is scanned once for each row
// the tables before it in a nested loop produce, unless it is joined through a
// hash join buffer; a dependent subquery runs once for each row of the table it
// is attached to.
func mysqlJSON(raw string, names map[string]string) (Plan, error) {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Plan{}, fmt.Errorf("mysql: the plan is not EXPLAIN JSON: %w", err)
	}
	s := &plan{Plan: Plan{Raw: raw}}
	// produced is the number of rows the last table walked produces, for
	// the loops of the table or dependent subquery after it.
	produced := 1.0
	var walk func(v any, loops float64)
	walk = func(v any, loops float64) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e, loops)
			}
		case map[string]any:
			if name, ok := x["table_name"].(string); ok {
				if _, derived := x["materialized_from_subquery"]; !derived && !strings.HasPrefix(name, "<") {
					name = mysqlName(names, name)
					switch x["access_type"] {
					case "ALL", "index":
						s.scan(name)
					}
					if key, ok := x["key"].(string); ok {
						s.index(key, name)
					}
					if n, ok := mysqlNumber(x["rows_examined_per_scan"]); ok {
						s.rows += n * loops
					}
				}
				if n, ok := mysqlNumber(x["rows_produced_per_join"]); ok {
					produced = n
				}
			}
			// The tables of the block first: a dependent subquery runs once
			// for each row they produce.
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				if mysqlSubqueries[keys[i]] != mysqlSubqueries[keys[j]] {
					return !mysqlSubqueries[keys[i]]
				}
				return keys[i] < keys[j]
			})
			for _, k := range keys {
				e := x[k]
				switch {
				case k == "nested_loop":
					list, _ := e.([]any)
					outer := loops
					for i, t := range list {
						l := outer
						if i > 0 && !mysqlHashJoin(t) {
							l = loops * max(produced, 1)
						}
						walk(t, l)
					}
				case mysqlSubqueries[k]:
					list, _ := e.([]any)
					rows := produced
					for _, q := range list {
						l := loops
						if m, ok := q.(map[string]any); ok && m["dependent"] == true {
							l = loops * max(rows, 1)
						}
						walk(q, l)
					}
					produced = rows
				default:
					walk(e, loops)
				}
			}
		}
	}
	walk(doc, 1)
	return s.done(), nil
}

// mysqlHashJoin says whether a table of a nested loop is joined through a hash
// join buffer, and so read once.
func mysqlHashJoin(v any) bool {
	m, _ := v.(map[string]any)
	t, _ := m["table"].(map[string]any)
	buf, _ := t["using_join_buffer"].(string)
	return strings.Contains(strings.ToLower(buf), "hash")
}

func mysqlNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		n, err := strconv.ParseFloat(x, 64)
		return n, err == nil
	}
	return 0, false
}

// mysqlLine is one line of the tree of EXPLAIN ANALYZE: the depth, what the
// step does, and the rows of one loop and the loops when it ran.
var mysqlLine = regexp.MustCompile(`^( *)-> (.+?)(?:  \(cost=[^)]*\))?(?: \(actual time=\S+ rows=(\S+) loops=(\d+)\)| \(never executed\))?$`)

var (
	mysqlTableScan = regexp.MustCompile(`^Table scan on (\S+)`)
	mysqlFullIndex = regexp.MustCompile(`^(?:Covering index|Index) scan on (\S+) using (\S+)`)
	mysqlIndexUse  = regexp.MustCompile(` on (\S+) using (\S+)`)
)

// mysqlTree sums up the tree of EXPLAIN ANALYZE. A table scan of a temporary
// table that the step below it materializes is not a table of the database.
func mysqlTree(raw string, names map[string]string) Plan {
	s := &plan{Plan: Plan{Raw: raw, Analyzed: true}}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i, line := range lines {
		m := mysqlLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		desc := m[2]
		read := false
		if t := mysqlTableScan.FindStringSubmatch(desc); t != nil {
			if i+1 < len(lines) {
				if next := mysqlLine.FindStringSubmatch(lines[i+1]); next != nil && len(next[1]) > len(m[1]) && strings.HasPrefix(next[2], "Materialize") {
					continue
				}
			}
			s.scan(mysqlName(names, t[1]))
			read = true
		} else if t := mysqlFullIndex.FindStringSubmatch(desc); t != nil {
			s.scan(mysqlName(names, t[1]))
			s.index(t[2], mysqlName(names, t[1]))
			read = true
		} else if t := mysqlIndexUse.FindStringSubmatch(desc); t != nil {
			s.index(t[2], mysqlName(names, t[1]))
			read = true
		}
		if read && m[3] != "" {
			rows, _ := strconv.ParseFloat(m[3], 64)
			loops, _ := strconv.ParseFloat(m[4], 64)
			s.rows += rows * loops
		}
	}
	return s.done()
}

var _ Explainer = (*mysqlSource)(nil)
