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

// Scope says how much of the data a run reads.
type Scope string

const (
	// Full reads every row. A check with a window gets the zero time as the start.
	Full Scope = "full"
	// Window reads the last window of every check that has one; the checks
	// without a window read every row.
	Window Scope = "window"
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
	// RightSource is the source of the right side of a compare check; Source
	// is the left one.
	RightSource string
	Severity    string
	Status      string
	Violations  int64   // rows that are not known violations
	Totals      []Money // of Violations, one per currency, sorted by code
	Samples     []Sample
	Error       string
	Duration    time.Duration

	// Scope is Window when the check read only the rows since Since, the
	// start of its Window.
	Scope  Scope
	Since  time.Time
	Window time.Duration
	// Until, when set, is the end of the rows a compare check with settle read:
	// newer rows are still on their way from one side to the other.
	Until  time.Time
	Settle time.Duration

	// Known violations: rows listed in the known file, with their reasons. They do
	// not count towards Violations or the status.
	Known        int64
	KnownTotals  []Money
	KnownSamples []Sample
	// Resolved are entries of the known file the check no longer finds. Only a
	// full run can say that: a window run does not see older rows.
	Resolved []Resolved

	// Expected is what the expect queries found: one for a check, one per side
	// that has it for a compare check. It is set when the check failed because
	// of it, too.
	Expected []Expected
}

