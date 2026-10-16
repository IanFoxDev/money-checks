package report

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func sample() check.Report {
	usd := func(minor int64) *check.Money { return &check.Money{Currency: "USD", Minor: minor, Exponent: 2} }
	r := check.Report{
		StartedAt: time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC),
		Warnings:  []string{"source billing: mongodb: the user can write to shop (insert); running anyway because require_read_only is false"},
		Results: []check.Result{
			{
				Name: "two_rebills_in_one_period", Description: "A subscription was charged twice for the same period.\n",
				Source: "billing", Severity: "error", Status: check.StatusViolations, Violations: 3,
				Totals: []check.Money{{Currency: "EUR", Minor: 500, Exponent: 2}, {Currency: "USD", Minor: 3709, Exponent: 2}},
				Samples: []check.Sample{
					{ID: `{"subscription":"s1","period":"2026-09"}`, Amount: usd(2699), Fields: []check.Field{{Name: "email", Value: "a***@example.com"}, {Name: "note", Value: "a|b\nc"}}},
					{ID: `{"subscription":"s2","period":"2026-09"}`, Amount: usd(1010), Fields: []check.Field{{Name: "email", Value: "b***@example.com"}, {Name: "note", Value: ""}}},
				},
				Duration: 120 * time.Millisecond,
			},
			{Name: "balance_matches_journal", Source: "billing", Severity: "error", Status: check.StatusOK, Duration: 40 * time.Millisecond},
			{Name: "paid_without_access", Source: "billing", Severity: "warn", Status: check.StatusViolations, Violations: 1, Samples: []check.Sample{{ID: "p9"}}},
			{Name: "refund_twice", Source: "ledger", Severity: "error", Status: check.StatusFailed, Error: "source ledger: environment variable LEDGER_DSN is empty"},
		},
	}
	for i := range r.Results {
		r.Results[i].Scope = check.Full
	}
	return r
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs; run go test ./internal/report -update and review the diff\n--- got\n%s", name, got)
	}
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, sample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report.md", b.Bytes())
}

func TestJSON(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report.json", b.Bytes())
}

// withKnown is sample() with a known file: the first check has known violations,
// one of them stale, and an entry that is gone; the second finds only known ones.
func withKnown() check.Report {
	r := sample()
	usd := func(minor int64) *check.Money { return &check.Money{Currency: "USD", Minor: minor, Exponent: 2} }
	reb := &r.Results[0]
	reb.Samples[1].Note = "known at 5.00 USD (partial refund), the amount changed"
	reb.Known = 2
	reb.KnownTotals = []check.Money{{Currency: "USD", Minor: 3098, Exponent: 2}}
	reb.KnownSamples = []check.Sample{
		{ID: `{"subscription":"s7","period":"2026-08"}`, Amount: usd(2699), Fields: []check.Field{{Name: "email", Value: "c***@example.com"}, {Name: "note", Value: ""}}, Reason: "refunded by hand, FIN-12"},
	}
	reb.Resolved = []check.Resolved{{ID: `{"subscription":"s0","period":"2026-07"}`, Reason: "fixed | in the data"}}
	r.Results[1].Known = 1
	r.Results[1].KnownSamples = []check.Sample{{ID: "u1", Reason: "test account"}}
	return r
}

func TestMarkdownKnown(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, withKnown()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report_known.md", b.Bytes())
}

func TestJSONKnown(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, withKnown()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report_known.json", b.Bytes())
}

// withWindow is sample() from a window run: the first check has a window, the
// others read every row.
func withWindow() check.Report {
	r := sample()
	reb := &r.Results[0]
	reb.Scope, reb.Window, reb.Since = check.Window, 24*time.Hour, r.StartedAt.Add(-24*time.Hour)
	return r
}

func TestMarkdownWindow(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, withWindow()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report_window.md", b.Bytes())
}

func TestJSONWindow(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, withWindow()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report_window.json", b.Bytes())
}

func TestJSONEmptyListsAreNotNull(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, check.Report{Results: []check.Result{{Name: "c", Status: check.StatusOK}}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"warnings": []`, `"totals": []`, `"samples": []`, `"known_totals": []`, `"known_samples": []`, `"resolved": []`} {
		if !bytes.Contains(b.Bytes(), []byte(want)) {
			t.Errorf("missing %s in %s", want, b.String())
		}
	}
}

// withCompare is a run with a compare check and a failed one.
func withCompare() check.Report {
	usd := func(minor int64) *check.Money { return &check.Money{Currency: "USD", Minor: minor, Exponent: 2} }
	fields := func(left, right, problem string) []check.Field {
		return []check.Field{{Name: "left", Value: left}, {Name: "right", Value: right}, {Name: "problem", Value: problem}}
	}
	return check.Report{
		StartedAt: time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC),
		Results: []check.Result{
			{
				Name: "purchases_reach_stats", Description: "Every purchase reaches the daily statistics.",
				Source: "billing", RightSource: "stats", Severity: "error", Status: check.StatusViolations, Violations: 2, Scope: check.Full,
				Totals: []check.Money{{Currency: "USD", Minor: 3200, Exponent: 2}},
				Samples: []check.Sample{
					{ID: "2026-10-13/USD", Amount: usd(200), Fields: fields("20.00 USD", "22.00 USD", "amount differs")},
					{ID: "2026-10-14/USD", Amount: usd(-3000), Fields: fields("30.00 USD", "-", "only left")},
				},
			},
			{
				Name: "payouts_match", Source: "ledger", RightSource: "stats", Severity: "warn", Status: check.StatusFailed, Scope: check.Full,
				Error: "right: more than max_rows (100000) rows; a side returns one row per key, grouped in the query",
			},
		},
	}
}

func TestMarkdownCompare(t *testing.T) {
	var b bytes.Buffer
	if err := Markdown(&b, withCompare()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report_compare.md", b.Bytes())
}

func TestJSONCompare(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, withCompare()); err != nil {
		t.Fatal(err)
	}
	golden(t, "report_compare.json", b.Bytes())
}
