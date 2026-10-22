package check

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

const compareConf = `
version: 1
sources:
  billing: {mongodb: {uri_env: X, database: shop}}
  stats: {clickhouse: {url_env: Y, database: stats}}
  broken: {postgres: {dsn_env: Z}}
checks:
  - name: daily
    compare:
      left: {source: billing, collection: purchases, pipeline: [{$match: {a: 1}}]}
      right: {source: stats, query: q}
      key: [day, currency]
      amount: {field: amount, unit: major, currency_field: currency}
      counts: [n]
      tolerance_minor: 1
`

// Left is MongoDB with doubles and integers, right is ClickHouse with text, as
// the real sources return them.
func compareRows() map[string][]source.Row {
	return map[string][]source.Row{
		"daily left": {
			row("day,currency,amount,n", "2026-10-11", "USD", 100.0, int64(10)),
			row("day,currency,amount,n", "2026-10-12", "USD", 50.5, int64(5)),
			row("day,currency,amount,n", "2026-10-13", "USD", 20.0, int64(2)),
			row("day,currency,amount,n", "2026-10-14", "USD", 30.0, int64(3)),
		},
		"daily right": {
			row("day,currency,amount,n", "2026-10-14", "EUR", "5.00", "1"),
			row("day,currency,amount,n", "2026-10-13", "USD", "22.00", "3"),
			row("day,currency,amount,n", "2026-10-12", "USD", "50.51", "5"),
			row("day,currency,amount,n", "2026-10-11", "USD", "100.00", "10"),
		},
	}
}

func TestCompare(t *testing.T) {
	r := runFake(t, load(t, compareConf), &fake{rows: compareRows()})
	res := r.Results[0]
	if res.Status != StatusViolations || res.Violations != 3 || res.Source != "billing" || res.RightSource != "stats" {
		t.Fatalf("%+v", res)
	}
	// Absolute differences: 2.00 + 30.00 in USD, 5.00 in EUR. 50.51 against 50.50
	// is within the tolerance of one cent.
	if got := totals(res.Totals); got != "5.00 EUR, 32.00 USD" {
		t.Errorf("totals %s", got)
	}
	want := []string{
		"2026-10-13/USD 2.00 USD left=20.00 USD right=22.00 USD n left=2 n right=3 problem=amount differs, n differs",
		"2026-10-14/EUR 5.00 EUR left=- right=5.00 EUR n left=- n right=1 problem=only right",
		"2026-10-14/USD -30.00 USD left=30.00 USD right=- n left=3 n right=- problem=only left",
	}
	if len(res.Samples) != len(want) {
		t.Fatalf("samples %+v", res.Samples)
	}
	for i, s := range res.Samples {
		if got := sampleText(s); got != want[i] {
			t.Errorf("sample %d:\n got %s\nwant %s", i, got, want[i])
		}
	}
}

func sampleText(s Sample) string {
	parts := []string{s.ID, s.Amount.String()}
	for _, f := range s.Fields {
		parts = append(parts, f.Name+"="+f.Value)
	}
	return strings.Join(parts, " ")
}

func totals(ms []Money) string {
	var parts []string
	for _, m := range ms {
		parts = append(parts, m.String())
	}
	return strings.Join(parts, ", ")
}

func TestCompareKnown(t *testing.T) {
	c := load(t, compareConf)
	c.Known = map[string]map[string]config.Known{"daily": {
		"2026-10-14/EUR": {ID: "2026-10-14/EUR", Reason: "test purchase", Amount: &config.KnownAmount{Currency: "EUR", Minor: 500}},
		// The difference was 1.00 when it was accepted; now it is 2.00.
		"2026-10-13/USD": {ID: "2026-10-13/USD", Reason: "late batch", Amount: &config.KnownAmount{Currency: "USD", Minor: 100}},
		"2026-10-01/USD": {ID: "2026-10-01/USD", Reason: "gone"},
	}}
	res := runFake(t, c, &fake{rows: compareRows()}).Results[0]
	if res.Violations != 2 || res.Known != 1 || totals(res.KnownTotals) != "5.00 EUR" || totals(res.Totals) != "32.00 USD" {
		t.Errorf("violations %d, known %d %s, totals %s", res.Violations, res.Known, totals(res.KnownTotals), totals(res.Totals))
	}
	if !strings.Contains(res.Samples[0].Note, "the amount changed") {
		t.Errorf("note %q", res.Samples[0].Note)
	}
	if len(res.Resolved) != 1 || res.Resolved[0].ID != "2026-10-01/USD" {
		t.Errorf("resolved %+v", res.Resolved)
	}
}

func TestCompareWithoutAmount(t *testing.T) {
	yaml := strings.Replace(compareConf, "      amount: {field: amount, unit: major, currency_field: currency}\n", "", 1)
	res := runFake(t, load(t, yaml), &fake{rows: compareRows()}).Results[0]
	if res.Violations != 3 || len(res.Totals) != 0 || res.Samples[0].Amount != nil {
		t.Fatalf("%+v", res)
	}
	if got := res.Samples[0].Fields; len(got) != 3 || got[2].Value != "n differs" {
		t.Errorf("fields %+v", got)
	}
}

