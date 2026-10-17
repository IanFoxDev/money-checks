// Package report writes a check.Report as Markdown for people and JSON for scripts.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/money"
)

// Markdown writes a summary table and a section per check that did not pass.
// It is meant to be pasted into an incident, a ticket or a planning document.
func Markdown(w io.Writer, r check.Report) error {
	var b strings.Builder
	s := summarize(r)
	fmt.Fprintf(&b, "# money-checks report\n\n")
	fmt.Fprintf(&b, "Run at %s. %d checks: %d found violations, %d failed, %d passed.\n\n",
		r.StartedAt.Format("2006-01-02 15:04:05 UTC"), s.Checks, s.Violations, s.Failed, s.Passed)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n\n", w)
	}
	withKnown, withWindow, withExpect := false, false, false
	for _, res := range r.Results {
		withKnown = withKnown || res.Known > 0 || len(res.Resolved) > 0
		withWindow = withWindow || res.Scope == check.Window || res.Settle > 0
		withExpect = withExpect || len(res.Expected) > 0
	}
	b.WriteString("| Check | Severity | Status | Violations | Amount |")
	align := "|---|---|---|---:|---:|"
	if withKnown {
		b.WriteString(" Known |")
		align += "---:|"
	}
	if withWindow {
		b.WriteString(" Rows |")
		align += "---|"
	}
	if withExpect {
		b.WriteString(" Expect |")
		align += "---:|"
	}
	b.WriteString("\n" + align + "\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |", cell(res.Name), res.Severity, res.Status, count(res), cell(totals(res.Totals)))
		if withKnown {
			fmt.Fprintf(&b, " %s |", knownCount(res))
		}
		if withWindow {
			fmt.Fprintf(&b, " %s |", rows(res))
		}
		if withExpect {
			fmt.Fprintf(&b, " %s |", expected(res))
		}
		b.WriteString("\n")
	}
	for _, res := range r.Results {
		if res.Status == check.StatusOK && res.Known == 0 && len(res.Resolved) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", res.Name)
		if res.Description != "" {
			fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(res.Description))
		}
		if res.Status == check.StatusFailed {
			fmt.Fprintf(&b, "%s. The check failed: %s\n", sources(res), res.Error)
			continue
		}
		head := fmt.Sprintf("%s, severity %s", sources(res), res.Severity)
		switch {
		case res.Scope == check.Window && res.Settle > 0:
			head += fmt.Sprintf(", rows from %s to %s (%s)", res.Since.Format("2006-01-02 15:04 UTC"), res.Until.Format("2006-01-02 15:04 UTC"), rows(res))
		case res.Scope == check.Window:
			head += fmt.Sprintf(", rows since %s (%s)", res.Since.Format("2006-01-02 15:04 UTC"), rows(res))
		case res.Settle > 0:
			head += fmt.Sprintf(", rows before %s (%s)", res.Until.Format("2006-01-02 15:04 UTC"), rows(res))
		}
		if res.Violations == 0 {
			fmt.Fprintf(&b, "%s. No new violations.\n", head)
		} else {
			fmt.Fprintf(&b, "%s. %s", head, plural(res.Violations, "violation"))
			if len(res.Totals) > 0 {
				fmt.Fprintf(&b, ", %s", totals(res.Totals))
			}
			b.WriteString(".\n")
			samples(&b, res, res.Samples, res.Violations, "known file", func(s check.Sample) string { return s.Note })
		}
		if res.Known > 0 {
			fmt.Fprintf(&b, "\n%s, not counted", plural(res.Known, "known violation"))
			if len(res.KnownTotals) > 0 {
				fmt.Fprintf(&b, ", %s", totals(res.KnownTotals))
			}
			b.WriteString(".\n")
			samples(&b, res, res.KnownSamples, res.Known, "accepted because", func(s check.Sample) string { return s.Reason })
		}
		if len(res.Resolved) > 0 {
			b.WriteString("\nNo longer found; these entries can be removed from the known file:\n\n")
			for _, k := range res.Resolved {
				fmt.Fprintf(&b, "- %s: %s\n", cell(k.ID), cell(k.Reason))
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// sources names the source of a check, or both sides of a compare check.
func sources(res check.Result) string {
	if res.RightSource != "" {
		return fmt.Sprintf("Sources `%s` (left) and `%s` (right)", res.Source, res.RightSource)
	}
	return fmt.Sprintf("Source `%s`", res.Source)
}

// samples writes a table of rows: id, amount, the shown columns and, when any row
// has one, a column from the known file. Its header has a space, so it does not
// look like one of the shown columns. The amount of a compare check is the
// difference, right minus left.
func samples(b *strings.Builder, res check.Result, rows []check.Sample, total int64, extra string, text func(check.Sample) string) {
	if len(rows) == 0 {
		return
	}
	withExtra := false
	for _, s := range rows {
		withExtra = withExtra || text(s) != ""
	}
	header := []string{"id"}
	if rows[0].Amount != nil {
		if res.RightSource != "" {
			header = append(header, "difference")
		} else {
			header = append(header, "amount")
		}
	}
	for _, f := range rows[0].Fields {
		header = append(header, f.Name)
	}
	if withExtra {
		header = append(header, extra)
	}
	fmt.Fprintf(b, "\n| %s |\n|%s\n", strings.Join(cells(header), " | "), strings.Repeat("---|", len(header)))
	for _, sm := range rows {
		row := []string{sm.ID}
		if sm.Amount != nil {
			row = append(row, sm.Amount.String())
		}
		for _, f := range sm.Fields {
			row = append(row, f.Value)
		}
		if withExtra {
			row = append(row, text(sm))
		}
		fmt.Fprintf(b, "| %s |\n", strings.Join(cells(row), " | "))
	}
	if n := int64(len(rows)); n < total {
		fmt.Fprintf(b, "\nShowing %d of %d.\n", n, total)
	}
}

// rows says which rows a check read: "last 24h" or "all", and "up to 15m ago"
// with settle.
func rows(res check.Result) string {
	s := "all"
	if res.Scope == check.Window {
		s = "last " + check.Span(res.Window)
	}
	if res.Settle > 0 {
		s += " up to " + check.Span(res.Settle) + " ago"
	}
	return s
}

// expected is what the expect queries found: "1532", or "left 40, right 12"
// for a compare check, and "-" for a check without expect.
func expected(res check.Result) string {
	if len(res.Expected) == 0 {
		return "-"
	}
	parts := make([]string, len(res.Expected))
	for i, e := range res.Expected {
		parts[i] = fmt.Sprint(e.Found)
		if e.Side != "" {
			parts[i] = e.Side + " " + parts[i]
		}
	}
	return strings.Join(parts, ", ")
}

func knownCount(res check.Result) string {
	if res.Status == check.StatusFailed {
		return "-"
	}
	return fmt.Sprint(res.Known)
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func count(res check.Result) string {
	if res.Status == check.StatusFailed {
		return "-"
	}
	return fmt.Sprint(res.Violations)
}

func totals(ts []check.Money) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.String()
	}
	return strings.Join(parts, ", ")
}

func cells(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = cell(v)
	}
	return out
}

