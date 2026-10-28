package check

import (
	"context"
	"fmt"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// ExplainReport is the plans of the queries of a configuration. See
// docs/adr/0009-explain.md.
type ExplainReport struct {
	StartedAt time.Time
	// Analyze is set when the queries ran and the plans count rows read.
	Analyze  bool
	Results  []Explained
	Warnings []string
}

// Explained is the plan of one query: of a check, of its expect, or of a side
// of a compare check.
type Explained struct {
	Check string
	// Query is "" for the query of the check, "expect", "left", "right",
	// "left expect" or "right expect".
	Query  string
	Source string
	// Scope is Window when the query got the start of its window as since, as
	// run --window and serve give it.
	Scope Scope
	Since time.Time
	Until time.Time
	Plan  source.Plan
	Error string
}

// Explain asks the sources for the plan of every query of the configuration,
// one at a time, with the parameters Run would give them. Without analyze
// nothing runs. A source that cannot be opened, or cannot explain, fails its
// queries; the others are still explained.
func Explain(ctx context.Context, cfg config.Config, scope Scope, open Opener, now func() time.Time, analyze bool) ExplainReport {
	r := ExplainReport{StartedAt: now().UTC(), Analyze: analyze}
	warn := func(msg string) { r.Warnings = append(r.Warnings, msg) }
	type opened struct {
		src source.Source
		err error
	}
	sources := map[string]opened{}
	defer func() {
		for _, o := range sources {
			if o.src != nil {
				_ = o.src.Close(context.WithoutCancel(ctx))
			}
		}
	}()
	explain := func(name, query string, q config.Check, since, until time.Time, sc Scope) {
		e := Explained{Check: name, Query: query, Source: q.Source, Scope: sc, Since: since, Until: until}
		o, ok := sources[q.Source]
		if !ok {
			o.src, o.err = open(ctx, q.Source, cfg.Sources[q.Source], warn)
			sources[q.Source] = o
		}
		switch ex, can := o.src.(source.Explainer); {
		case o.err != nil:
			e.Error = o.err.Error()
		case !can:
			e.Error = fmt.Sprintf("source %s (%s) cannot explain a query yet", q.Source, cfg.Sources[q.Source].Kind())
		default:
			p, err := ex.Explain(ctx, q, since, until, analyze)
			if err != nil {
				e.Error = errorText(err, q)
			}
			e.Plan = p
		}
		r.Results = append(r.Results, e)
	}
	for _, ch := range cfg.Checks {
		sc, since, until := Full, time.Time{}, time.Time{}
		if scope == Window && ch.Window > 0 {
			sc, since = Window, r.StartedAt.Add(-ch.Window)
		}
		if ch.Settle > 0 {
			until = r.StartedAt.Add(-ch.Settle)
		}
		if ch.Compare != nil {
			for _, side := range []string{"left", "right"} {
				s := ch.Side(side)
				if s.Expect != nil {
					explain(ch.Name, side+" expect", s.ExpectCheck(cfg.Sources[s.Source].Kind()), since, until, sc)
				}
				explain(ch.Name, side, s, since, until, sc)
			}
			continue
		}
		if ch.Expect != nil {
			explain(ch.Name, "expect", ch.ExpectCheck(cfg.Sources[ch.Source].Kind()), since, until, sc)
		}
		// As in Run, only a compare check gets until.
		explain(ch.Name, "", ch, since, time.Time{}, sc)
	}
	return r
}

// ExitCode is 2 when a plan could not be had or the timeout stopped the query
// of one, as a check that fails in Run; 1 when a plan reads more than maxRows
// rows (0 is no limit); and 0 otherwise.
func (r ExplainReport) ExitCode(maxRows int64) int {
	code := 0
	for _, e := range r.Results {
		switch {
		case e.Error != "" || e.Plan.Stopped:
			return 2
		case maxRows > 0 && e.Plan.Rows > maxRows:
			code = 1
		}
	}
	return code
}