func TestCompareFails(t *testing.T) {
	good := compareRows()
	for _, tc := range []struct {
		name string
		edit func(map[string][]source.Row)
		yaml func(string) string
		want string
	}{
		{"duplicate left", func(r map[string][]source.Row) {
			r["daily left"] = append(r["daily left"], r["daily left"][0])
		}, nil, "left: key 2026-10-11/USD is returned twice; group the rows by the key"},
		{"duplicate right", func(r map[string][]source.Row) {
			r["daily right"] = append(r["daily right"], r["daily right"][0])
		}, nil, "right: key 2026-10-14/EUR is returned twice"},
		{"no key column", func(r map[string][]source.Row) {
			r["daily right"] = []source.Row{row("date,currency,amount,n", "2026-10-11", "USD", "1", "1")}
		}, nil, "right: row 1 has no day column (the key); the side returns date, currency, amount, n"},
		{"count with a fraction", func(r map[string][]source.Row) {
			r["daily left"] = []source.Row{row("day,currency,amount,n", "2026-10-11", "USD", 1.0, 2.5)}
		}, nil, "left: key 2026-10-11/USD: n = 2.5: not an integer"},
		{"amount", func(r map[string][]source.Row) {
			r["daily right"] = []source.Row{row("day,currency,amount,n", "2026-10-11", "USD", "1.001", "1")}
		}, nil, "right: key 2026-10-11/USD: amount = 1.001"},
		{"max_rows", nil, func(y string) string {
			return strings.Replace(y, "tolerance_minor: 1", "tolerance_minor: 1\n      max_rows: 3", 1)
		}, "left: more than max_rows (3) rows"},
		{"broken source", nil, func(y string) string {
			return strings.Replace(y, "{source: stats, query: q}", "{source: broken, query: q}", 1)
		}, "source broken is down: dial tcp: connection refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := map[string][]source.Row{}
			for k, v := range good {
				rows[k] = append([]source.Row(nil), v...)
			}
			if tc.edit != nil {
				tc.edit(rows)
			}
			yaml := compareConf
			if tc.yaml != nil {
				yaml = tc.yaml(yaml)
			}
			res := runFake(t, load(t, yaml), &fake{rows: rows}).Results[0]
			if res.Status != StatusFailed || !strings.Contains(res.Error, tc.want) {
				t.Errorf("status %s, error %q, want %q", res.Status, res.Error, tc.want)
			}
		})
	}
}

func TestCompareSideError(t *testing.T) {
	f := &fake{rows: compareRows(), errs: map[string]error{"daily right": errors.New("Code: 60. Unknown table")}}
	res := runFake(t, load(t, compareConf), f).Results[0]
	if res.Status != StatusFailed || res.Error != "right: Code: 60. Unknown table" {
		t.Errorf("%s: %q", res.Status, res.Error)
	}
}

func TestSub(t *testing.T) {
	for _, tc := range []struct {
		a, b int64
		ok   bool
	}{
		{5, 3, true}, {-5, 3, true}, {math.MaxInt64, 0, true}, {math.MinInt64, 0, true},
		{math.MaxInt64, -1, false}, {math.MinInt64, 1, false}, {-1, math.MinInt64, true}, {0, math.MinInt64, false},
	} {
		d, ok := sub(tc.a, tc.b)
		if ok != tc.ok || ok && d != tc.a-tc.b {
			t.Errorf("sub(%d, %d) = %d, %v", tc.a, tc.b, d, ok)
		}
	}
}

// Both sides read the same window.
func TestCompareWindow(t *testing.T) {
	yaml := strings.Replace(compareConf, "  - name: daily\n", "  - name: daily\n    window: 72h\n", 1)
	yaml = strings.Replace(yaml, "pipeline: [{$match: {a: 1}}]", `pipeline: [{$match: {$expr: {$gte: ["$at", "$$since"]}}}]`, 1)
	yaml = strings.Replace(yaml, "query: q}", "query: \"select {since:DateTime64(6, 'UTC')}\"}", 1)
	f := &fake{rows: compareRows()}
	r := runScope(t, load(t, yaml), Window, f)
	want := r.StartedAt.Add(-72 * time.Hour)
	if res := r.Results[0]; res.Scope != Window || !res.Since.Equal(want) || len(res.Resolved) != 0 {
		t.Errorf("scope %s, since %s", res.Scope, res.Since)
	}
	if !f.since["daily left"].Equal(want) || !f.since["daily right"].Equal(want) {
		t.Errorf("left got %s, right got %s, want %s", f.since["daily left"], f.since["daily right"], want)
	}
}

// Both sides read up to the same moment, on a full run too.
func TestCompareSettle(t *testing.T) {
	yaml := strings.Replace(compareConf, "  - name: daily\n", "  - name: daily\n    settle: 15m\n", 1)
	yaml = strings.Replace(yaml, "pipeline: [{$match: {a: 1}}]", `pipeline: [{$match: {$expr: {$lt: ["$at", "$$until"]}}}]`, 1)
	yaml = strings.Replace(yaml, "query: q}", "query: \"select {until:DateTime64(6, 'UTC')}\"}", 1)
	f := &fake{rows: compareRows()}
	r := runFake(t, load(t, yaml), f)
	want := r.StartedAt.Add(-15 * time.Minute)
	if res := r.Results[0]; res.Scope != Full || !res.Until.Equal(want) || res.Settle != 15*time.Minute || res.Violations != 3 {
		t.Errorf("scope %s, until %s, settle %s, violations %d", res.Scope, res.Until, res.Settle, res.Violations)
	}
	if !f.until["daily left"].Equal(want) || !f.until["daily right"].Equal(want) || !f.since["daily left"].IsZero() {
		t.Errorf("left got %s..%s, right got ..%s, want ..%s", f.since["daily left"], f.until["daily left"], f.until["daily right"], want)
	}
}
