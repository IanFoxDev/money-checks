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
	withKnown := false
	for _, res := range r.Results {
		withKnown = withKnown || res.Known > 0 || len(res.Resolved) > 0
	}
	if withKnown {
		b.WriteString("| Check | Severity | Status | Violations | Amount | Known |\n|---|---|---|---:|---:|---:|\n")
	} else {
		b.WriteString("| Check | Severity | Status | Violations | Amount |\n|---|---|---|---:|---:|\n")
	}
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |", cell(res.Name), res.Severity, res.Status, count(res), cell(totals(res.Totals)))
		if withKnown {
			fmt.Fprintf(&b, " %s |", knownCount(res))
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
			fmt.Fprintf(&b, "Source `%s`. The check failed: %s\n", res.Source, res.Error)
			continue
		}
		if res.Violations == 0 {
			fmt.Fprintf(&b, "Source `%s`, severity %s. No new violations.\n", res.Source, res.Severity)
		} else {
			fmt.Fprintf(&b, "Source `%s`, severity %s. %s", res.Source, res.Severity, plural(res.Violations, "violation"))
			if len(res.Totals) > 0 {
				fmt.Fprintf(&b, ", %s", totals(res.Totals))
			}
			b.WriteString(".\n")
			samples(&b, res.Samples, res.Violations, "known file", func(s check.Sample) string { return s.Note })
		}
		if res.Known > 0 {
			fmt.Fprintf(&b, "\n%s, not counted", plural(res.Known, "known violation"))
			if len(res.KnownTotals) > 0 {
				fmt.Fprintf(&b, ", %s", totals(res.KnownTotals))
			}
			b.WriteString(".\n")
			samples(&b, res.KnownSamples, res.Known, "accepted because", func(s check.Sample) string { return s.Reason })
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

// samples writes a table of rows: id, amount, the shown columns and, when any row
// has one, a column from the known file. Its header has a space, so it does not
// look like one of the shown columns.
func samples(b *strings.Builder, rows []check.Sample, total int64, extra string, text func(check.Sample) string) {
	if len(rows) == 0 {
		return
	}
	withExtra := false
	for _, s := range rows {
		withExtra = withExtra || text(s) != ""
	}
	header := []string{"id"}
	if rows[0].Amount != nil {
		header = append(header, "amount")
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
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Source      string       `json:"source"`
	Severity    string       `json:"severity"`
	Status      string       `json:"status"`
	Violations  int64        `json:"violations"`
	Totals      []jsonMoney  `json:"totals"`
	Samples     []jsonSample `json:"samples"`
	Error       string       `json:"error,omitempty"`
	DurationMS  int64        `json:"duration_ms"`

	Known        int64          `json:"known"`
	KnownTotals  []jsonMoney    `json:"known_totals"`
	KnownSamples []jsonSample   `json:"known_samples"`
	Resolved     []jsonResolved `json:"resolved"`
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
			Name: res.Name, Description: strings.TrimSpace(res.Description), Source: res.Source,
			Severity: res.Severity, Status: res.Status, Violations: res.Violations,
			Error: res.Error, DurationMS: res.Duration.Milliseconds(),
			Totals: []jsonMoney{}, Samples: []jsonSample{},
			Known: res.Known, KnownTotals: []jsonMoney{}, KnownSamples: []jsonSample{}, Resolved: []jsonResolved{},
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
