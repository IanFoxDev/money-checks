package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// rows is a source that returns the same rows for every check.
type rows struct {
	rows []source.Row
	err  error
}

func (s rows) Query(_ context.Context, _ config.Check, _, _ time.Time, each func(source.Row) error) error {
	for _, r := range s.rows {
		if err := each(r); err != nil {
			return err
		}
	}
	return s.err
}

func (rows) Close(context.Context) error { return nil }

func opener(s rows) check.Opener {
	return func(context.Context, string, config.Source, func(string)) (source.Source, error) { return s, nil }
}

func refund(id, amount, currency string) source.Row {
	return source.Row{Columns: []string{"id", "amount", "currency"}, Values: []any{id, amount, currency}}
}

const knownConfig = `
version: 1
known_file: known.yaml
sources: {ledger: {postgres: {dsn_env: MC_UNUSED}}}
checks:
  - name: refund_twice
    source: ledger
    query: select 1
    id: id
    amount: {field: amount, unit: major, currency_field: currency}
    samples: 1
  - {name: other, source: ledger, query: select 1, id: id}
`

// knownDir writes the configuration and, unless known is empty, the known file.
func knownDir(t *testing.T, known string) (config, knownPath string) {
	t.Helper()
	config = writeConfig(t, knownConfig)
	knownPath = filepath.Join(filepath.Dir(config), "known.yaml")
	if known != "" {
		if err := os.WriteFile(knownPath, []byte(known), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return config, knownPath
}

func knownCLI(t *testing.T, src rows, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), append([]string{"known", "add"}, args...), &out, &errOut, opener(src), fixed)
	return code, out.String(), errOut.String()
}

func TestKnownAdd(t *testing.T) {
	old := "# reviewed by finance\nversion: 1\nknown:\n  - check: refund_twice\n    id: p0\n    reason: test account\n  - {check: refund_twice, id: p1, amount: 5.00 USD, reason: was smaller}"
	path, knownPath := knownDir(t, old)
	src := rows{rows: []source.Row{
		refund("p0", "10.00", "USD"),
		refund("p1", "21.98", "USD"),
		refund("p2", "1.50", "EUR"),
		refund(`{"user":"u1","period":"2026-09"}`, "3", "USD"),
		refund("42", "0.29", "USD"),
	}}
	code, out, errOut := knownCLI(t, src, "-c", path, "--check", "refund_twice", "--reason", " imported twice in 2025, FIN-7: fixed in the books ", "--until", "2026-12-31")
	if code != exitOK || out != "added 3 entries for check refund_twice to known.yaml\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "skipped p1: known at 5.00 USD (was smaller), the amount changed; edit its entry in known.yaml") {
		t.Errorf("stderr %q", errOut)
	}
	data, err := os.ReadFile(knownPath)
	if err != nil {
		t.Fatal(err)
	}
	want := old + `
  # added by money-checks known add on 2026-10-08
  - check: refund_twice
    id: p2
    amount: 1.50 EUR
    reason: 'imported twice in 2025, FIN-7: fixed in the books'
    until: "2026-12-31"
  - check: refund_twice
    id: '{"user":"u1","period":"2026-09"}'
    amount: 3.00 USD
    reason: 'imported twice in 2025, FIN-7: fixed in the books'
    until: "2026-12-31"
  - check: refund_twice
    id: "42"
    amount: 0.29 USD
    reason: 'imported twice in 2025, FIN-7: fixed in the books'
    until: "2026-12-31"
`
	if string(data) != want {
		t.Errorf("known.yaml\n%s\nwant\n%s", data, want)
	}
	if st, err := os.Stat(knownPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", st.Mode(), err)
	}

	// Run again: everything new is known now, and the report agrees.
	if code, out, _ := knownCLI(t, src, "-c", path, "--check", "refund_twice", "--reason", "r"); code != exitOK || !strings.Contains(out, "not listed already; nothing to add") {
		t.Errorf("again: exit %d, %q", code, out)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r := check.Run(context.Background(), cfg, check.Full, opener(src), fixed)
	if res := r.Results[0]; res.Violations != 1 || res.Known != 4 || res.Samples[0].ID != "p1" {
		t.Errorf("after: %+v", res)
	}
}

func TestKnownAddCreatesTheFile(t *testing.T) {
	path, knownPath := knownDir(t, "")
	code, out, errOut := knownCLI(t, rows{rows: []source.Row{source.Row{Columns: []string{"id"}, Values: []any{int64(7)}}}}, "-c", path, "--check", "other", "--reason", "test account")
	if code != exitOK || out != "added 1 entries for check other to known.yaml\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	data, _ := os.ReadFile(knownPath)
	if want := "version: 1\nknown:\n  # added by money-checks known add on 2026-10-08\n  - check: other\n    id: \"7\"\n    reason: test account\n"; string(data) != want {
		t.Errorf("known.yaml\n%s\nwant\n%s", data, want)
	}

	// Nothing to add: the file is not left behind.
	path, knownPath = knownDir(t, "")
	if code, _, _ := knownCLI(t, rows{}, "-c", path, "--check", "other", "--reason", "r"); code != exitOK {
		t.Errorf("clean: exit %d", code)
	}
	if _, err := os.Stat(knownPath); !os.IsNotExist(err) {
		t.Errorf("known.yaml left behind: %v", err)
	}
}

