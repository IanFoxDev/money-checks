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
	return check.Report{
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

func TestJSONEmptyListsAreNotNull(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, check.Report{Results: []check.Result{{Name: "c", Status: check.StatusOK}}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"warnings": []`, `"totals": []`, `"samples": []`} {
		if !bytes.Contains(b.Bytes(), []byte(want)) {
			t.Errorf("missing %s in %s", want, b.String())
		}
	}
}
