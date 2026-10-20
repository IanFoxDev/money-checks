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
  stats:
    clickhouse:
      url_env: STATS_CLICKHOUSE_URL
      database: stats
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
  - name: shares_match_pool
    source: stats
    query: select period, pool - paid as amount from payouts where pool != paid
    id: period
    amount: {field: amount, unit: major, currency: EUR}
serve:
  slack: {webhook_env: SLACK_WEBHOOK_URL}
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(valid), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Checks) != 4 {
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
	if !*c.Sources["billing"].MongoDB.RequireReadOnly || !*c.Sources["stats"].ClickHouse.RequireReadOnly {
		t.Error("require_read_only should default to true")
	}
	if k := c.Sources["stats"].Kind(); k != "clickhouse" {
		t.Errorf("kind %q", k)
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
  three:
    mongodb: {uri_env: A, database: x}
    postgres: {dsn_env: B}
    clickhouse: {url_env: C, database: y}
  none: {}
  ch:
    clickhouse: {}
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
  - name: ch_pipeline
    source: ch
    collection: c
    pipeline: [{$match: {a: 1}}]
serve: {interval: 10s, slack: {}}
`
	_, err := Parse([]byte(bad), "testdata")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"version: want 1",
		"defaults.samples",
		"sources.both: set one of mongodb, postgres and clickhouse, not several",
		"sources.three: set one of mongodb, postgres and clickhouse, not several",
		"sources.none: set mongodb, postgres or clickhouse",
		"sources.ch.clickhouse.url_env",
		"sources.ch.clickhouse.database: required",
		"check ch_pipeline: collection and pipeline are for mongodb sources; ch is clickhouse, use query",
		"sources.nouri.mongodb.uri_env",
		"sources.pg.postgres.dsn_env",
		"name must match",
		"check Bad-Name: collection: required",
		"$merge writes to the database",
		"$out writes to the database",
		"stage 1 (line",
		"query is for postgres and clickhouse sources",
		"check dup: the name is used twice",
		"set query or query_file",
		"id: the column",
		"amount.unit: want minor or major",
		"amount.currency: unknown \"ZZZ\"",
		"mask: \"email\" is not in show",
		"source: unknown \"missing\", known: both, ch, none, nouri, pg, three",
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

func TestParseWindow(t *testing.T) {
	ok := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  ledger: {postgres: {dsn_env: B}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - name: rebills
    source: billing
    collection: payments
    window: 48h
    pipeline:
      - $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
  - name: refunds
    source: ledger
    query: select id from refunds where created_at >= $1
    id: id
    window: 24h
  - name: daily
    source: stats
    query: "select day as id from daily where day >= { since : DateTime64(6, 'UTC') }"
    id: id
    window: 24h
serve: {interval: 1h}
`
	c, err := Parse([]byte(ok), ".")
	if err != nil {
		t.Fatal(err)
	}
	if c.Checks[0].Window != 48*time.Hour || c.Serve.FullEvery != 24*time.Hour {
		t.Errorf("window %s, full_every %s", c.Checks[0].Window, c.Serve.FullEvery)
	}

	bad := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  ledger: {postgres: {dsn_env: B}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - {name: ch_no_since, source: stats, query: select 1 as id, id: id, window: 24h}
  - {name: ch_no_window, source: stats, query: "select 1 as id where now() > {since:DateTime64(6, 'UTC')}", id: id}
  - {name: ch_zone, source: stats, query: "select 1 as id where now() > {since:DateTime64(6)}", id: id, window: 24h}
  - {name: ch_seconds, source: stats, query: "select 1 as id where now() > { since : DateTime('UTC') }", id: id, window: 24h}
  - {name: no_since, source: billing, collection: c, window: 24h, pipeline: [{$match: {a: 1}}]}
  - {name: no_window, source: billing, collection: c, pipeline: [{$match: {$expr: {$gte: ["$at", "$$since"]}}}]}
  - {name: no_param, source: ledger, query: select 1, id: id, window: 24h}
  - {name: short, source: ledger, query: select $1, id: id, window: 30m}
  - {name: negative, source: ledger, query: select $1, id: id, window: -1h}
  - {name: odd_every, source: ledger, query: select 1, id: id, every: 90m}
  - {name: short_every, source: ledger, query: select 1, id: id, every: 30m}
  - {name: window_every, source: ledger, query: select $1, id: id, window: 2h, every: 6h}
serve: {interval: 1h, full_every: 30m}
`
	_, err = Parse([]byte(bad), ".")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"check no_since: window: the pipeline must compare a time field with $$since",
		"check no_window: pipeline: $$since is the start of the window; set window",
		"check no_param: window: the query must compare a time column with $1",
		"check ch_no_since: window: the query must compare a time column with {since:DateTime64(6, 'UTC')}",
		"check ch_no_window: query: {since:...} is the start of the window; set window",
		"check ch_zone: query: write {since:DateTime64(6, 'UTC')}, not {since:DateTime64(6)}",
		"check ch_seconds: query: write {since:DateTime64(6, 'UTC')}, not {since:DateTime('UTC')}",
		"check short: window: at least serve.interval (1h0m0s), got 30m0s",
		"check negative: window: must be positive",
		"check odd_every: every: a multiple of serve.interval (1h0m0s), got 1h30m0s",
		"check short_every: every: a multiple of serve.interval (1h0m0s), got 30m0s",
		"check window_every: window: at least every (6h0m0s), got 2h0m0s",
		"serve.full_every: at least serve.interval (1h0m0s), got 30m0s",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestParseCompare(t *testing.T) {
	ok := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - name: purchases_reach_stats
    window: 72h
    compare:
      left:
        source: billing
        collection: purchases
        pipeline:
          - $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
      right:
        source: stats
        query: "select toString(day) as day, currency, sum(amount) as amount, count() as n from daily where day >= toDate({since:DateTime64(6, 'UTC')}) group by day, currency"
      key: [day, currency]
      amount: {field: amount, unit: major, currency_field: currency}
      counts: [n]
      tolerance_minor: 1
`
	c, err := Parse([]byte(ok), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	ch := c.Checks[0]
	if ch.Compare.MaxRows != DefaultMaxRows || !ch.HasAmount() || ch.ID != "" {
		t.Errorf("max_rows %d, amount %v, id %q", ch.Compare.MaxRows, ch.HasAmount(), ch.ID)
	}
	left, right := ch.Side("left"), ch.Side("right")
	if left.Source != "billing" || left.Pipeline.Kind == 0 || left.Window != 72*time.Hour || left.Timeout != DefaultTimeout {
		t.Errorf("left: %+v", left)
	}
	if right.Source != "stats" || !strings.Contains(right.Query, "group by") || right.Name != "purchases_reach_stats right" {
		t.Errorf("right: %+v", right)
	}

	bad := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - name: on_the_check
    source: billing
    id: day
    show: [n]
    compare:
      left: {source: billing, collection: p, pipeline: [{$match: {a: 1}}]}
      right: {source: stats, query: select 1}
      key: [day]
  - name: sides
    window: 24h
    compare:
      left: {source: nowhere, query: select 1}
      right: {source: stats, collection: p}
      key: [day, day]
  - name: currency_not_in_key
    compare:
      left: {source: stats, query: select 1}
      right: {source: stats, query: select 2}
      key: [day]
      amount: {field: amount, unit: cents, currency_field: currency}
      counts: [day, amount, n, n]
      tolerance_minor: -1
      max_rows: -5
  - name: no_key
    compare:
      left: {source: stats, query: select 1}
      right: {source: stats, query: select 2}
`
	_, err = Parse([]byte(bad), ".")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"check on_the_check: source: a compare check sets it under compare or on each side",
		"check on_the_check: id: a compare check",
		"check on_the_check: show: a compare check",
		`check sides: compare.left: source: unknown "nowhere"`,
		"check sides: compare.right: collection and pipeline are for mongodb sources",
		"check sides: compare.right: set query or query_file",
		"check sides: compare.right: window: the query must compare a time column with {since:DateTime64(6, 'UTC')}",
		`check sides: compare.key: "day" is listed twice`,
		`check currency_not_in_key: compare.amount.unit: want minor or major, got "cents"`,
		`check currency_not_in_key: compare.amount.currency_field: "currency" must be in key`,
		`check currency_not_in_key: compare.counts: "day" is in key`,
		`check currency_not_in_key: compare.counts: "amount" is the amount`,
		`check currency_not_in_key: compare.counts: "n" is listed twice`,
		"check currency_not_in_key: compare.tolerance_minor: must be 0 or more",
		"check currency_not_in_key: compare.max_rows: must be positive",
		"check no_key: compare.key: the columns both sides group by",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "id: the column") {
		t.Errorf("a side needs no id:\n%v", err)
	}

	files := strings.Replace(ok, "        pipeline:\n", "        pipeline_file: missing.json\n        pipeline:\n", 1)
	if _, err := Parse([]byte(files), "testdata"); err == nil || !strings.Contains(err.Error(), "check purchases_reach_stats: compare.left: set pipeline or pipeline_file, not both") {
		t.Errorf("missing side file: %v", err)
	}
}

func TestParseSettle(t *testing.T) {
	ok := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  ledger: {postgres: {dsn_env: B}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - name: three_sides
    window: 72h
    settle: 15m
    compare:
      left:
        source: billing
        collection: purchases
        pipeline:
          - $match: {$expr: {$and: [{$gte: ["$paid_at", "$$since"]}, {$lt: ["$paid_at", "$$until"]}]}}
      right: {source: stats, query: "select 1 where now() >= {since:DateTime64(6, 'UTC')} and now() < { until : DateTime64(6, 'UTC') }"}
      key: [day]
  - name: no_window
    settle: 15m
    compare:
      left: {source: ledger, query: select 1 where now() >= $1 and now() < $2}
      right: {source: stats, query: "select 1 where now() < {until:DateTime64(6, 'UTC')}"}
      key: [day]
serve: {interval: 10m}
`
	c, err := Parse([]byte(ok), ".")
	if err != nil {
		t.Fatal(err)
	}
	if c.Checks[0].Settle != 15*time.Minute || c.Checks[0].Side("right").Settle != 15*time.Minute {
		t.Errorf("settle %s", c.Checks[0].Settle)
	}

	bad := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  ledger: {postgres: {dsn_env: B}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - {name: plain, source: ledger, query: select 1, id: id, settle: 15m}
  - name: no_until
    settle: 15m
    compare:
      left: {source: billing, collection: c, pipeline: [{$match: {a: 1}}]}
      right: {source: stats, query: select 1}
      key: [day]
  - name: pg_only_two
    settle: 15m
    compare:
      left: {source: ledger, query: select 1 where now() < $2}
      right: {source: stats, query: "select 1 where now() < {until:DateTime(UTC)}"}
      key: [day]
  - name: no_settle
    compare:
      left: {source: ledger, query: select 1 where now() < $2}
      right: {source: stats, query: "select 1 where now() < {until:DateTime64(6, 'UTC')}"}
      key: [day]
  - name: mongo_no_settle
    compare:
      left: {source: billing, collection: c, pipeline: [{$match: {$expr: {$lt: ["$at", "$$until"]}}}]}
      right: {source: stats, query: select 1}
      key: [day]
  - name: too_long
    window: 1h
    settle: 1h
    compare:
      left: {source: ledger, query: select 1 where now() >= $1 and now() < $2}
      right: {source: ledger, query: select 1 where now() >= $1 and now() < $2}
      key: [day]
  - name: negative
    settle: -1m
    compare:
      left: {source: ledger, query: select 1}
      right: {source: ledger, query: select 1}
      key: [day]
serve: {interval: 10m}
`
	_, err = Parse([]byte(bad), ".")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"check plain: settle: only a compare check has it",
		"check no_until: compare.left: settle: the pipeline must compare a time field with $$until",
		"check no_until: compare.right: settle: the query must compare a time column with {until:DateTime64(6, 'UTC')}",
		"check pg_only_two: compare.left: settle: the query must compare a time column with $2, the end of the rows that have settled, and with $1",
		"check pg_only_two: compare.right: query: write {until:DateTime64(6, 'UTC')}, not {until:DateTime(UTC)}",
		"check no_settle: compare.left: query: $2 is the end of the rows that have settled; set settle",
		"check no_settle: compare.right: query: {until:...} is the end of the rows that have settled; set settle",
		"check mongo_no_settle: compare.left: pipeline: $$until is the end of the rows that have settled; set settle",
		"check too_long: settle: shorter than window (1h0m0s), got 1h0m0s",
		"check negative: settle: must be positive",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestParseExpect(t *testing.T) {
	ok := `
version: 1
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  ledger: {postgres: {dsn_env: B}}
  stats: {clickhouse: {url_env: C, database: stats}}
checks:
  - name: rebills
    source: billing
    collection: payments
    window: 24h
    pipeline:
      - $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
    expect:
      pipeline:
        - $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
        - $count: n
  - name: refunds
    source: ledger
    query: select id from refunds where created_at >= $1
    id: id
    window: 24h
    expect: {query: select count(*) from refunds, min: 0}
  - name: daily
    compare:
      left:
        source: billing
        collection: purchases
        pipeline: [{$match: {a: 1}}]
        expect: {collection: users, pipeline: [{$count: n}], min: 10}
      right: {source: stats, query: select 1}
      key: [day]
  - {name: bare, source: ledger, query: select 1 as id, id: id}
`
	c, err := Parse([]byte(ok), ".")
	if err != nil {
		t.Fatal(err)
	}
	rebills := c.Checks[0].ExpectCheck("mongodb")
	if rebills.Collection != "payments" || rebills.Window != 24*time.Hour || *c.Checks[0].Expect.Min != 1 {
		t.Errorf("rebills expect: collection %q, window %s, min %d", rebills.Collection, rebills.Window, *c.Checks[0].Expect.Min)
	}
	// The count does not use $1, so it is not given the window.
	if refunds := c.Checks[1].ExpectCheck("postgres"); refunds.Window != 0 || *c.Checks[1].Expect.Min != 0 {
		t.Errorf("refunds expect: window %s", refunds.Window)
	}
	left := c.Checks[2].Side("left")
	if ex := left.ExpectCheck("mongodb"); ex.Collection != "users" || *left.Expect.Min != 10 || ex.Name != "daily left expect" {
		t.Errorf("left expect: %+v", ex)
	}
	if got := strings.Join(c.WithoutExpect(), ","); got != "bare" {
		t.Errorf("without expect: %s", got)
	}

	bad := `
version: 1
defaults: {require_expect: true}
sources:
  billing: {mongodb: {uri_env: A, database: shop}}
  ledger: {postgres: {dsn_env: B}}
checks:
  - {name: missing, source: ledger, query: select 1 as id, id: id}
  - {name: negative, source: ledger, query: select 1 as id, id: id, expect: {query: select 1, min: -1}}
  - {name: writes, source: billing, collection: c, pipeline: [{$match: {a: 1}}], expect: {pipeline: [{$out: x}]}}
  - {name: no_window, source: billing, collection: c, pipeline: [{$match: {a: 1}}], expect: {pipeline: [{$match: {$expr: {$gte: ["$at", "$$since"]}}}]}}
  - {name: pg_no_window, source: ledger, query: select 1 as id, id: id, expect: {query: select count(*) from t where at >= $1}}
  - {name: wrong_kind, source: ledger, query: select 1 as id, id: id, expect: {pipeline: [{$count: n}]}}
  - name: on_check
    expect: {query: select 1}
    compare:
      left: {source: ledger, query: select 1}
      right: {source: ledger, query: select 1}
      key: [day]
  - name: pg_settle_only
    settle: 15m
    compare:
      left: {source: ledger, query: select 1 where now() >= $1 and now() < $2, expect: {query: select count(*) from t where at >= $1}}
      right: {source: ledger, query: select 1 where now() >= $1 and now() < $2, expect: {query: select count(*) from t where at < $2}}
      key: [day]
`
	_, err = Parse([]byte(bad), ".")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		"check missing: expect: required by defaults.require_expect",
		"check negative: expect.min: must be 0 or more",
		"check writes: expect: pipeline: line 10: $out writes to the database",
		"check no_window: expect: pipeline: $$since is the start of the window; set window",
		"check pg_no_window: expect: query: $1 is the start of the window; the check has no window",
		"check wrong_kind: expect: collection and pipeline are for mongodb sources",
		"check on_check: expect: a compare check sets it on each side",
		"check on_check: expect: defaults.require_expect is set",
		"check pg_settle_only: compare.left.expect: query: $1 without a window is given only with $2",
		"check pg_settle_only: compare.right.expect: settle: the query must compare a time column with $2, the end of the rows that have settled, and with $1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestParseExpectFiles(t *testing.T) {
	c, err := Parse([]byte(`
version: 1
sources:
  ledger: {postgres: {dsn_env: B}}
checks:
  - {name: refunds, source: ledger, query: select 1 as id, id: id, expect: {query_file: refund_twice.sql}}
`), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Checks[0].Expect.Query, "refunds") {
		t.Errorf("expect query %q", c.Checks[0].Expect.Query)
	}
}

// $$since bound by let, vars or as is that variable, not the start of the window.
func TestOwnSinceVariable(t *testing.T) {
	ok := `
version: 1
sources: {billing: {mongodb: {uri_env: A, database: shop}}}
checks:
  - name: lookup_let
    source: billing
    collection: payments
    pipeline:
      - $lookup:
          from: refunds
          let: {since: "$paid_at"}
          pipeline: [{$match: {$expr: {$gte: ["$created_at", "$$since"]}}}]
          as: later
  - name: map_as
    source: billing
    collection: payments
    pipeline:
      - $set: {d: {$map: {input: "$dates", as: since, in: {$year: "$$since"}}}}
`
	if _, err := Parse([]byte(ok), "."); err != nil {
		t.Fatal(err)
	}
	// The value bound is outside the variable: $$since there is the parameter.
	bad := strings.Replace(ok, `let: {since: "$paid_at"}`, `let: {since: "$$since"}`, 1)
	if _, err := Parse([]byte(bad), "."); err == nil || !strings.Contains(err.Error(), "check lookup_let: pipeline: $$since is the start of the window; set window") {
		t.Errorf("got %v", err)
	}
}
