package source

import (
	"context"
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
	if !analyze {
		return mysqlJSON(raw)
	}
	p := mysqlTree(raw)
	// The server stops the query a little before its limit.
	p.Stopped = time.Since(start) >= ch.Timeout*95/100
	return p, nil
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
func mysqlJSON(raw string) (Plan, error) {
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
func mysqlTree(raw string) Plan {
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
			s.scan(t[1])
			read = true
		} else if t := mysqlFullIndex.FindStringSubmatch(desc); t != nil {
			s.scan(t[1])
			s.index(t[2], t[1])
			read = true
		} else if t := mysqlIndexUse.FindStringSubmatch(desc); t != nil {
			s.index(t[2], t[1])
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
