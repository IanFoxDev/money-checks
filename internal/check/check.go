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
	Violations  int64   // rows that are not known violations
	Totals      []Money // of Violations, one per currency, sorted by code
	Samples     []Sample
	Error       string
	Duration    time.Duration

	// Known violations: rows listed in the known file, with their reasons. They do
	// not count towards Violations or the status.
	Known        int64
	KnownTotals  []Money
	KnownSamples []Sample
	// Resolved are entries of the known file the check no longer finds.
	Resolved []Resolved
}

// Resolved is a known violation that is gone and can be removed from the file.
type Resolved struct {
	ID     string
	Reason string
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
	// Reason is why a known violation is accepted.
	Reason string
	// Note says why a row listed in the known file counts again: the entry
	// expired or the amount changed.
	Note string
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
			runOne(ctx, cfg.Money, ch, cfg.Known[ch.Name], r.StartedAt, o.src, &res)
		}
		res.Duration = now().Sub(start)
		r.Results = append(r.Results, res)
	}
	return r
}

func runOne(ctx context.Context, cur money.Currencies, ch config.Check, known map[string]config.Known, at time.Time, src source.Source, res *Result) {
	totals, knownTotals := map[string]*Money{}, map[string]*Money{}
	seen := map[string]bool{}
	var rows int64
	err := src.Query(ctx, ch, func(row source.Row) error {
		rows++
		id, ok := row.Get(ch.ID)
		if !ok {
			return fmt.Errorf("row %d has no %s column (the id); the query returns %s", rows, ch.ID, strings.Join(row.Columns, ", "))
		}
		s := Sample{ID: Display(id)}
		if ch.Amount != nil {
			m, err := amount(cur, *ch.Amount, row)
			if err != nil {
				return fmt.Errorf("row %s: %w", s.ID, err)
			}
			s.Amount = &m
		}
		k, isKnown := known[s.ID]
		if isKnown {
			seen[s.ID] = true
			s.Reason = k.Reason
			if s.Note = stale(cur, k, s.Amount, at); s.Note != "" {
				isKnown, s.Reason = false, ""
			}
		}
		count, sums, samples := &res.Violations, totals, &res.Samples
		if isKnown {
			count, sums, samples = &res.Known, knownTotals, &res.KnownSamples
		}
		*count++
		if s.Amount != nil {
			if err := add(sums, *s.Amount); err != nil {
				return err
			}
		}
		if len(*samples) < *ch.Samples {
			s.Fields = fields(ch, row)
			*samples = append(*samples, s)
		}
		return nil
	})
	if err != nil {
		res.Status, res.Error = StatusFailed, errorText(err, ch)
		return
	}
	res.Totals, res.KnownTotals = sorted(totals), sorted(knownTotals)
	for id, k := range known {
		if !seen[id] {
			res.Resolved = append(res.Resolved, Resolved{ID: id, Reason: k.Reason})
		}
	}
	sort.Slice(res.Resolved, func(i, j int) bool { return res.Resolved[i].ID < res.Resolved[j].ID })
	res.Status = StatusOK
	if res.Violations > 0 {
		res.Status = StatusViolations
	}
}

// stale says why a known entry no longer covers the row, or "" when it does.
func stale(cur money.Currencies, k config.Known, m *Money, at time.Time) string {
	if !k.Expires.IsZero() && !at.Before(k.Expires) {
		return fmt.Sprintf("known until %s (%s), expired", k.Until, k.Reason)
	}
	if k.Amount != nil && m != nil && (k.Amount.Currency != m.Currency || k.Amount.Minor != m.Minor) {
		exp, _ := cur.Exponent(k.Amount.Currency)
		was := Money{Currency: k.Amount.Currency, Minor: k.Amount.Minor, Exponent: exp}
		return fmt.Sprintf("known at %s (%s), the amount changed", was, k.Reason)
	}
	return ""
}

func add(totals map[string]*Money, m Money) error {
	t, ok := totals[m.Currency]
	if !ok {
		t = &Money{Currency: m.Currency, Exponent: m.Exponent}
		totals[m.Currency] = t
	}
	if m.Minor > 0 && t.Minor > math.MaxInt64-m.Minor || m.Minor < 0 && t.Minor < math.MinInt64-m.Minor {
		return fmt.Errorf("the total in %s does not fit in int64 minor units", m.Currency)
	}
	t.Minor += m.Minor
	return nil
}

func sorted(totals map[string]*Money) []Money {
	var out []Money
	for _, t := range totals {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// fields are the shown columns of a row, masked where the check says so.
func fields(ch config.Check, row source.Row) []Field {
	masked := map[string]bool{}
	for _, f := range ch.Mask {
		masked[f] = true
	}
	var out []Field
	for _, f := range ch.Show {
		v, ok := row.Get(f)
		value := Display(v)
		switch {
		case !ok:
			value = "(missing)"
		case masked[f]:
			value = Mask(value)
		}
		out = append(out, Field{Name: f, Value: value})
	}
	return out
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