// cell keeps a value on one line of a Markdown table.
func cell(v string) string {
	v = strings.ReplaceAll(v, "|", `\|`)
	return strings.Join(strings.Fields(v), " ")
}

type summary struct {
	Checks     int `json:"checks"`
	Violations int `json:"violations"`
	Failed     int `json:"failed"`
	Passed     int `json:"passed"`
}

func summarize(r check.Report) summary {
	s := summary{Checks: len(r.Results)}
	for _, res := range r.Results {
		switch res.Status {
		case check.StatusViolations:
			s.Violations++
		case check.StatusFailed:
			s.Failed++
		default:
			s.Passed++
		}
	}
	return s
}

type jsonReport struct {
	Version   int         `json:"version"`
	StartedAt time.Time   `json:"started_at"`
	Summary   summary     `json:"summary"`
	Warnings  []string    `json:"warnings"`
	Checks    []jsonCheck `json:"checks"`
}

type jsonCheck struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	// RightSource is set for a compare check; Source is its left side, and the
	// amounts are differences, right minus left.
	RightSource string       `json:"right_source,omitempty"`
	Severity    string       `json:"severity"`
	Status      string       `json:"status"`
	Violations  int64        `json:"violations"`
	Totals      []jsonMoney  `json:"totals"`
	Samples     []jsonSample `json:"samples"`
	Error       string       `json:"error,omitempty"`
	DurationMS  int64        `json:"duration_ms"`
	// Scope is "window" when the check read the rows since Since only.
	Scope  string     `json:"scope"`
	Since  *time.Time `json:"since,omitempty"`
	Window string     `json:"window,omitempty"`
	// Until is the end of the rows a compare check with settle read.
	Until  *time.Time `json:"until,omitempty"`
	Settle string     `json:"settle,omitempty"`

	Known        int64          `json:"known"`
	KnownTotals  []jsonMoney    `json:"known_totals"`
	KnownSamples []jsonSample   `json:"known_samples"`
	Resolved     []jsonResolved `json:"resolved"`
	// Expect is what the expect queries found, one per side of a compare check.
	Expect []jsonExpected `json:"expect"`
}

