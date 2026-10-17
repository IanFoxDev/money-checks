package check

import (
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/source"
)

const expectConf = `
version: 1
sources:
  billing: {mongodb: {uri_env: X, database: shop}}
  ledger: {postgres: {dsn_env: Y}}
checks:
  - name: rebills
    source: billing
    collection: payments
    pipeline: [{$match: {a: 1}}]
    expect: {pipeline: [{$count: n}]}
  - name: refunds
    source: ledger
    query: q where created_at >= $1
    id: id
    window: 24h
    expect: {query: "select count(*) from refunds where created_at >= $1", min: 100}
  - name: bare
    source: ledger
    query: q
    id: id
`

func TestExpect(t *testing.T) {
	f := &fake{rows: map[string][]source.Row{
		"rebills expect": {row("_id,n", nil, int64(1532))},
		"rebills":        {row("_id", "p1")},
		"refunds expect": {row("count", "99")},
		"refunds":        {row("id", "r1")},
	}}
	r := runScope(t, load(t, expectConf), Window, f)
	reb := r.Results[0]
	if reb.Status != StatusViolations || len(reb.Expected) != 1 || reb.Expected[0] != (Expected{Found: 1532, Min: 1}) {
		t.Errorf("rebills: %+v", reb)
	}
	ref := r.Results[1]
	if ref.Status != StatusFailed || ref.Error != "expect found 99, at least 100 required" {
		t.Errorf("refunds: %+v", ref)
	}
	if len(ref.Expected) != 1 || ref.Expected[0].Found != 99 {
		t.Errorf("refunds expected: %+v", ref.Expected)
	}
	// The check does not run on data it cannot see.
	if _, ran := f.since["refunds"]; ran {
		t.Error("refunds ran after its expect failed")
	}
	// The expect query of a window run gets the start of the window.
	if got, want := f.since["refunds expect"], r.StartedAt.Add(-24*time.Hour); !got.Equal(want) {
		t.Errorf("expect since %s, want %s", got, want)
	}
	if bare := r.Results[2]; bare.Status != StatusOK || bare.Expected != nil {
		t.Errorf("bare: %+v", bare)
	}
	if r.ExitCode() != 2 {
		t.Errorf("exit code %d", r.ExitCode())
	}
}

func TestExpectNoRowsIsZero(t *testing.T) {
	c := load(t, expectConf)
	c.Checks = c.Checks[:1]
	r := runFake(t, c, &fake{})
	if res := r.Results[0]; res.Status != StatusFailed || res.Error != "expect found 0, at least 1 required" {
		t.Errorf("%+v", res)
	}
}

func TestExpectFailsLoudlyOnBadRows(t *testing.T) {
	c := load(t, expectConf)
	c.Checks = c.Checks[1:2]
	for _, tt := range []struct {
		rows []source.Row
		want string
	}{
		{[]source.Row{row("n", "1"), row("n", "2")}, "expect: more than one row"},
		{[]source.Row{row("day,n", "2026-10-13", "2")}, "expect: 2 columns (day, n)"},
		{[]source.Row{row("n", "1.5")}, "expect: 1.5: not an integer"},
		// _id is left out only for MongoDB.
		{[]source.Row{row("_id,n", nil, "1")}, "expect: 2 columns (_id, n)"},
	} {
		r := runFake(t, c, &fake{rows: map[string][]source.Row{"refunds expect": tt.rows}})
		if res := r.Results[0]; res.Status != StatusFailed || !strings.HasPrefix(res.Error, tt.want) {
			t.Errorf("want %q, got %+v", tt.want, res)
		}
	}
}

func TestExpectOfCompareSides(t *testing.T) {
	c := load(t, `
version: 1
sources:
  billing: {mongodb: {uri_env: X, database: shop}}
  stats: {clickhouse: {url_env: Y, database: stats}}
checks:
  - name: daily
    compare:
      left: {source: billing, collection: purchases, pipeline: [{$match: {a: 1}}], expect: {pipeline: [{$count: n}]}}
      right: {source: stats, query: q, expect: {query: select count() from daily}}
      key: [day]
`)
	rows := map[string][]source.Row{
		"daily left expect":  {row("n", int64(40))},
		"daily left":         {row("day", "2026-10-13")},
		"daily right expect": {row("count()", "0")},
		"daily right":        {row("day", "2026-10-13")},
	}
	f := &fake{rows: rows}
	res := runFake(t, c, f).Results[0]
	if res.Status != StatusFailed || res.Error != "right: expect found 0, at least 1 required" {
		t.Errorf("%+v", res)
	}
	want := []Expected{{Side: "left", Found: 40, Min: 1}, {Side: "right", Found: 0, Min: 1}}
	if len(res.Expected) != 2 || res.Expected[0] != want[0] || res.Expected[1] != want[1] {
		t.Errorf("expected %+v", res.Expected)
	}
	if _, ran := f.since["daily right"]; ran {
		t.Error("the right side ran after its expect failed")
	}

	rows["daily right expect"] = []source.Row{row("count()", "12")}
	if res := runFake(t, c, &fake{rows: rows}).Results[0]; res.Status != StatusOK || len(res.Expected) != 2 {
		t.Errorf("%+v", res)
	}
}
