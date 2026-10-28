package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// chInSubquery finds IN (SELECT ...), whose set ClickHouse builds by running the
// subquery while it plans.
var chInSubquery = regexp.MustCompile(`(?i)\bIN\s*\(\s*(SELECT|WITH)\b`)

// chPlanLimit is the most of a plan that is read.
const chPlanLimit = 16 << 20

// Explain sends EXPLAIN json = 1, indexes = 1 and EXPLAIN ESTIMATE of the check's
// query, with the settings of Query. Neither reads the rows of a table, but to
// choose the parts to read ClickHouse runs the subqueries of IN (SELECT ...):
// without analyze use_index_for_in_with_subqueries = 0 stops that, and the plan
// then uses no index for them. ClickHouse has no explain that runs the query, so
// the rows are always the estimate. See docs/adr/0009-explain.md.
func (c *clickhouse) Explain(ctx context.Context, ch config.Check, since, until time.Time, analyze bool) (Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	settings := c.settings(ch, since, until)
	in := chInSubquery.MatchString(ch.Query)
	if !analyze && c.readonly != "1" {
		settings.Set("use_index_for_in_with_subqueries", "0")
	}
	raw, err := c.explain(ctx, "EXPLAIN json = 1, indexes = 1 "+ch.Query, settings)
	if err != nil {
		return Plan{}, err
	}
	if len(raw) != 1 || len(raw[0]) != 1 {
		return Plan{}, fmt.Errorf("clickhouse: EXPLAIN json returned %d rows, want 1", len(raw))
	}
	s, err := chSummary(raw[0][0])
	if err != nil {
		return Plan{}, err
	}
	est, err := c.explain(ctx, "EXPLAIN ESTIMATE "+ch.Query, settings)
	if err != nil {
		return Plan{}, err
	}
	// database, table, parts, rows, marks
	for _, r := range est {
		if len(r) == 5 {
			n, _ := strconv.ParseFloat(r[3], 64)
			s.rows += n
		}
	}
	switch {
	case in && c.readonly == "1":
		s.note("the user's profile sets readonly=1, so ClickHouse ran the IN (SELECT ...) subqueries while it planned, with no time limit")
	case in && !analyze:
		s.note("the IN (SELECT ...) subqueries did not run, so the plan uses no index for them; explain with --analyze to let ClickHouse run them as it would")
	}
	if analyze {
		s.note("ClickHouse cannot run a query in explain: the rows are the estimate of the parts and granules left to read")
	}
	return s.done(), nil
}

// explain runs an EXPLAIN and returns its rows without the header.
func (c *clickhouse) explain(ctx context.Context, query string, settings map[string][]string) ([][]string, error) {
	resp, err := c.post(ctx, query, settings)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, chPlanLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > chPlanLimit {
		return nil, fmt.Errorf("clickhouse: the plan is over %d MB", chPlanLimit>>20)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	var rows [][]string
	for i, line := range lines {
		if i == 0 {
			continue
		}
		if line == "__exception__" {
			return nil, fmt.Errorf("clickhouse: %s", strings.TrimSpace(strings.Join(lines[i+1:], "\n")))
		}
		fields := strings.Split(line, "\t")
		for j, f := range fields {
			fields[j] = unescapeTSV(f)
		}
		rows = append(rows, fields)
	}
	return rows, nil
}

// chNode is one node of EXPLAIN json = 1, indexes = 1.
type chNode struct {
	NodeType    string    `json:"Node Type"`
	Description string    `json:"Description"`
	Indexes     []chIndex `json:"Indexes"`
	Plans       []chNode  `json:"Plans"`
}

type chIndex struct {
	Type     string   `json:"Type"`
	Name     string   `json:"Name"`
	Keys     []string `json:"Keys"`
	Initial  *int64   `json:"Initial Granules"`
	Selected *int64   `json:"Selected Granules"`
}

// chSummary walks the plan. A table is read in full when no index or partition
// key leaves out a granule; an index is used when it leaves out at least one.
func chSummary(raw string) (*plan, error) {
	var doc []struct {
		Plan chNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("clickhouse: the plan is not EXPLAIN json: %w", err)
	}
	s := &plan{Plan: Plan{Raw: raw}}
	var walk func(n chNode)
	walk = func(n chNode) {
		if n.NodeType == "ReadFromMergeTree" {
			full := true
			for _, ix := range n.Indexes {
				if ix.Initial == nil || ix.Selected == nil || *ix.Selected >= *ix.Initial {
					continue
				}
				full = false
				name := ix.Name
				switch ix.Type {
				case "PrimaryKey":
					name = "primary key"
				case "Partition", "MinMax":
					name = "partition key"
				}
				if len(ix.Keys) > 0 && ix.Type != "Skip" {
					name += " (" + strings.Join(ix.Keys, ", ") + ")"
				}
				s.index(name, n.Description)
			}
			if full {
				s.scan(n.Description)
			}
		}
		for _, c := range n.Plans {
			walk(c)
		}
	}
	for _, d := range doc {
		walk(d.Plan)
	}
	return s, nil
}

var _ Explainer = (*clickhouse)(nil)
