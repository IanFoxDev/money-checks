package report

import (
	"bytes"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/source"
)

func explained() check.ExplainReport {
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	return check.ExplainReport{
		StartedAt: at,
		Warnings:  []string{"source billing: postgres: the user can write to charges; running anyway because require_read_only is false"},
		Results: []check.Explained{
			{Check: "rebills", Query: "expect", Source: "billing", Scope: check.Window, Since: at.Add(-24 * time.Hour),
				Plan: source.Plan{Raw: `[{"Plan": {}}]`, Indexes: []string{"charges_created_at_idx on charges"}, Rows: 61}},
			{Check: "rebills", Source: "billing", Scope: check.Window, Since: at.Add(-24 * time.Hour),
				Plan: source.Plan{Raw: `[{"Plan": {}}]`, Indexes: []string{"charges_created_at_idx on charges"}, Rows: 61}},
			{Check: "stuck", Source: "billing", Scope: check.Full,
				Plan: source.Plan{Raw: `[{"Plan": {}}]`, Scans: []string{"payments"}, Rows: 120000, Notes: []string{"payments has no statistics: the estimates are a guess; run ANALYZE payments"}}},
			{Check: "daily", Query: "left", Source: "shop", Scope: check.Window, Since: at.Add(-72 * time.Hour), Until: at.Add(-2 * time.Hour),
				Plan: source.Plan{Raw: `{"queryPlanner": {}}`, Indexes: []string{"day_1 on totals"}, Rows: -1}},
			{Check: "daily", Query: "right", Source: "stats", Scope: check.Window, Since: at.Add(-72 * time.Hour), Until: at.Add(-2 * time.Hour),
				Error: "source stats: connection refused"},
		},
	}
}

func TestExplainText(t *testing.T) {
	var b bytes.Buffer
	if err := ExplainText(&b, explained(), 100000); err != nil {
		t.Fatal(err)
	}
	golden(t, "explain.txt", b.Bytes())
}

func TestExplainTextAnalyze(t *testing.T) {
	r := explained()
	r.Analyze, r.Warnings, r.Results = true, nil, r.Results[2:3]
	r.Results[0].Plan.Analyzed, r.Results[0].Plan.Stopped = true, true
	var b bytes.Buffer
	if err := ExplainText(&b, r, 0); err != nil {
		t.Fatal(err)
	}
	golden(t, "explain_analyze.txt", b.Bytes())
}

func TestExplainJSON(t *testing.T) {
	var b bytes.Buffer
	if err := ExplainJSON(&b, explained()); err != nil {
		t.Fatal(err)
	}
	golden(t, "explain.json", b.Bytes())
}
