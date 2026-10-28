package source

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// pgNode is one node of EXPLAIN (FORMAT JSON).
type pgNode struct {
	NodeType       string   `json:"Node Type"`
	Relation       string   `json:"Relation Name"`
	Schema         string   `json:"Schema"`
	Index          string   `json:"Index Name"`
	Parent         string   `json:"Parent Relationship"`
	PlanRows       float64  `json:"Plan Rows"`
	ActualRows     *float64 `json:"Actual Rows"`
	ActualLoops    *float64 `json:"Actual Loops"`
	RemovedFilter  float64  `json:"Rows Removed by Filter"`
	RemovedRecheck float64  `json:"Rows Removed by Index Recheck"`
	Plans          []pgNode `json:"Plans"`
}

// Explain runs EXPLAIN of the check's query in the same READ ONLY transaction
// and with the same parameters as Query. With analyze, EXPLAIN ANALYZE runs the
// query, and statement_timeout stops it with an error. For the tables the plan
// reads, pg_class says how many rows they hold, or that they were never analyzed.
func (p *postgres) Explain(ctx context.Context, ch config.Check, since, until time.Time, analyze bool) (Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	tx, args, err := p.begin(ctx, ch, since, until)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	explain := "explain (verbose, format json) "
	if analyze {
		explain = "explain (analyze, buffers, verbose, format json) "
	}
	var raw string
	if err := tx.QueryRow(ctx, explain+ch.Query, args...).Scan(&raw); err != nil {
		return Plan{}, err
	}
	var doc []struct {
		Plan pgNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Plan{}, fmt.Errorf("postgres: the plan is not EXPLAIN JSON: %w", err)
	}
	if len(doc) != 1 {
		return Plan{}, fmt.Errorf("postgres: EXPLAIN returned %d plans, want 1", len(doc))
	}
	stats := map[[2]string]float64{}
	var tables func(n pgNode)
	tables = func(n pgNode) {
		if n.Relation != "" {
			stats[[2]string{n.Schema, n.Relation}] = 0
		}
		for _, c := range n.Plans {
			tables(c)
		}
	}
	tables(doc[0].Plan)
	for t := range stats {
		var n float64
		err := tx.QueryRow(ctx, "select c.reltuples from pg_class c join pg_namespace s on s.oid = c.relnamespace where s.nspname = $1 and c.relname = $2", t[0], t[1]).Scan(&n)
		if err != nil {
			return Plan{}, fmt.Errorf("postgres: rows of %s: %w", pgTable(t[0], t[1]), err)
		}
		stats[t] = n
	}
	return pgSummary(raw, doc[0].Plan, stats, analyze), nil
}

// pgSummary walks a plan. stats holds pg_class.reltuples of every table in it:
// -1 for a table never analyzed.
func pgSummary(raw string, root pgNode, stats map[[2]string]float64, analyze bool) Plan {
	s := &plan{Plan: Plan{Raw: raw, Analyzed: analyze}}
	var walk func(n pgNode, loops float64, table string)
	walk = func(n pgNode, loops float64, table string) {
		if n.Relation != "" {
			key := [2]string{n.Schema, n.Relation}
			table = pgTable(n.Schema, n.Relation)
			if n.NodeType == "Seq Scan" {
				s.scan(table)
			}
			reltuples, known := stats[key]
			switch {
			case analyze:
				if n.ActualRows != nil && n.ActualLoops != nil {
					s.rows += (*n.ActualRows + n.RemovedFilter + n.RemovedRecheck) * *n.ActualLoops
				}
			case n.NodeType == "Seq Scan" && known && reltuples >= 0:
				s.rows += reltuples * loops
			default:
				s.rows += n.PlanRows * loops
			}
			if !analyze && known && reltuples < 0 {
				s.note("table " + table + " has no statistics, so the estimates are guesses: ANALYZE it, or explain with --analyze")
			}
		}
		if n.Index != "" {
			// A Bitmap Index Scan has no relation of its own: it is that of
			// the Bitmap Heap Scan above it.
			s.index(n.Index, table)
		}
		for i, c := range n.Plans {
			l := loops
			switch {
			case c.Parent == "SubPlan":
				// A correlated subquery runs once for each row of its node.
				l = loops * max(n.PlanRows, 1)
			case n.NodeType == "Nested Loop" && i == 1:
				l = loops * max(n.Plans[0].PlanRows, 1)
			}
			walk(c, l, table)
		}
	}
	walk(root, 1, "")
	return s.done()
}

// pgTable is the name of a table as people write it: without the schema when it
// is public.
func pgTable(schema, name string) string {
	if schema == "" || schema == "public" {
		return name
	}
	return schema + "." + name
}

var _ Explainer = (*postgres)(nil)
