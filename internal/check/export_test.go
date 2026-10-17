package check

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

func TestRunExport(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[:1] // rebills: two samples, show [email, n, missing]
	c.Known = map[string]map[string]config.Known{"rebills": {"s2": {Check: "rebills", ID: "s2", Reason: "refunded"}}}
	f := &fake{rows: map[string][]source.Row{"rebills": {
		row("id,amount,currency,email,n", "s1", "26.99", "USD", "anna@example.com", int64(2)),
		row("id,amount,currency,email,n", "s2", 10.1, "USD", "bob@example.com", int64(2)),
		row("id,amount,currency,email,n", "s3", source.Decimal{Coef: big.NewInt(500), Scale: -2}, "EUR", nil, int64(3)),
		row("id,amount,currency,email,n", "s4", "1", "USD", "eve@example.com", int64(2)),
	}}}
	var got []string
	r := RunExport(context.Background(), c, Full, func(context.Context, string, config.Source, func(string)) (source.Source, error) {
		return f, nil
	}, clock(), func(s Sample) error {
		parts := []string{s.ID, s.Amount.String()}
		for _, f := range s.Fields {
			parts = append(parts, f.Name+"="+f.Value)
		}
		got = append(got, strings.Join(append(parts, "reason="+s.Reason), " "))
		return nil
	})
	want := []string{
		"s1 26.99 USD email=a***@example.com n=2 missing=(missing) reason=",
		"s2 10.10 USD email=b***@example.com n=2 missing=(missing) reason=refunded",
		"s3 5.00 EUR email=null n=3 missing=(missing) reason=",
		"s4 1.00 USD email=e***@example.com n=2 missing=(missing) reason=",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("exported\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The report keeps its samples.
	if res := r.Results[0]; res.Violations != 3 || res.Known != 1 || len(res.Samples) != 2 {
		t.Errorf("%+v", res)
	}
	if cols := strings.Join(Columns(c.Checks[0]), ","); cols != "email,n,missing" {
		t.Errorf("columns %s", cols)
	}
}

func TestRunExportError(t *testing.T) {
	c := load(t, conf)
	c.Checks = c.Checks[:1]
	f := &fake{rows: map[string][]source.Row{"rebills": {row("id,amount,currency,email,n", "s1", "1", "USD", "a@b", int64(2))}}}
	r := RunExport(context.Background(), c, Full, func(context.Context, string, config.Source, func(string)) (source.Source, error) {
		return f, nil
	}, clock(), func(Sample) error { return errors.New("disk full") })
	if res := r.Results[0]; res.Status != StatusFailed || !strings.Contains(res.Error, "disk full") {
		t.Errorf("%+v", res)
	}
}

func TestColumnsOfCompare(t *testing.T) {
	c := load(t, compareConf)
	r := runFake(t, c, &fake{rows: compareRows()})
	var names []string
	for _, f := range r.Results[0].Samples[0].Fields {
		names = append(names, f.Name)
	}
	if got, want := strings.Join(Columns(c.Checks[0]), ","), strings.Join(names, ","); got != want {
		t.Errorf("columns %s, fields %s", got, want)
	}
}
