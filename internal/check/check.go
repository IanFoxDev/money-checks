// Package check runs the checks of a configuration and collects what they found.
package check

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/money"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// Status of a check after a run.
const (
	StatusOK         = "ok"
	StatusViolations = "violations"
	StatusFailed     = "failed"
)

// Report is the outcome of one run.
type Report struct {
	StartedAt time.Time
	Results   []Result
	Warnings  []string
}

// Result is the outcome of one check.
type Result struct {
	Name        string
	Description string
	Source      string
	Severity    string
	Status      string
	Violations  int64
	Totals      []Money // one per currency, sorted by code
	Samples     []Sample
	Error       string
	Duration    time.Duration
}

// Money is an amount in minor units of a currency.
type Money struct {
	Currency string
	Minor    int64
	Exponent int
}

// String is "323.88 USD".
func (m Money) String() string {
	return money.FormatMinor(m.Minor, m.Exponent) + " " + m.Currency
}

// Sample is one violating row as it may be shown: the id, the amount and the
// columns the check lists in show, masked where it says so.
type Sample struct {
	ID     string
	Amount *Money
	Fields []Field
}

// Field is a shown column.
type Field struct {
	Name  string
	Value string
}

// Opener connects to a source; source.Open in production.
type Opener func(ctx context.Context, name string, s config.Source, warn func(string)) (source.Source, error)

// Run runs every check in the order of the configuration, one at a time, so a
// production database gets one query at a time. Each source is opened once, when
// the first check that uses it runs. A source that cannot be opened fails its
// checks; the other checks still run.
func Run(ctx context.Context, cfg config.Config, open Opener, now func() time.Time) Report {
	r := Report{StartedAt: now().UTC()}
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
	for _, ch := range cfg.Checks {
		o, ok := sources[ch.Source]
		if !ok {
			o.src, o.err = open(ctx, ch.Source, cfg.Sources[ch.Source], warn)
			sources[ch.Source] = o
		}
		start := now()
		res := Result{
			Name: ch.Name, Description: ch.Description, Source: ch.Source, Severity: ch.Severity,
		}
		if o.err != nil {
			res.Status, res.Error = StatusFailed, o.err.Error()
		} else {
			runOne(ctx, cfg.Money, ch, o.src, &res)
		}
		res.Duration = now().Sub(start)
		r.Results = append(r.Results, res)
	}
	return r
}

func runOne(ctx context.Context, cur money.Currencies, ch config.Check, src source.Source, res *Result) {
	totals := map[string]*Money{}
	err := src.Query(ctx, ch, func(row source.Row) error {
		res.Violations++
		id, ok := row.Get(ch.ID)
		if !ok {
			return fmt.Errorf("row %d has no %s column (the id); the query returns %s", res.Violations, ch.ID, strings.Join(row.Columns, ", "))
		}
		s := Sample{ID: Display(id)}
		if ch.Amount != nil {
			m, err := amount(cur, *ch.Amount, row)
			if err != nil {
				return fmt.Errorf("row %s: %w", s.ID, err)
			}
			t, ok := totals[m.Currency]
			if !ok {
				t = &Money{Currency: m.Currency, Exponent: m.Exponent}
				totals[m.Currency] = t
			}
			if m.Minor > 0 && t.Minor > math.MaxInt64-m.Minor || m.Minor < 0 && t.Minor < math.MinInt64-m.Minor {
				return fmt.Errorf("the total in %s does not fit in int64 minor units", m.Currency)
			}
			t.Minor += m.Minor
			s.Amount = &m
		}
		if int64(len(res.Samples)) < int64(*ch.Samples) {
			masked := map[string]bool{}
			for _, f := range ch.Mask {
				masked[f] = true
			}
			for _, f := range ch.Show {
				v, ok := row.Get(f)
				value := Display(v)
				switch {
				case !ok:
					value = "(missing)"
				case masked[f]:
					value = Mask(value)
				}
				s.Fields = append(s.Fields, Field{Name: f, Value: value})
			}
			res.Samples = append(res.Samples, s)
		}
		return nil
	})
	if err != nil {
		res.Status, res.Error = StatusFailed, errorText(err, ch)
		return
	}
	for _, t := range totals {
		res.Totals = append(res.Totals, *t)
	}
	sort.Slice(res.Totals, func(i, j int) bool { return res.Totals[i].Currency < res.Totals[j].Currency })
	res.Status = StatusOK
	if res.Violations > 0 {
		res.Status = StatusViolations
	}
}

func errorText(err error, ch config.Check) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timed out after %s: %v", ch.Timeout, err)
	}
	return err.Error()
}

func amount(cur money.Currencies, a config.Amount, row source.Row) (Money, error) {
	code := a.Currency
	if a.CurrencyField != "" {
		v, ok := row.Get(a.CurrencyField)
		if !ok {
			return Money{}, fmt.Errorf("no %s column (the currency)", a.CurrencyField)
		}
		s, isString := v.(string)
		if !isString || s == "" {
			return Money{}, fmt.Errorf("currency %s is %s, want a code like USD", a.CurrencyField, Display(v))
		}
		code = s
	}
	code = strings.ToUpper(code)
	exp, ok := cur.Exponent(code)
	if !ok {
		return Money{}, fmt.Errorf("unknown currency %q; add it under currencies with its number of decimal places", code)
	}
	v, ok := row.Get(a.Field)
	if !ok {
		return Money{}, fmt.Errorf("no %s column (the amount)", a.Field)
	}
	minor, err := source.Minor(v, a.Unit, exp)
	if err != nil {
		return Money{}, fmt.Errorf("%s = %s: %w", a.Field, Display(v), err)
	}
	return Money{Currency: code, Minor: minor, Exponent: exp}, nil
}

// Display writes a row value for a report.
func Display(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// Mask keeps enough of a value to tell two apart in a report and no more:
// "anna@example.com" is "a***@example.com", "4111111111111111" is "4***".
func Mask(v string) string {
	if v == "" || v == "null" {
		return v
	}
	if at := strings.LastIndexByte(v, '@'); at > 0 {
		return v[:1] + "***" + v[at:]
	}
	r := []rune(v)
	if len(r) <= 2 {
		return "***"
	}
	return string(r[0]) + "***"
}

// ExitCode is 2 when a check failed, 1 when a check with severity error found
// violations, and 0 otherwise.
func (r Report) ExitCode() int {
	code := 0
	for _, res := range r.Results {
		switch {
		case res.Status == StatusFailed:
			return 2
		case res.Status == StatusViolations && res.Severity == "error":
			code = 1
		}
	}
	return code
}
