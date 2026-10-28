package source

import (
	"context"
	"math"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// Explainer is a Source that can say how the database runs a check's query. See
// docs/adr/0009-explain.md.
type Explainer interface {
	// Explain returns the plan of the check's query for since and until, as Query
	// would run it. Without analyze the query does not run. With analyze it runs
	// as in Query: read-only, with the check's timeout.
	Explain(ctx context.Context, ch config.Check, since, until time.Time, analyze bool) (Plan, error)
}

// Plan is what a database says about one query, and a summary of it.
type Plan struct {
	// Raw is the plan as the database returned it: JSON, or text where the
	// database has no JSON form. It can hold values of rows; see the ADR.
	Raw string
	// Analyzed is set when the query ran, and Rows counts rows read rather than
	// rows the planner expects to read.
	Analyzed bool
	// Scans are the tables read in full, in the order the plan names them.
	Scans []string
	// Indexes are the indexes the plan uses, as "index on table".
	Indexes []string
	// Rows is how many rows the scans of the plan read, by estimate or counted;
	// -1 when the database does not say.
	Rows int64
	// Notes are what the summary cannot vouch for, such as a table without
	// statistics, whose estimates are guesses.
	Notes []string
	// Stopped is set when the timeout stopped a query run with analyze and the
	// database still returned a plan: the plan covers only what ran until then.
	Stopped bool
}

// plan collects a summary while a source walks its plan.
type plan struct {
	Plan
	seen map[string]bool
	rows float64
}

func (p *plan) scan(table string) {
	if p.once("scan " + table) {
		p.Scans = append(p.Scans, table)
	}
}

func (p *plan) index(name, table string) {
	if p.once("index " + name + " " + table) {
		p.Indexes = append(p.Indexes, name+" on "+table)
	}
}

func (p *plan) note(msg string) {
	if p.once("note " + msg) {
		p.Notes = append(p.Notes, msg)
	}
}

func (p *plan) once(key string) bool {
	if p.seen == nil {
		p.seen = map[string]bool{}
	}
	if p.seen[key] {
		return false
	}
	p.seen[key] = true
	return true
}

// done rounds the rows and returns the summary.
func (p *plan) done() Plan {
	switch {
	case p.rows >= math.MaxInt64:
		p.Rows = math.MaxInt64
	default:
		p.Rows = int64(math.Round(p.rows))
	}
	return p.Plan
}
