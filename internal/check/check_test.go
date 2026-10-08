package check

import (
	"context"
	"errors"
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
