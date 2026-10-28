package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
)

// ExplainText writes the summary of each plan: what is read in full, which
// indexes, how many rows, and the notes. Never the plan itself, which can hold
// the values of a row. See docs/adr/0009-explain.md.
func ExplainText(w io.Writer, r check.ExplainReport, maxRows int64) error {
	var b strings.Builder
	ran := "nothing ran"
	if r.Analyze {
		ran = "with --analyze, the queries ran"
	}
	fmt.Fprintf(&b, "money-checks explain at %s, %s.\n", r.StartedAt.Format("2006-01-02 15:04:05 UTC"), ran)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", w)
	}
	full, failed, over := 0, 0, 0
	for _, e := range r.Results {
		fmt.Fprintf(&b, "\n%s (source %s, %s)\n", queryName(e), e.Source, explainedRows(e))
		if e.Error != "" {
			failed++
			fmt.Fprintf(&b, "  error: %s\n", e.Error)
			continue
		}
		p := e.Plan
		if p.Stopped {
			failed++
			b.WriteString("  stopped by the timeout: the plan is of the part that ran\n")
		}
		if len(p.Scans) > 0 {
			full++
		}
		for _, s := range p.Scans {
			fmt.Fprintf(&b, "  full scan: %s\n", s)
		}
		for _, ix := range p.Indexes {
			fmt.Fprintf(&b, "  index: %s\n", ix)
		}
		switch {
		case p.Rows < 0:
			b.WriteString("  rows: unknown\n")
		case p.Analyzed:
			fmt.Fprintf(&b, "  rows read: %d\n", p.Rows)
		default:
			fmt.Fprintf(&b, "  rows estimated: %d\n", p.Rows)
		}
		if maxRows > 0 && p.Rows > maxRows {
			over++
			fmt.Fprintf(&b, "  over --max-rows %d\n", maxRows)
		}
		for _, n := range p.Notes {
			fmt.Fprintf(&b, "  note: %s\n", n)
		}
	}
	queries := "queries"
	if len(r.Results) == 1 {
		queries = "query"
	}
	fmt.Fprintf(&b, "\n%d %s: %d read a table in full, %d failed", len(r.Results), queries, full, failed)
	if maxRows > 0 {
		fmt.Fprintf(&b, ", %d over --max-rows %d", over, maxRows)
	}
	b.WriteString(".\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// queryName is "rebills", "rebills expect" or "daily left".
func queryName(e check.Explained) string {
	if e.Query == "" {
		return e.Check
	}
	return e.Check + " " + e.Query
}

// explainedRows says which rows the query was given, as rows does for a check.
func explainedRows(e check.Explained) string {
	const at = "2006-01-02 15:04 UTC"
	switch {
	case e.Scope == check.Window && !e.Until.IsZero():
		return "rows from " + e.Since.Format(at) + " to " + e.Until.Format(at)
	case e.Scope == check.Window:
		return "rows since " + e.Since.Format(at)
	case !e.Until.IsZero():
		return "all rows before " + e.Until.Format(at)
	}
	return "all rows"
}

type jsonExplainReport struct {
	Version   int                `json:"version"`
	StartedAt time.Time          `json:"started_at"`
	Analyze   bool               `json:"analyze"`
	Warnings  []string           `json:"warnings"`
	Queries   []jsonExplainQuery `json:"queries"`
}

type jsonExplainQuery struct {
	Check  string     `json:"check"`
	Query  string     `json:"query"`
	Source string     `json:"source"`
	Scope  string     `json:"scope"`
	Since  *time.Time `json:"since,omitempty"`
	Until  *time.Time `json:"until,omitempty"`
	Error  string     `json:"error,omitempty"`

	Scans   []string `json:"scans"`
	Indexes []string `json:"indexes"`
	// Rows is -1 when the source gives no estimate.
	Rows     int64    `json:"rows"`
	Analyzed bool     `json:"analyzed"`
	Stopped  bool     `json:"stopped"`
	Notes    []string `json:"notes"`
	// Plan is the plan as the database returned it: JSON for most sources, text
	// for EXPLAIN ANALYZE of MySQL.
	Plan string `json:"plan"`
}

// ExplainJSON writes the summaries and the plans, version 1 of its format.
// Lists are never null.
func ExplainJSON(w io.Writer, r check.ExplainReport) error {
	out := jsonExplainReport{Version: 1, StartedAt: r.StartedAt, Analyze: r.Analyze, Warnings: nonNil(r.Warnings), Queries: []jsonExplainQuery{}}
	for _, e := range r.Results {
		q := jsonExplainQuery{
			Check: e.Check, Query: e.Query, Source: e.Source, Scope: string(e.Scope), Error: e.Error,
			Scans: nonNil(e.Plan.Scans), Indexes: nonNil(e.Plan.Indexes), Rows: e.Plan.Rows,
			Analyzed: e.Plan.Analyzed, Stopped: e.Plan.Stopped, Notes: nonNil(e.Plan.Notes), Plan: e.Plan.Raw,
		}
		if e.Error != "" {
			q.Rows = -1
		}
		if e.Scope == check.Window {
			since := e.Since
			q.Since = &since
		}
		if !e.Until.IsZero() {
			until := e.Until
			q.Until = &until
		}
		out.Queries = append(out.Queries, q)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
