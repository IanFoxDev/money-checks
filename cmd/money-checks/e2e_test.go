package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const example = "../../examples/subscriptions"

// The subscription shop from examples/: money-checks must find exactly the planted
// violations, as read-only users of the three databases. Needs all three test
// databases.
func TestEndToEndExample(t *testing.T) {
	pgDSN, mongoURI, chURL := os.Getenv("MONEY_CHECKS_TEST_PG_DSN"), os.Getenv("MONEY_CHECKS_TEST_MONGO_URI"), os.Getenv("MONEY_CHECKS_TEST_CLICKHOUSE_URL")
	if pgDSN == "" || mongoURI == "" || chURL == "" {
		skipWithoutDB(t, "MONEY_CHECKS_TEST_PG_DSN, MONEY_CHECKS_TEST_MONGO_URI and MONEY_CHECKS_TEST_CLICKHOUSE_URL are needed; make postgres-up mongo-up clickhouse-up")
	}
	name := fmt.Sprintf("mc_e2e_%d", time.Now().UnixNano())
	t.Setenv("SHOP_MONGO_URI", seedMongo(t, mongoURI, name))
	t.Setenv("LEDGER_PG_DSN", seedPostgres(t, pgDSN, name))
	t.Setenv("STATS_CLICKHOUSE_URL", seedClickHouse(t, chURL, name))

	conf, err := os.ReadFile(filepath.Join(example, "checks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "checks.yaml")
	conf = []byte(strings.Replace(string(conf), "database: shop", "database: "+name, 1))
	conf = []byte(strings.Replace(string(conf), "database: stats", "database: "+name, 1))
	if err := os.WriteFile(path, conf, 0o600); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(dir, "report.json")
	code, out, errOut := runCLI(t, "run", "-c", path, "--json", jsonPath)
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s\n%s", code, errOut, out)
	}
	if strings.Contains(out, "anna@example.com") || !strings.Contains(out, "a***@example.com") {
		t.Error("emails are not masked in the report")
	}

	var report struct {
		Checks []struct {
			Name   string
			Status string
			Expect []struct{ Found int64 }
		}
	}
	b, err := os.ReadFile(jsonPath)
	if err != nil || json.Unmarshal(b, &report) != nil {
		t.Fatalf("report: %v\n%s", err, b)
	}
	for _, c := range report.Checks {
		if len(c.Expect) == 0 || c.Expect[0].Found == 0 {
			t.Errorf("check %s: expect %+v", c.Name, c.Expect)
		}
	}

	// The planted violations, by id and amount, as CI checks them.
	code, out, errOut = runCLI(t, "test", "-c", path, "-t", filepath.Join(example, "tests.yaml"))
	if code != exitOK || !strings.Contains(out, "9 tests: 9 passed") {
		t.Errorf("test: exit %d\n%s\n%s", code, out, errOut)
	}
}

// seedMongo loads the example's Extended JSON files into database name and returns
// the URI of a user with only the read role on it.
func seedMongo(t *testing.T, adminURI, name string) string {
	t.Helper()
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(adminURI))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database(name)
	t.Cleanup(func() {
		_ = db.RunCommand(ctx, bson.D{{Key: "dropUser", Value: "checks_" + name}}).Err()
		_ = db.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	files, err := filepath.Glob(filepath.Join(example, "seed", "mongo", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("seed files: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var wrapped bson.D
		if err := bson.UnmarshalExtJSON([]byte(`{"docs":`+string(data)+`}`), false, &wrapped); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		docs, _ := wrapped[0].Value.(bson.A)
		if _, err := db.Collection(strings.TrimSuffix(filepath.Base(f), ".json")).InsertMany(ctx, docs); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	user := "checks_" + name
	if err := db.RunCommand(ctx, bson.D{
		{Key: "createUser", Value: user}, {Key: "pwd", Value: "checks"},
		{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: name}}}},
	}).Err(); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(adminURI)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, "checks")
	q := u.Query()
	q.Set("authSource", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// seedPostgres loads the example ledger into schema name and returns a DSN of a
// role that can only read it.
func seedPostgres(t *testing.T, adminDSN, name string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := os.ReadFile(filepath.Join(example, "postgres", "ledger.sql"))
	if err != nil {
		t.Fatal(err)
	}
	role := "checks_" + name
	for _, q := range []string{
		"create schema " + name,
		"set search_path = " + name,
		string(ledger),
		"create role " + role + " login password 'checks'",
		"grant usage on schema " + name + " to " + role,
		"grant select on all tables in schema " + name + " to " + role,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, "drop schema "+name+" cascade")
		_, _ = conn.Exec(ctx, "drop role "+role)
		_ = conn.Close(ctx)
	})
	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "checks")
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// seedClickHouse loads the example statistics into database name and returns the
// URL of a user with SELECT on it only.
func seedClickHouse(t *testing.T, adminURL, name string) string {
	t.Helper()
	exec := func(database, q string) {
		t.Helper()
		u, err := url.Parse(adminURL)
		if err != nil {
			t.Fatal(err)
		}
		if database != "" {
			v := u.Query()
			v.Set("database", database)
			u.RawQuery = v.Encode()
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), strings.NewReader(q))
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
	stats, err := os.ReadFile(filepath.Join(example, "clickhouse", "stats.sql"))
	if err != nil {
		t.Fatal(err)
	}
	user := "checks_" + name
	exec("", "create database "+name)
	t.Cleanup(func() {
		exec("", "drop user if exists "+user)
		exec("", "drop database "+name)
	})
	// The HTTP interface runs one statement at a time.
	for _, q := range strings.Split(string(stats), ";") {
		if strings.TrimSpace(stripComments(q)) != "" {
			exec(name, q)
		}
	}
	exec("", "create user "+user+" identified by 'checks'")
	exec("", "grant select on "+name+".* to "+user)
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, "checks")
	return u.String()
}

// stripComments drops the -- comment lines of an SQL file.
func stripComments(q string) string {
	var lines []string
	for _, line := range strings.Split(q, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
