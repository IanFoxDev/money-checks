package check

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// explainer is a fake source that also explains: the plan set for each query
// name, and what each one got.
type explainer struct {
	fake
	plans   map[string]source.Plan
	analyze map[string]bool
}

func (e *explainer) Explain(_ context.Context, ch config.Check, since, until time.Time, analyze bool) (source.Plan, error) {
	if e.since == nil {
		e.since, e.until, e.analyze = map[string]time.Time{}, map[string]time.Time{}, map[string]bool{}
	}
	e.since[ch.Name], e.until[ch.Name], e.analyze[ch.Name] = since, until, analyze
	if err := e.errs[ch.Name]; err != nil {
		return source.Plan{}, err
	}
	return e.plans[ch.Name], nil
}

const explainConf = `
version: 1
sources:
  billing: {postgres: {dsn_env: X}}
  stats: {clickhouse: {url_env: Y, database: stats}}
checks:
  - name: rebills
    source: billing
    id: id
    query: select id from charges where created_at >= $1
    window: 24h
    expect: {query: select count(*) from charges where created_at >= $1}
  - name: stuck
    source: billing
    id: id
    query: select id from payments
  - name: daily
    compare:
      left: {source: billing, query: "select day, n from a where day >= $1 and day < $2"}
      right: {source: stats, query: "select day, n from b where t >= {since:DateTime64(6, 'UTC')} and t < {until:DateTime64(6, 'UTC')}"}
      key: [day]
      counts: [n]
    window: 72h
    settle: 2h
`

func TestExplain(t *testing.T) {
	cfg := load(t, explainConf)
	billing := &explainer{plans: map[string]source.Plan{
		"rebills":        {Indexes: []string{"charges_created_at_idx on charges"}, Rows: 60},
		"rebills expect": {Rows: 60},
		"stuck":          {Scans: []string{"payments"}, Rows: 120000},
	}}
	stats := &fake{}
	open := func(_ context.Context, name string, _ config.Source, _ func(string)) (source.Source, error) {
		if name == "stats" {
			return stats, nil
		}
		return billing, nil
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	r := Explain(context.Background(), cfg, Window, open, func() time.Time { return now }, true)
	var got []string
	for _, e := range r.Results {
		got = append(got, e.Check+"/"+e.Query+"/"+string(e.Scope))
	}
	if want := "rebills/expect/window rebills//window stuck//full daily/left/window daily/right/window"; strings.Join(got, " ") != want {
		t.Fatalf("explained %s, want %s", strings.Join(got, " "), want)
	}
	if !billing.since["rebills"].Equal(now.Add(-24*time.Hour)) || !billing.since["rebills expect"].Equal(now.Add(-24*time.Hour)) {
		t.Errorf("since %v", billing.since)
	}
	if !billing.since["stuck"].IsZero() || !billing.analyze["stuck"] {
		t.Errorf("stuck: since %v, analyze %v", billing.since["stuck"], billing.analyze["stuck"])
	}
	if !billing.since["daily left"].Equal(now.Add(-72*time.Hour)) || !billing.until["daily left"].Equal(now.Add(-2*time.Hour)) {
		t.Errorf("daily left: since %v, until %v", billing.since["daily left"], billing.until["daily left"])
	}
	if r.Results[2].Plan.Rows != 120000 || r.Results[2].Error != "" {
		t.Errorf("stuck: %+v", r.Results[2])
	}
	if e := r.Results[4]; e.Error != "source stats (clickhouse) cannot explain a query yet" {
		t.Errorf("daily right: %q", e.Error)
	}
	if r.ExitCode(0) != 2 {
		t.Errorf("exit %d with a source that cannot explain", r.ExitCode(0))
	}
}

func TestExplainExitCode(t *testing.T) {
	ok := ExplainReport{Results: []Explained{{Plan: source.Plan{Rows: 60}}, {Plan: source.Plan{Rows: 120000}}}}
	for _, c := range []struct {
		r    ExplainReport
		max  int64
		want int
	}{
		{ok, 0, 0},
		{ok, 200000, 0},
		{ok, 100000, 1},
		{ExplainReport{Results: []Explained{{Plan: source.Plan{Rows: 120000}}, {Error: "down"}}}, 100000, 2},
		{ExplainReport{Results: []Explained{{Plan: source.Plan{Analyzed: true, Stopped: true}}}}, 0, 2},
	} {
		if got := c.r.ExitCode(c.max); got != c.want {
			t.Errorf("%+v with --max-rows %d: %d, want %d", c.r.Results, c.max, got, c.want)
		}
	}
}

func TestExplainSourceErrors(t *testing.T) {
	cfg := load(t, explainConf)
	billing := &explainer{fake: fake{errs: map[string]error{"stuck": context.DeadlineExceeded}}}
	open := func(_ context.Context, name string, _ config.Source, _ func(string)) (source.Source, error) {
		if name == "stats" {
			return nil, errors.New("source stats: connection refused")
		}
		return billing, nil
	}
	r := Explain(context.Background(), cfg, Full, open, time.Now, false)
	for _, e := range r.Results {
		switch e.Check + "/" + e.Query {
		case "stuck/":
			if !strings.HasPrefix(e.Error, "timed out after") {
				t.Errorf("stuck: %q", e.Error)
			}
		case "daily/right":
			if e.Error != "source stats: connection refused" {
				t.Errorf("daily right: %q", e.Error)
			}
		default:
			if e.Error != "" || e.Scope != Full || !billing.since[e.Check].IsZero() {
				t.Errorf("%s %s: error %q, scope %s", e.Check, e.Query, e.Error, e.Scope)
			}
		}
	}
}