func TestKnownAddRefuses(t *testing.T) {
	three := rows{rows: []source.Row{refund("p1", "1", "USD"), refund("p2", "1", "USD"), refund("p3", "1", "USD")}}
	const known = "version: 1\nknown:\n  - {check: other, id: x, reason: r}\n"
	path, knownPath := knownDir(t, known)
	noKnownFile := writeConfig(t, strings.Replace(knownConfig, "known_file: known.yaml\n", "", 1))
	for _, tt := range []struct {
		name string
		src  rows
		args []string
		want string
	}{
		{"no check", three, []string{"-c", path, "--reason", "r"}, "--check is required"},
		{"no reason", three, []string{"-c", path, "--check", "refund_twice", "--reason", "  "}, "--reason is required"},
		{"bad until", three, []string{"-c", path, "--check", "refund_twice", "--reason", "r", "--until", "31.12.2026"}, `--until: want a date like 2026-12-31, got "31.12.2026"`},
		{"bad limit", three, []string{"-c", path, "--check", "refund_twice", "--reason", "r", "--limit", "0"}, "--limit must be at least 1"},
		{"no known file", three, []string{"-c", noKnownFile, "--check", "refund_twice", "--reason", "r"}, "has no known_file"},
		{"unknown check", three, []string{"-c", path, "--check", "nope", "--reason", "r"}, `no check is named "nope"`},
		{"failed", rows{rows: three.rows[:1], err: errors.New("connection reset")}, []string{"-c", path, "--check", "refund_twice", "--reason", "r"}, "check refund_twice failed: connection reset"},
		{"over the limit", three, []string{"-c", path, "--check", "refund_twice", "--reason", "r", "--limit", "2"}, "has 3 new violations, more than --limit 2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := knownCLI(t, tt.src, tt.args...)
			if code != exitError || out != "" || !strings.Contains(errOut, tt.want) {
				t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
		})
	}
	if data, _ := os.ReadFile(knownPath); string(data) != known {
		t.Errorf("known.yaml changed:\n%s", data)
	}
	if code, out, _ := knownCLI(t, rows{}, "-c", path, "--check", "refund_twice", "--reason", "r"); code != exitOK || !strings.Contains(out, "no new violations; nothing to add") {
		t.Errorf("clean: exit %d, %q", code, out)
	}
	if code, _, errOut := runCLI(t, "known", "list"); code != exitError || !strings.Contains(errOut, "usage:") {
		t.Errorf("known list: exit %d, %q", code, errOut)
	}
}

// When the known list is not the end of the file, appending would break it: the
// file is left alone and the entries are printed to add by hand.
func TestKnownAddCannotAppend(t *testing.T) {
	for _, known := range []string{
		"known:\n  - {check: other, id: x, reason: r}\nversion: 1\n",
		"version: 1\nknown: []\n",
	} {
		path, knownPath := knownDir(t, known)
		code, out, errOut := knownCLI(t, rows{rows: []source.Row{refund("p1", "1", "USD")}}, "-c", path, "--check", "refund_twice", "--reason", "r")
		if code != exitError || out != "" || !strings.Contains(errOut, "cannot append to known.yaml") || !strings.Contains(errOut, "  - check: refund_twice\n    id: p1\n") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", known, code, out, errOut)
		}
		if data, _ := os.ReadFile(knownPath); string(data) != known {
			t.Errorf("%q: known.yaml changed:\n%s", known, data)
		}
	}
}

// The known file is shared by every check of the configuration: entries of other
// checks stay valid while one check is added to.
func TestKnownAddKeepsOtherChecks(t *testing.T) {
	old := "version: 1\nknown:\n  - {check: other, id: o1, reason: test account}\n"
	path, knownPath := knownDir(t, old)
	src := rows{rows: []source.Row{refund("p2", "1.50", "EUR")}}
	code, out, errOut := knownCLI(t, src, "-c", path, "--check", "refund_twice", "--reason", "imported twice")
	if code != exitOK || out != "added 1 entries for check refund_twice to known.yaml\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Known["other"]) != 1 || len(cfg.Known["refund_twice"]) != 1 {
		data, _ := os.ReadFile(knownPath)
		t.Errorf("known %v\n%s", cfg.Known, data)
	}
}
