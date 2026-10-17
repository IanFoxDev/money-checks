package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTests(t *testing.T) {
	c, err := Parse([]byte(valid), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	tests, err := c.ParseTests([]byte(`
version: 1
tests:
  - check: refund_twice
    violations:
      - {id: pay_1, amount: 10.00 usd}
      - {id: 7}
  - check: two_active_subscriptions
    violations: []
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 || len(tests[0].Violations) != 2 || len(tests[1].Violations) != 0 {
		t.Fatalf("%+v", tests)
	}
	if v := tests[0].Violations[0]; v.ID != "pay_1" || *v.Amount != (KnownAmount{Currency: "USD", Minor: 1000}) {
		t.Errorf("first violation %+v", v)
	}
	if v := tests[0].Violations[1]; v.ID != "7" || v.Amount != nil {
		t.Errorf("second violation %+v", v)
	}

	_, err = c.ParseTests([]byte(`
version: 2
tests:
  - {check: nope, violations: []}
  - {violations: []}
  - check: refund_twice
  - check: two_active_subscriptions
    violations: [{id: a, amount: 1.00 USD}, {amount: 1.00 USD}, {id: b}, {id: b}]
  - {check: two_active_subscriptions, violations: []}
  - {check: two_rebills_in_one_period, violations: [{id: x, amount: 1 XXX}]}
`))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"version: want 1, got 2",
		`tests[0]: check: no check is named "nope"`,
		"tests[1]: check: required",
		"test refund_twice: violations: required; [] when the check must find nothing",
		"test two_active_subscriptions: violations[0]: amount: check two_active_subscriptions has no amount",
		"test two_active_subscriptions: violations[1]: id: required",
		"test two_active_subscriptions: violations[3]: id b is listed twice",
		"tests[4]: check two_active_subscriptions is tested twice",
		`test two_rebills_in_one_period: violations[0]: amount: unknown currency "XXX"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

// Where the checks are tested there is no known file; it is not read.
func TestLoadWithoutKnown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checks.yaml")
	conf := `
version: 1
known_file: known.yaml
sources: {ledger: {postgres: {dsn_env: B}}}
checks:
  - {name: refunds, source: ledger, query: select 1 as id, id: id}
`
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "known_file") {
		t.Errorf("Load: %v", err)
	}
	c, err := LoadWithoutKnown(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.KnownFile != "" || c.Known != nil {
		t.Errorf("known file %q, %d entries", c.KnownFile, len(c.Known))
	}
}