// Expected is the number an expect query found and the least it had to find.
type Expected struct {
	// Side is "left" or "right" for a compare check, "" for other checks.
	Side  string
	Found int64
	Min   int64
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
func Run(ctx context.Context, cfg config.Config, scope Scope, open Opener, now func() time.Time) Report {
	return RunExport(ctx, cfg, scope, open, now, nil)
}

// RunExport is Run that also calls export for every violation, new and known, in
// the order the checks find them, with the fields of a sample: a list of every
// row without holding them all in memory. An error from export fails the check.
func RunExport(ctx context.Context, cfg config.Config, scope Scope, open Opener, now func() time.Time, export func(Sample) error) Report {
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
	get := func(name string) (source.Source, error) {
		o, ok := sources[name]
		if !ok {
			o.src, o.err = open(ctx, name, cfg.Sources[name], warn)
			sources[name] = o
		}
		return o.src, o.err
	}
	for _, ch := range cfg.Checks {
		start := now()
		res := Result{
			Name: ch.Name, Description: ch.Description, Source: ch.Source, Severity: ch.Severity, Scope: Full,
		}
		if scope == Window && ch.Window > 0 {
			res.Scope, res.Since, res.Window = Window, r.StartedAt.Add(-ch.Window), ch.Window
		}
		if ch.Settle > 0 {
			res.Until, res.Settle = r.StartedAt.Add(-ch.Settle), ch.Settle
		}
		c := &collector{cur: cfg.Money, ch: ch, known: cfg.Known[ch.Name], at: r.StartedAt, res: &res, export: export}
		var err error
		if ch.Compare != nil {
			res.Source, res.RightSource = ch.Compare.Left.Source, ch.Compare.Right.Source
			err = runCompare(ctx, ch, func(name string) string { return cfg.Sources[name].Kind() }, get, c)
		} else {
			var src source.Source
			if src, err = get(ch.Source); err == nil {
				if err = runExpect(ctx, ch, "", cfg.Sources[ch.Source].Kind(), src, &res); err == nil {
					err = runOne(ctx, ch, src, c)
				}
			}
		}
		if err != nil {
			res.Status, res.Error = StatusFailed, err.Error()
		} else {
			c.finish()
		}
		res.Duration = now().Sub(start)
		r.Results = append(r.Results, res)
	}
	return r
}

// WithoutExpectWarning is the warning about the checks that have no expect query.
func WithoutExpectWarning(names []string) string {
	return "checks without expect, which cannot tell no violations from no data: " + strings.Join(names, ", ")
}

// runExpect runs the expect query of ch, a check or a side named side, and
// records what it found. It fails when the number is below the minimum, so the
// check, which would see no data, does not run. A check without expect passes.
func runExpect(ctx context.Context, ch config.Check, side, kind string, src source.Source, res *Result) error {
	if ch.Expect == nil {
		return nil
	}
	ex := ch.ExpectCheck(kind)
	var found int64
	rows := 0
	err := src.Query(ctx, ex, res.Since, res.Until, func(row source.Row) error {
		rows++
		if rows > 1 {
			return errors.New("more than one row; expect returns one number, such as a count")
		}
		var values []any
		for i, col := range row.Columns {
			// The _id of $group: {_id: null, n: {$sum: 1}}.
			if kind == "mongodb" && col == "_id" {
				continue
			}
			values = append(values, row.Values[i])
		}
		if len(values) != 1 {
			return fmt.Errorf("%d columns (%s); expect returns one number, such as a count", len(row.Columns), strings.Join(row.Columns, ", "))
		}
		n, err := integer(values[0])
		if err != nil {
			return fmt.Errorf("%s: %w", Display(values[0]), err)
		}
		found = n
		return nil
	})
	if err != nil {
		return fmt.Errorf("expect: %s", errorText(err, ex))
	}
	// No rows is 0: $count over nothing returns no document.
	res.Expected = append(res.Expected, Expected{Side: side, Found: found, Min: *ch.Expect.Min})
	if found < *ch.Expect.Min {
		return fmt.Errorf("expect found %d, at least %d required", found, *ch.Expect.Min)
	}
	return nil
}

func runOne(ctx context.Context, ch config.Check, src source.Source, c *collector) error {
	var rows int64
	err := src.Query(ctx, ch, c.res.Since, time.Time{}, func(row source.Row) error {
		rows++
		id, ok := row.Get(ch.ID)
		if !ok {
			return fmt.Errorf("row %d has no %s column (the id); the query returns %s", rows, ch.ID, strings.Join(row.Columns, ", "))
		}
		s := Sample{ID: Display(id)}
		if ch.Amount != nil {
			m, err := amount(c.cur, *ch.Amount, row)
			if err != nil {
				return fmt.Errorf("row %s: %w", s.ID, err)
			}
			s.Amount = &m
		}
		return c.add(s, func() []Field { return fields(ch, row) })
	})
	if err != nil {
		return errors.New(errorText(err, ch))
	}
	return nil
}

// collector sorts the violations of one check into new and known ones and adds
// them up.
type collector struct {
	cur   money.Currencies
	ch    config.Check
	known map[string]config.Known
	at    time.Time
	res   *Result
	// abs adds up the absolute amounts: the differences of a compare check.
	abs bool
	// export, when set, gets every violation.
	export func(Sample) error

	totals, knownTotals map[string]*Money
	seen                map[string]bool
}

// add counts one violation. fields is called only for a row that becomes a sample.
func (c *collector) add(s Sample, fields func() []Field) error {
	if c.seen == nil {
		c.totals, c.knownTotals, c.seen = map[string]*Money{}, map[string]*Money{}, map[string]bool{}
	}
	k, isKnown := c.known[s.ID]
	if isKnown {
		c.seen[s.ID] = true
		s.Reason = k.Reason
		if s.Note = stale(c.cur, k, s.Amount, c.at); s.Note != "" {
			isKnown, s.Reason = false, ""
		}
	}
	res := c.res
	count, sums, samples := &res.Violations, c.totals, &res.Samples
	if isKnown {
		count, sums, samples = &res.Known, c.knownTotals, &res.KnownSamples
	}
	*count++
	if s.Amount != nil {
		m := *s.Amount
		if c.abs && m.Minor < 0 {
			if m.Minor == math.MinInt64 {
				return fmt.Errorf("the difference in %s does not fit in int64 minor units", m.Currency)
			}
			m.Minor = -m.Minor
		}
		if err := add(sums, m); err != nil {
			return err
		}
	}
	keep := len(*samples) < *c.ch.Samples
	if keep || c.export != nil {
		s.Fields = fields()
	}
	if keep {
		*samples = append(*samples, s)
	}
	if c.export != nil {
		return c.export(s)
	}
	return nil
}

// Columns names the fields of the samples of ch, in their order: the shown
// columns, or for a compare check both sides, each count on both sides and the
// problem.
func Columns(ch config.Check) []string {
	cmp := ch.Compare
	if cmp == nil {
		return append([]string(nil), ch.Show...)
	}
	var cols []string
	if cmp.Amount != nil {
		cols = append(cols, "left", "right")
	}
	for _, n := range cmp.Counts {
		cols = append(cols, n+" left", n+" right")
	}
	return append(cols, "problem")
}

// finish sets the totals, the known entries no longer found and the status of a
// check that ran to the end.
func (c *collector) finish() {
	res := c.res
	res.Totals, res.KnownTotals = sorted(c.totals), sorted(c.knownTotals)
	for id, k := range c.known {
		if !c.seen[id] && res.Scope == Full {
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

// Span writes a window the way people say it: 24h, 90m, 168h.
func Span(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return d.String()
	}
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
