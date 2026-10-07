package config

import (
	"strings"
	"testing"
	"time"
)

const valid = `
version: 1
currencies: {USDT: 6}
defaults:
  timeout: 45s
sources:
  billing:
    mongodb:
      uri_env: BILLING_MONGO_URI
      database: shop
  ledger:
    postgres:
      dsn_env: LEDGER_DSN
checks:
  - name: two_rebills_in_one_period
    description: A subscription was charged twice for the same period.
    source: billing
    collection: payments
    pipeline:
      - $match: {kind: rebill, status: success}
      - $group:
          _id: {subscription: "$subscription_id", period: "$period"}
          n: {$sum: 1}
          amount: {$sum: "$amount_cents"}
      - $match: {n: {$gt: 1}}
    amount: {field: amount, unit: minor, currency: USD}
    show: [n]
  - name: two_active_subscriptions
    source: billing
    collection: subscriptions
    pipeline_file: two_active.json
    severity: warn
    samples: 3
  - name: refund_twice
    source: ledger
    query_file: refund_twice.sql
    id: payment_id
    amount: {field: amount, unit: minor, currency_field: currency}
    timeout: 5s
serve:
  slack: {webhook_env: SLACK_WEBHOOK_URL}
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(valid), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 3 {
		t.Fatalf("%d checks", len(c.Checks))
	}
	rebills, active, refunds := c.Checks[0], c.Checks[1], c.Checks[2]
	if rebills.ID != "_id" || rebills.Severity != "error" || *rebills.Samples != DefaultSamples || rebills.Timeout != 45*time.Second {
		t.Errorf("defaults not applied: %+v", rebills)
	}
	if len(rebills.Pipeline.Content) != 3 {
		t.Errorf("pipeline has %d stages", len(rebills.Pipeline.Content))
	}
	if len(active.Pipeline.Content) != 3 || active.Severity != "warn" || *active.Samples != 3 {
		t.Errorf("pipeline_file not read: %+v", active)
	}
	if !strings.Contains(refunds.Query, "having count(*) > 1") || refunds.Timeout != 5*time.Second {
		t.Errorf("query_file not read: %+v", refunds)
	}
	if !*c.Sources["billing"].MongoDB.RequireReadOnly {
		t.Error("require_read_only should default to true")
	}
	if c.Serve.Interval != DefaultInterval || c.Serve.Listen != DefaultListen {
		t.Errorf("serve defaults: %+v", c.Serve)
	}
	if exp, ok := c.Money.Exponent("USDT"); !ok || exp != 6 {
		t.Errorf("currency override: %d %v", exp, ok)
	}
}

func TestParseUnknownKey(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(valid, "severity: warn", "severty: warn", 1)), "testdata")
	if err == nil || !strings.Contains(err.Error(), "severty") || !strings.Contains(err.Error(), "line") {
		t.Fatalf("want an error naming the key and line, got %v", err)
	}
}

func TestParseReportsEveryProblem(t *testing.T) {
	bad := `
version: 2
defaults: {samples: 5000}
sources:
  both:
    mongodb: {uri_env: A, database: x}
    postgres: {dsn_env: B}
  nouri:
    mongodb: {database: x}
  pg:
    postgres: {}
checks:
  - name: Bad-Name
    source: nouri
    pipeline: [{$match: {a: 1}}, {$merge: {into: copy}}]
  - name: dup
    source: nouri
    collection: c
    pipeline:
      - $facet:
          x: [{$out: copy}]
      - {notastage: 1}
    query: select 1
  - name: dup
    source: pg
    amount: {field: amount, unit: cents, currency: ZZZ}
    show: [user_id]
    mask: [email]
  - name: nowhere
    source: missing
  - name: unit_less
    source: pg
    query: select 1
    id: id
    amount: {field: amount, currency: USD, currency_field: cur}
serve: {interval: 10s, slack: {}}
`
	_, err := Parse([]byte(bad), "testdata")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"version: want 1",
		"defaults.samples",
		"sources.both: set mongodb or postgres, not both",
		"sources.nouri.mongodb.uri_env",
		"sources.pg.postgres.dsn_env",
		"name must match",
		"check Bad-Name: collection: required",
		"$merge writes to the database",
		"$out writes to the database",
		"stage 1 (line",
		"query is for postgres sources",
		"check dup: the name is used twice",
		"set query or query_file",
		"id: the column",
		"amount.unit: want minor or major",
		"amount.currency: unknown \"ZZZ\"",
		"mask: \"email\" is not in show",
		"source: unknown \"missing\", known: both, nouri, pg",
		"amount: set currency or currency_field",
		"serve.interval: at least 1m",
		"serve.slack.webhook_env",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestParseFiles(t *testing.T) {
	both := strings.Replace(valid, "    query_file: refund_twice.sql", "    query_file: refund_twice.sql\n    query: select 1", 1)
	if _, err := Parse([]byte(both), "testdata"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("query and query_file: %v", err)
	}
	if _, err := Parse([]byte(valid), "nowhere"); err == nil || !strings.Contains(err.Error(), "query_file") {
		t.Errorf("missing file: %v", err)
	}
}

func TestLoadNamesTheFile(t *testing.T) {
	if _, err := Load("testdata/none.yaml"); err == nil {
		t.Fatal("no error")
	}
	_, err := Load("testdata/two_active.json")
	if err == nil || !strings.HasPrefix(err.Error(), "testdata/two_active.json: ") {
		t.Errorf("want the path in the error, got %v", err)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("MONEY_CHECKS_TEST_EMPTY", "")
	if _, err := Getenv("MONEY_CHECKS_TEST_EMPTY"); err == nil || !strings.Contains(err.Error(), "MONEY_CHECKS_TEST_EMPTY") {
		t.Errorf("want the variable named, got %v", err)
	}
	t.Setenv("MONEY_CHECKS_TEST_SET", "x")
	if v, err := Getenv("MONEY_CHECKS_TEST_SET"); err != nil || v != "x" {
		t.Errorf("Getenv = %q, %v", v, err)
	}
}