type jsonExpected struct {
	Side  string `json:"side,omitempty"`
	Found int64  `json:"found"`
	Min   int64  `json:"min"`
}

type jsonResolved struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// jsonMoney carries the amount twice: minor units for arithmetic and a decimal
// string for people. Never a JSON number with a fraction.
type jsonMoney struct {
	Currency string `json:"currency"`
	Minor    int64  `json:"minor"`
	Amount   string `json:"amount"`
}

type jsonSample struct {
	ID     string            `json:"id"`
	Amount *jsonMoney        `json:"amount,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	Reason string            `json:"reason,omitempty"`
	Note   string            `json:"note,omitempty"`
}

func toJSONMoney(m check.Money) jsonMoney {
	return jsonMoney{Currency: m.Currency, Minor: m.Minor, Amount: money.FormatMinor(m.Minor, m.Exponent)}
}

// JSON writes the report with version 1 of its format. Lists are never null.
func JSON(w io.Writer, r check.Report) error {
	out := jsonReport{Version: 1, StartedAt: r.StartedAt, Summary: summarize(r), Warnings: r.Warnings, Checks: []jsonCheck{}}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, res := range r.Results {
		c := jsonCheck{
			Name: res.Name, Description: strings.TrimSpace(res.Description), Source: res.Source, RightSource: res.RightSource,
			Severity: res.Severity, Status: res.Status, Violations: res.Violations,
			Error: res.Error, DurationMS: res.Duration.Milliseconds(), Scope: string(res.Scope),
			Totals: []jsonMoney{}, Samples: []jsonSample{},
			Known: res.Known, KnownTotals: []jsonMoney{}, KnownSamples: []jsonSample{}, Resolved: []jsonResolved{},
			Expect: []jsonExpected{},
		}
		for _, e := range res.Expected {
			c.Expect = append(c.Expect, jsonExpected(e))
		}
		if res.Scope == check.Window {
			since := res.Since
			c.Since, c.Window = &since, check.Span(res.Window)
		}
		if res.Settle > 0 {
			until := res.Until
			c.Until, c.Settle = &until, check.Span(res.Settle)
		}
		for _, t := range res.Totals {
			c.Totals = append(c.Totals, toJSONMoney(t))
		}
		for _, t := range res.KnownTotals {
			c.KnownTotals = append(c.KnownTotals, toJSONMoney(t))
		}
		for _, sm := range res.Samples {
			c.Samples = append(c.Samples, toJSONSample(sm))
		}
		for _, sm := range res.KnownSamples {
			c.KnownSamples = append(c.KnownSamples, toJSONSample(sm))
		}
		for _, k := range res.Resolved {
			c.Resolved = append(c.Resolved, jsonResolved(k))
		}
		out.Checks = append(out.Checks, c)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func toJSONSample(sm check.Sample) jsonSample {
	js := jsonSample{ID: sm.ID, Reason: sm.Reason, Note: sm.Note}
	if sm.Amount != nil {
		m := toJSONMoney(*sm.Amount)
		js.Amount = &m
	}
	if len(sm.Fields) > 0 {
		js.Fields = map[string]string{}
		for _, f := range sm.Fields {
			js.Fields[f.Name] = f.Value
		}
	}
	return js
}
