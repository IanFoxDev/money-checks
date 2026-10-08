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
	b.WriteString("| Check | Severity | Status | Violations | Amount |\n|---|---|---|---:|---:|\n")
	for _, res := range r.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", cell(res.Name), res.Severity, res.Status, count(res), cell(totals(res.Totals)))
	}
	for _, res := range r.Results {
		if res.Status == check.StatusOK {
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
		fmt.Fprintf(&b, "Source `%s`, severity %s. %s", res.Source, res.Severity, plural(res.Violations, "violation"))
		if len(res.Totals) > 0 {
			fmt.Fprintf(&b, ", %s", totals(res.Totals))
		}
		b.WriteString(".\n\n")
		if len(res.Samples) == 0 {
			continue
		}
		header := []string{"id"}
		if res.Samples[0].Amount != nil {
			header = append(header, "amount")
		}
		for _, f := range res.Samples[0].Fields {
			header = append(header, f.Name)
		}
		fmt.Fprintf(&b, "| %s |\n|%s\n", strings.Join(cells(header), " | "), strings.Repeat("---|", len(header)))
		for _, sm := range res.Samples {
			row := []string{sm.ID}
			if sm.Amount != nil {
				row = append(row, sm.Amount.String())
			}
			for _, f := range sm.Fields {
				row = append(row, f.Value)
			}
			fmt.Fprintf(&b, "| %s |\n", strings.Join(cells(row), " | "))
		}
		if n := int64(len(res.Samples)); n < res.Violations {
			fmt.Fprintf(&b, "\nShowing %d of %d.\n", n, res.Violations)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
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
		}
		for _, t := range res.Totals {
			c.Totals = append(c.Totals, toJSONMoney(t))
		}
		for _, sm := range res.Samples {
			js := jsonSample{ID: sm.ID}
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
			c.Samples = append(c.Samples, js)
		}
		out.Checks = append(out.Checks, c)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
