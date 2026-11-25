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

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// byQuery is a source that returns the rows set for each query.
type byQuery map[string][]source.Row

func (s byQuery) Query(_ context.Context, ch config.Check, _, _ time.Time, each func(source.Row) error) error {
	for _, r := range s[ch.Query] {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

func (byQuery) Close(context.Context) error { return nil }

const diffConf = `
version: 1
known_file: known.yaml
sources: {ledger: {postgres: {dsn_env: MC_TEST_UNUSED}}}
checks:
  - name: refund_twice
    description: DESCRIPTION
    source: ledger
    query: QUERY
    id: id
    amount: {field: amount, unit: major, currency_field: currency}
  - name: same
    source: ledger
    query: same
    id: id
`

func diffCLI(t *testing.T, src byQuery, old, next string, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"old.yaml": old, "new.yaml": next} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"diff", "-c", filepath.Join(dir, "new.yaml"), "--old", filepath.Join(dir, "old.yaml")}, args...), &stdout, &stderr, func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }, fixed)
	return code, stdout.String(), stderr.String()
}

func TestDiff(t *testing.T) {
	src := byQuery{
		"old": {refund("r1", "9.99", "USD"), refund("r2", "5.00", "USD"), refund("r3", "1.00", "EUR")},
		"new": {refund("r2", "5.00", "USD"), refund("r3", "2.00", "EUR"), refund("r4", "7.00", "USD")},
	}
	old := strings.Replace(diffConf, "QUERY", "old", 1)
	code, out, errOut := diffCLI(t, src, old, strings.Replace(diffConf, "QUERY", "new", 1))
	if code != exitFound {
		t.Fatalf("code %d: %s", code, errOut)
	}
	for _, want := range []string{
		": 3 rows before, 3 rows now; 1 only old, 1 only new, 1 with another amount\n",
		"  only old (the new version hides them):\n    r1 9.99 USD\n",
		"  only new:\n    r4 7.00 USD\n",
		"  another amount:\n    r3 1.00 EUR -> 2.00 EUR\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "same") {
		t.Errorf("an unchanged check ran:\n%s", out)
	}

	// A new description is not a new version.
	code, out, _ = diffCLI(t, src, old, strings.Replace(old, "DESCRIPTION", "Refunded twice.", 1))
	if code != exitOK || out != "No check differs between the two configurations.\n" {
		t.Errorf("code %d:\n%s", code, out)
	}
	// --check runs one check even when it did not change.
	code, out, _ = diffCLI(t, src, old, old, "--check", "refund_twice", "--limit", "1")
	if code != exitOK || !strings.Contains(out, "3 rows before, 3 rows now; 0 only old, 0 only new, 0 with another amount") {
		t.Errorf("code %d:\n%s", code, out)
	}
}

func TestDiffNewAndRemoved(t *testing.T) {
	src := byQuery{"old": {refund("r1", "9.99", "USD"), refund("r2", "1.00", "USD")}}
	old := strings.Replace(diffConf, "QUERY", "old", 1)
	only := strings.SplitN(old, "  - name: same", 2)[0]
	code, out, _ := diffCLI(t, src, old, strings.Replace(old, "name: same", "name: other", 1), "--limit", "1")
	if code != exitOK {
		t.Errorf("code %d:\n%s", code, out)
	}
	for _, want := range []string{"other: new check, version ", ": 0 rows", "same: removed, version "} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	code, out, _ = diffCLI(t, src, old, strings.Replace(only, "name: refund_twice", "name: renamed", 1), "--check", "refund_twice", "--limit", "1")
	if code != exitFound || !strings.Contains(out, "refund_twice: removed, version ") || !strings.Contains(out, "found 2 rows\n  only old (the new version hides them):\n    r1 9.99 USD\n    and 1 more\n") {
		t.Errorf("code %d:\n%s", code, out)
	}
}

func TestDiffErrors(t *testing.T) {
	conf := strings.Replace(diffConf, "QUERY", "old", 1)
	for _, args := range [][]string{{"--check", "none"}} {
		if code, _, _ := diffCLI(t, byQuery{}, conf, conf, args...); code != exitError {
			t.Errorf("%v: code %d", args, code)
		}
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "old.yaml"), []byte(conf), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "new.yaml"), []byte(strings.Replace(conf, "query: old", "query: new", 1)), 0o600)
	var stdout, stderr bytes.Buffer
	down := func(context.Context, string, config.Source, func(string)) (source.Source, error) {
		return nil, errors.New("connection refused")
	}
	if code := run(context.Background(), []string{"diff", "-c", filepath.Join(dir, "new.yaml"), "--old", filepath.Join(dir, "old.yaml")}, &stdout, &stderr, down, fixed); code != exitError || !strings.Contains(stdout.String(), "refund_twice: failed: ") {
		t.Errorf("source down: code %d:\n%s", code, stdout.String())
	}
	if code, _, _ := runCLI(t, "diff", "-c", "x.yaml"); code != exitError {
		t.Errorf("without --old: code %d", code)
	}
}
