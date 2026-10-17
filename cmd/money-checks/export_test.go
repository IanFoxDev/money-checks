package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ianfoxdev/money-checks/internal/source"
)

const exportConf = `
version: 1
known_file: known.yaml
sources: {ledger: {postgres: {dsn_env: MC_EXPORT_UNUSED}}}
checks:
  - name: refund_twice
    source: ledger
    query: q
    id: id
    amount: {field: amount, unit: major, currency_field: currency}
    show: [currency]
    samples: 1
  - {name: other, source: ledger, query: q, id: id}
`

func exportCLI(t *testing.T, src rows, args ...string) (dir string, code int, stdout, stderr string) {
	t.Helper()
	dir = t.TempDir()
	for name, body := range map[string]string{
		"checks.yaml": exportConf,
		"known.yaml":  "version: 1\nknown:\n  - {check: refund_twice, id: p2, reason: refunded by hand}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	code = run(context.Background(), append([]string{"run", "-c", filepath.Join(dir, "checks.yaml")}, args...), &out, &errOut, opener(src), fixed)
	return dir, code, out.String(), errOut.String()
}

func TestRunExportsEveryRow(t *testing.T) {
	src := rows{rows: refunds("p1", "10.99", "USD", "p2", "5", "EUR", "p3", "1", "USD")}
	dir := t.TempDir()
	csvPath, jsonlPath := filepath.Join(dir, "all.csv"), filepath.Join(dir, "all.jsonl")
	_, code, out, errOut := exportCLI(t, src, "--check", "refund_twice", "--csv", csvPath, "--jsonl", jsonlPath)
	if code != exitFound {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	// The report has one sample and only the one check.
	if !strings.Contains(out, "Showing 1 of 2.") || strings.Contains(out, "| other |") {
		t.Errorf("report:\n%s", out)
	}
	b, err := os.ReadFile(csvPath)
	want := "id,amount,currency,currency,known,known_note\n" +
		"p1,10.99,USD,USD,,\n" +
		"p2,5.00,EUR,EUR,refunded by hand,\n" +
		"p3,1.00,USD,USD,,\n"
	if err != nil || string(b) != want {
		t.Errorf("csv %v\n%s", err, b)
	}
	b, err = os.ReadFile(jsonlPath)
	if err != nil || strings.Count(string(b), "\n") != 3 || !strings.Contains(string(b), `"reason":"refunded by hand"`) {
		t.Errorf("jsonl %v\n%s", err, b)
	}
	for _, p := range []string{csvPath, jsonlPath} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", p, fi.Mode(), err)
		}
	}
}

// A check that fails half way leaves no file and no temporary file.
func TestRunExportFailedCheck(t *testing.T) {
	src := rows{rows: refunds("p1", "10.99", "USD"), err: errors.New("connection reset")}
	dir := t.TempDir()
	_, code, _, errOut := exportCLI(t, src, "--check", "refund_twice", "--csv", filepath.Join(dir, "all.csv"))
	if code != exitError || !strings.Contains(errOut, "connection reset") {
		t.Errorf("exit %d, %s", code, errOut)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
}

func TestRunExportNeedsCheck(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--csv", "x.csv"}, "--csv and --jsonl need --check"},
		{[]string{"--check", "nope"}, `no check is named "nope"`},
	} {
		_, code, _, errOut := exportCLI(t, rows{}, tt.args...)
		if code != exitError || !strings.Contains(errOut, tt.want) {
			t.Errorf("%v: exit %d, %s", tt.args, code, errOut)
		}
	}
}

func refunds(v ...string) []source.Row {
	var out []source.Row
	for i := 0; i+2 < len(v); i += 3 {
		out = append(out, refund(v[i], v[i+1], v[i+2]))
	}
	return out
}
