package check

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// fake returns the rows set for each check name, or the error.
type fake struct {
	rows   map[string][]source.Row
	errs   map[string]error
	closed bool
}

func (f *fake) Query(_ context.Context, ch config.Check, each func(source.Row) error) error {
	if err := f.errs[ch.Name]; err != nil {
		return err
	}
	for _, r := range f.rows[ch.Name] {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *fake) Close(context.Context) error {
	f.closed = true
	return nil
}

func row(cols string, values ...any) source.Row {
	return source.Row{Columns: strings.Split(cols, ","), Values: values}
}

const conf = `
version: 1
sources:
  billing: {postgres: {dsn_env: X}}
  broken: {postgres: {dsn_env: Y}}
checks:
  - name: rebills
    description: Charged twice for one period.
    source: billing
    query: q
    id: id
    amount: {field: amount, unit: major, currency_field: currency}
    show: [email, n, missing]
    mask: [email]
    samples: 2
  - name: clean
    source: billing
    query: q
    id: id
  - name: soft
    source: billing
    query: q
    id: id
    severity: warn
  - name: unreachable
    source: broken
    query: q
    id: id
`

func load(t *testing.T, yaml string) config.Config {
	t.Helper()
	c, err := config.Parse([]byte(yaml), ".")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func clock() func() time.Time {
	t0 := time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return t0.Add(time.Duration(n) * time.Millisecond)
	}
}

func runFake(t *testing.T, c config.Config, f *fake) Report {
	t.Helper()
	opens := map[string]int{}
	r := Run(context.Background(), c, func(_ context.Context, name string, _ config.Source, warn func(string)) (source.Source, error) {
		opens[name]++
		if opens[name] > 1 {
			t.Errorf("source %s opened %d times", name, opens[name])
		}
		if name == "broken" {
			return nil, errors.New("source broken: connection refused")
		}
		warn("source " + name + ": a warning")
		return f, nil
	}, clock())
	if !f.closed {
		t.Error("source not closed")
	}
	return r
}

func TestRun(t *testing.T) {
	f := &fake{rows: map[string][]source.Row{
		"rebills": {
			row("id,amount,currency,email,n", "s1", "26.99", "USD", "anna@example.com", int64(2)),
			row("id,amount,currency,email,n", "s2", 10.1, "usd", "bob@example.com", int64(2)),
			row("id,amount,currency,email,n", "s3", source.Decimal{Coef: big.NewInt(500), Scale: -2}, "EUR", nil, int64(3)),
		},
		"soft": {row("id", int64(7))},
	}}
	r := runFake(t, load(t, conf), f)
	if !r.StartedAt.Equal(time.Date(2026, 10, 8, 6, 0, 0, 1e6, time.UTC)) {
		t.Errorf("started at %s", r.StartedAt)
	}
	if len(r.Warnings) != 1 || r.Warnings[0] != "source billing: a warning" {
		t.Errorf("warnings %v", r.Warnings)
	}
	if len(r.Results) != 4 {
		t.Fatalf("%d results", len(r.Results))
	}
	reb := r.Results[0]
	if reb.Status != StatusViolations || reb.Violations != 3 || reb.Description != "Charged twice for one period." {
		t.Errorf("rebills: %+v", reb)
	}
	if len(reb.Totals) != 2 || reb.Totals[0].String() != "5.00 EUR" || reb.Totals[1].String() != "37.09 USD" {
		t.Errorf("totals %v", reb.Totals)
	}
	if len(reb.Samples) != 2 {
		t.Fatalf("%d samples, want 2", len(reb.Samples))
	}
	s := reb.Samples[0]
	if s.ID != "s1" || s.Amount.String() != "26.99 USD" {
		t.Errorf("sample %+v", s)
	}
	want := []Field{{"email", "a***@example.com"}, {"n", "2"}, {"missing", "(missing)"}}
	for i, f := range s.Fields {
		if f != want[i] {
			t.Errorf("field %d = %+v, want %+v", i, f, want[i])
		}
	}
	if reb.Duration != time.Millisecond {
		t.Errorf("duration %s", reb.Duration)
	}
	if c := r.Results[1]; c.Status != StatusOK || c.Violations != 0 || len(c.Totals) != 0 {
		t.Errorf("clean: %+v", c)
	}
	if c := r.Results[2]; c.Status != StatusViolations || c.Samples[0].ID != "7" {
		t.Errorf("soft: %+v", c)
	}
	if c := r.Results[3]; c.Status != StatusFailed || !strings.Contains(c.Error, "connection refused") {
		t.Errorf("unreachable: %+v", c)
	}
	if code := r.ExitCode(); code != 2 {
		t.Errorf("exit code %d", code)
	}
}

func TestRunFailsLoudlyOnBadRows(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[:1]
	for _, tt := range []struct {
		row  source.Row
		want string
	}{
		{row("amount,currency", "1", "USD"), "row 1 has no id column (the id); the query returns amount, currency"},
		{row("id,amount,currency", "s1", 10.105, "USD"), "row s1: amount = 10.105"},
		{row("id,amount,currency", "s1", "1", "XXX"), `row s1: unknown currency "XXX"`},
		{row("id,amount,currency", "s1", "1", nil), "row s1: currency currency is null"},
		{row("id,currency", "s1", "USD"), "row s1: no amount column (the amount)"},
		{row("id,amount", "s1", "1"), "row s1: no currency column (the currency)"},
	} {
		r := runFake(t, c, &fake{rows: map[string][]source.Row{"rebills": {tt.row}}})
		if res := r.Results[0]; res.Status != StatusFailed || !strings.Contains(res.Error, tt.want) {
			t.Errorf("want %q, got %+v", tt.want, res)
		}
	}
}

func TestRunTotalOverflow(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[:1]
	c.Checks[0].Amount.Unit = "minor"
	big := row("id,amount,currency", "s", int64(math.MaxInt64), "USD")
	r := runFake(t, c, &fake{rows: map[string][]source.Row{"rebills": {big, big}}})
	if res := r.Results[0]; res.Status != StatusFailed || !strings.Contains(res.Error, "does not fit") {
		t.Errorf("got %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[1:2]
	f := &fake{errs: map[string]error{"clean": context.DeadlineExceeded}}
	r := runFake(t, c, f)
	if res := r.Results[0]; !strings.HasPrefix(res.Error, "timed out after 30s") {
		t.Errorf("got %q", res.Error)
	}
}

func TestExitCode(t *testing.T) {
	for _, tt := range []struct {
		results []Result
		want    int
	}{
		{nil, 0},
		{[]Result{{Status: StatusOK}}, 0},
		{[]Result{{Status: StatusViolations, Severity: "warn"}}, 0},
		{[]Result{{Status: StatusViolations, Severity: "error"}}, 1},
		{[]Result{{Status: StatusViolations, Severity: "error"}, {Status: StatusFailed}}, 2},
	} {
		if got := (Report{Results: tt.results}).ExitCode(); got != tt.want {
			t.Errorf("%+v: %d, want %d", tt.results, got, tt.want)
		}
	}
}

func TestMask(t *testing.T) {
	for in, want := range map[string]string{
		"anna@example.com": "a***@example.com",
		"4111111111111111": "4***",
		"ab":               "***",
		"":                 "",
		"null":             "null",
		"\u00d6lga":        "\u00d6***", // one rune, not one byte
	} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunKnown(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[:1]
	exp := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC) // expired before the run
	c.Known = map[string]map[string]config.Known{"rebills": {
		"s1": {ID: "s1", Reason: "refunded by hand", Amount: &config.KnownAmount{Currency: "USD", Minor: 2699}},
		"s2": {ID: "s2", Reason: "partial refund", Amount: &config.KnownAmount{Currency: "USD", Minor: 500}},
		"s3": {ID: "s3", Reason: "until the migration", Until: "2026-10-07", Expires: exp},
		"s4": {ID: "s4", Reason: "test account"},
		"s9": {ID: "s9", Reason: "fixed in the data"},
	}}
	r := runFake(t, c, &fake{rows: map[string][]source.Row{"rebills": {
		row("id,amount,currency,email,n", "s1", "26.99", "USD", "anna@example.com", int64(2)),
		row("id,amount,currency,email,n", "s2", "10.10", "USD", nil, int64(2)),
		row("id,amount,currency,email,n", "s3", "5.00", "EUR", nil, int64(2)),
		row("id,amount,currency,email,n", "s4", "1.00", "EUR", nil, int64(2)),
		row("id,amount,currency,email,n", "s5", "2.00", "USD", nil, int64(2)),
	}}})
	res := r.Results[0]
	if res.Status != StatusViolations || res.Violations != 3 || res.Known != 2 {
		t.Fatalf("got %+v", res)
	}
	if got := fmt.Sprint(res.Totals); got != "[5.00 EUR 12.10 USD]" {
		t.Errorf("totals %s", got)
	}
	if got := fmt.Sprint(res.KnownTotals); got != "[1.00 EUR 26.99 USD]" {
		t.Errorf("known totals %s", got)
	}
	notes := map[string]string{}
	for _, s := range res.Samples {
		notes[s.ID] = s.Note
		if s.Reason != "" {
			t.Errorf("%s counts as new but has reason %q", s.ID, s.Reason)
		}
	}
	want := map[string]string{
		"s2": "known at 5.00 USD (partial refund), the amount changed",
		"s3": "known until 2026-10-07 (until the migration), expired",
	}
	for id, note := range want {
		if notes[id] != note {
			t.Errorf("%s: note %q, want %q", id, notes[id], note)
		}
	}
	// samples: 2 in conf, for new and known violations each.
	if len(res.Samples) != 2 || len(res.KnownSamples) != 2 {
		t.Fatalf("%d samples, %d known samples", len(res.Samples), len(res.KnownSamples))
	}
	if k := res.KnownSamples[0]; k.ID != "s1" || k.Reason != "refunded by hand" || k.Fields[0].Value != "a***@example.com" {
		t.Errorf("known sample %+v", k)
	}
	if len(res.Resolved) != 1 || res.Resolved[0] != (Resolved{ID: "s9", Reason: "fixed in the data"}) {
		t.Errorf("resolved %+v", res.Resolved)
	}
}

func TestRunOnlyKnownIsOK(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[2:3]
	c.Known = map[string]map[string]config.Known{"soft": {"7": {ID: "7", Reason: "r"}}}
	r := runFake(t, c, &fake{rows: map[string][]source.Row{"soft": {row("id", int64(7))}}})
	if res := r.Results[0]; res.Status != StatusOK || res.Violations != 0 || res.Known != 1 || len(res.Resolved) != 0 {
		t.Errorf("got %+v", res)
	}
	if code := r.ExitCode(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// A failed run says nothing about which known violations are gone.
func TestRunFailedResolvesNothing(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[1:2]
	c.Known = map[string]map[string]config.Known{"clean": {"1": {ID: "1", Reason: "r"}}}
	r := runFake(t, c, &fake{errs: map[string]error{"clean": errors.New("boom")}})
	if res := r.Results[0]; res.Status != StatusFailed || len(res.Resolved) != 0 {
		t.Errorf("got %+v", res)
	}
}
