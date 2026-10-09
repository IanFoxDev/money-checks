package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const withKnown = `
version: 1
known_file: known.yaml
sources:
  ledger: {postgres: {dsn_env: X}}
checks:
  - name: refund_twice
    source: ledger
    query: select 1
    id: payment_id
    amount: {field: amount, unit: minor, currency_field: currency}
  - name: paid_without_access
    source: ledger
    query: select 1
    id: user_id
`

func parseKnown(t *testing.T, known string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "known.yaml"), []byte(known), 0o644); err != nil {
		t.Fatal(err)
	}
	return Parse([]byte(withKnown), dir)
}

func TestKnownValid(t *testing.T) {
	c, err := parseKnown(t, `
version: 1
known:
  - check: refund_twice
    id: p1
    reason: refunded by hand on 2026-09-30, ticket FIN-12
    amount: 26.99 usd
    until: 2026-12-31
  - check: paid_without_access
    id: 42
    reason: "  test account  "
`)
	if err != nil {
		t.Fatal(err)
	}
	k, ok := c.Known["refund_twice"]["p1"]
	if !ok {
		t.Fatalf("known %+v", c.Known)
	}
	if k.Amount == nil || *k.Amount != (KnownAmount{Currency: "USD", Minor: 2699}) {
		t.Errorf("amount %+v", k.Amount)
	}
	if !k.Expires.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || k.Until != "2026-12-31" {
		t.Errorf("expires %s, until %q", k.Expires, k.Until)
	}
	// An unquoted number is an id like any other: the report shows it as 42.
	k = c.Known["paid_without_access"]["42"]
	if k.Reason != "test account" || k.Amount != nil || !k.Expires.IsZero() {
		t.Errorf("42: %+v", k)
	}
}

func TestKnownNone(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(withKnown, "known_file: known.yaml\n", "", 1)), ".")
	if err != nil {
		t.Fatal(err)
	}
	if c.Known != nil || c.Known["refund_twice"]["p1"].ID != "" {
		t.Errorf("known %+v", c.Known)
	}
}

func TestKnownReportsEveryProblem(t *testing.T) {
	_, err := parseKnown(t, `
version: 2
known:
  - check: refund_twice
    id: p1
  - check: nope
    id: p2
    reason: r
  - id: p3
    reason: r
  - check: refund_twice
    reason: r
  - check: paid_without_access
    id: u1
    reason: r
    amount: 1.00 USD
  - check: refund_twice
    id: p4
    reason: r
    amount: "26.99"
  - check: refund_twice
    id: p5
    reason: r
    amount: 26.999 USD
  - check: refund_twice
    id: p6
    reason: r
    amount: 1 XXX
  - check: refund_twice
    id: p7
    reason: r
    until: 31.12.2026
  - check: refund_twice
    id: p8
    reason: r
  - check: refund_twice
    id: p8
    reason: again
`)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"known_file known.yaml: version: want 1, got 2",
		"known[0]: reason: required",
		`known[1]: check: no check is named "nope"`,
		"known[2]: check: required",
		"known[3]: id: required",
		"known[4]: amount: check paid_without_access has no amount",
		`known[5]: amount: want an amount and a currency like 26.99 USD, got "26.99"`,
		"known[6]: amount: invalid amount",
		`known[7]: amount: unknown currency "XXX"`,
		`known[8]: until: want a date like 2026-12-31, got "31.12.2026"`,
		"known[10]: check refund_twice, id p8 is listed twice",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in\n%v", want, err)
		}
	}
}

func TestKnownFileErrors(t *testing.T) {
	if _, err := Parse([]byte(withKnown), t.TempDir()); err == nil || !strings.Contains(err.Error(), "known_file: open") {
		t.Errorf("missing file: %v", err)
	}
	if _, err := parseKnown(t, "version: 1\nknown:\n  - check: refund_twice\n    ids: p1\n"); err == nil || !strings.Contains(err.Error(), "field ids not found") {
		t.Errorf("unknown key: %v", err)
	}
}
