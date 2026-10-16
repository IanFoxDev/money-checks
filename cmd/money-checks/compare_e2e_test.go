package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Purchases in MongoDB against daily totals in ClickHouse: the key has to come
// out as the same text from both, the doubles of MongoDB and the decimals of
// ClickHouse have to meet in minor units.
func TestEndToEndCompare(t *testing.T) {
	mongoURI, chURL := os.Getenv("MONEY_CHECKS_TEST_MONGO_URI"), os.Getenv("MONEY_CHECKS_TEST_CLICKHOUSE_URL")
	if mongoURI == "" || chURL == "" {
		skipWithoutDB(t, "MONEY_CHECKS_TEST_MONGO_URI and MONEY_CHECKS_TEST_CLICKHOUSE_URL are needed; make mongo-up clickhouse-up")
	}
	name := fmt.Sprintf("mc_cmp_%d", time.Now().UnixNano())

	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database(name)
	t.Cleanup(func() {
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var docs []any
	for _, p := range []struct {
		at, currency string
		amount       float64
	}{
		{"2026-10-12T09:00:00Z", "USD", 10.99}, {"2026-10-12T23:59:59Z", "USD", 5.01},
		{"2026-10-13T00:00:00Z", "USD", 20.00}, {"2026-10-13T12:00:00Z", "EUR", 7.50},
		{"2026-10-14T08:00:00Z", "USD", 3.30},
	} {
		docs = append(docs, bson.D{{Key: "paid_at", Value: at(p.at)}, {Key: "currency", Value: p.currency}, {Key: "amount", Value: p.amount}})
	}
	if _, err := db.Collection("purchases").InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}

	ch := func(q string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, chURL, strings.NewReader(q))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: %s", q, b)
		}
	}
	ch("CREATE DATABASE " + name)
	t.Cleanup(func() { ch("DROP DATABASE " + name) })
	ch("CREATE TABLE " + name + ".daily (day Date, currency LowCardinality(String), amount Decimal(18, 2), n UInt64) ENGINE = MergeTree ORDER BY (day, currency)")
	// 2026-10-13 USD is 2.00 more with one purchase more, 2026-10-13 EUR is
	// missing, 2026-10-15 USD has no purchases behind it.
	ch("INSERT INTO " + name + ".daily VALUES ('2026-10-12', 'USD', 16.00, 2), ('2026-10-13', 'USD', 22.00, 2), " +
		"('2026-10-14', 'USD', 3.30, 1), ('2026-10-15', 'USD', 1.00, 1)")

	t.Setenv("CMP_MONGO_URI", mongoURI)
	t.Setenv("CMP_CLICKHOUSE_URL", chURL)
	conf := `
version: 1
sources:
  billing: {mongodb: {uri_env: CMP_MONGO_URI, database: ` + name + `, require_read_only: false}}
  stats: {clickhouse: {url_env: CMP_CLICKHOUSE_URL, database: ` + name + `, require_read_only: false}}
checks:
  - name: purchases_reach_stats
    compare:
      left:
        source: billing
        collection: purchases
        pipeline:
          - $group:
              _id: {day: {$dateToString: {format: "%Y-%m-%d", date: "$paid_at"}}, currency: "$currency"}
              amount: {$sum: "$amount"}
              n: {$sum: 1}
          - $project: {_id: 0, day: "$_id.day", currency: "$_id.currency", amount: {$round: ["$amount", 2]}, n: 1}
      right:
        source: stats
        query: select day, currency, amount, n from daily
      key: [day, currency]
      amount: {field: amount, unit: major, currency_field: currency}
      counts: [n]
`
	dir := t.TempDir()
	path := filepath.Join(dir, "checks.yaml")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(dir, "report.json")
	code, out, errOut := runCLI(t, "run", "-c", path, "--json", jsonPath)
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s\n%s", code, errOut, out)
	}
	var report struct {
		Checks []struct {
			Status     string
			Violations int64
			Totals     []struct{ Currency, Amount string }
			Samples    []struct {
				ID     string
				Amount struct{ Amount string }
				Fields map[string]string
			}
		}
	}
	b, err := os.ReadFile(jsonPath)
	if err != nil || json.Unmarshal(b, &report) != nil {
		t.Fatalf("report: %v\n%s", err, b)
	}
	res := report.Checks[0]
	var got []string
	for _, s := range res.Samples {
		got = append(got, s.ID+" "+s.Amount.Amount+" "+s.Fields["problem"])
	}
	for _, tot := range res.Totals {
		got = append(got, tot.Amount+" "+tot.Currency)
	}
	want := []string{
		"2026-10-13/EUR -7.50 only left",
		"2026-10-13/USD 2.00 amount differs, n differs",
		"2026-10-15/USD 1.00 only right",
		"7.50 EUR",
		"3.00 USD",
	}
	if res.Violations != 3 || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations %d:\n%s\n\nreport:\n%s", res.Violations, strings.Join(got, "\n"), out)
	}
}
