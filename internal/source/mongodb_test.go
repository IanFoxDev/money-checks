package source

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.yaml.in/yaml/v3"

	"github.com/ianfoxdev/money-checks/internal/config"
)

func yamlNode(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func TestPipelineKeepsOrderAndExtendedJSON(t *testing.T) {
	p, err := Pipeline(yamlNode(t, `
- $match:
    created: {$gte: {$date: "2026-10-01T00:00:00Z"}}
    amount: {$type: double}
    price: {$numberDecimal: "10.99"}
    name: {$regex: "^a", $options: i}
- $group: {_id: {user: "$user_id", period: "$period"}, n: {$sum: 1}}
- $sort: {n: -1, _id: 1}
- $limit: 5
`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := bson.MarshalExtJSON(bson.D{{Key: "p", Value: p}}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"created":{"$gte":{"$date":{"$numberLong":"1790812800000"}}}`,
		`"amount":{"$type":"double"}`,
		`"price":{"$numberDecimal":"10.99"}`,
		`"name":{"$regex":"^a","$options":"i"}`, // the query operator, not a BSON regex
		`"_id":{"user":"$user_id","period":"$period"}`,
		`{"$sort":{"n":{"$numberInt":"-1"},"_id":{"$numberInt":"1"}}}`,
		`{"$limit":{"$numberInt":"5"}}`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}

// Ids from $group are matched against known files by their text, so nested values
// read like top-level ones: hex, not {"$oid": ...}.
func TestMongoValueNested(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex("652f1c9e8a1b2c3d4e5f6071")
	dec, _ := bson.ParseDecimal128("10.99")
	at := bson.NewDateTimeFromTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	for _, tt := range []struct {
		in   any
		want string
	}{
		{bson.D{{Key: "user", Value: oid}, {Key: "period", Value: "2026-09"}}, `{"user":"652f1c9e8a1b2c3d4e5f6071","period":"2026-09"}`},
		{bson.D{{Key: "z", Value: int32(1)}, {Key: "a", Value: bson.D{{Key: "at", Value: at}, {Key: "amount", Value: dec}}}}, `{"z":1,"a":{"at":"2026-09-01T00:00:00Z","amount":"10.99"}}`},
		{bson.A{oid, int64(7), nil}, `["652f1c9e8a1b2c3d4e5f6071",7,null]`},
		{bson.D{{Key: "b", Value: bson.Binary{Data: []byte{1}}}}, `{"b":{"$binary":{"base64":"AQ==","subType":"00"}}}`},
	} {
		if got := mongoValue(tt.in); got != tt.want {
			t.Errorf("%v: %#v, want %s", tt.in, got, tt.want)
		}
	}
}

func TestPipelineRefusesWrites(t *testing.T) {
	for _, p := range []string{
		`[{$match: {}}, {$out: copy}]`,
		`[{$facet: {x: [{$merge: {into: copy}}]}}]`,
	} {
		if _, err := Pipeline(yamlNode(t, p)); err == nil || !strings.Contains(err.Error(), "writes to the database") {
			t.Errorf("%s: got %v", p, err)
		}
	}
}

func TestWriteProblem(t *testing.T) {
	str := func(s string) *string { return &s }
	user := []bson.Raw{bson.Raw(mustMarshal(t, bson.D{{Key: "user", Value: "u"}}))}
	var a connectionAuth
	if p := a.writeProblem("shop"); !strings.Contains(p, "without a user") {
		t.Errorf("no user: %q", p)
	}
	a.Users = user
	a.Privileges = append(a.Privileges, a.Privileges...)
	add := func(db *string, anyResource bool, actions ...string) {
		var p = struct {
			Resource struct {
				DB          *string `bson:"db"`
				Collection  *string `bson:"collection"`
				Cluster     bool    `bson:"cluster"`
				AnyResource bool    `bson:"anyResource"`
			} `bson:"resource"`
			Actions []string `bson:"actions"`
		}{Actions: actions}
		p.Resource.DB, p.Resource.AnyResource = db, anyResource
		a.Privileges = append(a.Privileges, p)
	}
	add(str("shop"), false, "find", "listCollections")
	add(str("other"), false, "insert", "remove")
	if p := a.writeProblem("shop"); p != "" {
		t.Errorf("read on shop, write elsewhere: %q", p)
	}
	add(str(""), false, "update")
	add(nil, true, "insert")
	if p := a.writeProblem("shop"); p != "the user can write to shop (insert, update)" {
		t.Errorf("got %q", p)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := bson.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const mongoEnv = "MONEY_CHECKS_TEST_MONGO_URI"

// mongoDB seeds a database as the admin user from the environment and creates a
// user with only the read role on it. It returns the database name and URIs for
// the admin and the reader.
func mongoDB(t *testing.T) (db, adminURI, readerURI string) {
	t.Helper()
	adminURI = os.Getenv(mongoEnv)
	if adminURI == "" {
		skipWithoutDB(t, mongoEnv+" is not set; make mongo-up starts a server for it")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(adminURI))
	if err != nil {
		t.Fatal(err)
	}
	db = fmt.Sprintf("mc_%d", time.Now().UnixNano())
	d := client.Database(db)
	_, err = d.Collection("payments").InsertMany(ctx, []any{
		bson.D{{Key: "_id", Value: "p1"}, {Key: "user_id", Value: "u1"}, {Key: "period", Value: "2026-09"}, {Key: "amount", Value: 10.1}, {Key: "email", Value: "a@example.com"}},
		bson.D{{Key: "_id", Value: "p2"}, {Key: "user_id", Value: "u1"}, {Key: "period", Value: "2026-09"}, {Key: "amount", Value: 26.99}},
		bson.D{{Key: "_id", Value: "p3"}, {Key: "user_id", Value: "u2"}, {Key: "period", Value: "2026-09"}, {Key: "amount", Value: mustDecimal(t, "0.29")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := "reader_" + db
	if err := d.RunCommand(ctx, bson.D{
		{Key: "createUser", Value: reader}, {Key: "pwd", Value: "reader"},
		{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "read"}, {Key: "db", Value: db}}}},
	}).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		n, err := d.Collection("payments").CountDocuments(ctx, bson.D{})
		if err != nil || n != 3 {
			t.Errorf("payments changed: %d documents, %v", n, err)
		}
		_ = d.RunCommand(ctx, bson.D{{Key: "dropUser", Value: reader}}).Err()
		_ = d.Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	u, err := url.Parse(adminURI)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(reader, "reader")
	q := u.Query()
	q.Set("authSource", db)
	u.RawQuery = q.Encode()
	return db, adminURI, u.String()
}

func mustDecimal(t *testing.T, s string) bson.Decimal128 {
	t.Helper()
	d, err := bson.ParseDecimal128(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func openMongo(t *testing.T, db, uri string, requireReadOnly bool) (Source, []string, error) {
	t.Helper()
	t.Setenv("MC_MONGO", uri)
	var warnings []string
	src, err := Open(context.Background(), "billing", config.Source{MongoDB: &config.MongoDB{
		URIEnv: "MC_MONGO", Database: db, RequireReadOnly: &requireReadOnly,
	}}, func(m string) { warnings = append(warnings, m) })
	if err == nil {
		t.Cleanup(func() { _ = src.Close(context.Background()) })
	}
	return src, warnings, err
}

func mongoCheck(t *testing.T, pipeline string) config.Check {
	return config.Check{Name: "c", Collection: "payments", Pipeline: *yamlNode(t, pipeline), Timeout: 5 * time.Second}
}

func TestMongoDBReadsDocuments(t *testing.T) {
	db, _, reader := mongoDB(t)
	src, warnings, err := openMongo(t, db, reader+"&readPreference=secondaryPreferred", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings for a read-only user: %v", warnings)
	}
	if rp := src.(*mongodb).readPref; rp == nil || rp.Mode().String() != "secondaryPreferred" {
		t.Errorf("read preference from the URI = %v", rp)
	}
	rows, err := collect(t, src, mongoCheck(t, `
- $sort: {_id: 1}
- $project: {user_id: 1, amount: 1, email: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	if strings.Join(rows[0].Columns, ",") != "_id,user_id,amount,email" {
		t.Errorf("columns %v", rows[0].Columns)
	}
	for i, want := range []int64{1010, 2699, 29} {
		v, _ := rows[i].Get("amount")
		if n, err := Minor(v, "major", 2); err != nil || n != want {
			t.Errorf("row %d: Minor(%#v) = %d, %v; want %d", i, v, n, err, want)
		}
	}
	rows, err = collect(t, src, mongoCheck(t, `
- $group: {_id: {user: "$user_id", period: "$period"}, n: {$sum: 1}}
- $match: {n: {$gt: 1}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d groups", len(rows))
	}
	if id, _ := rows[0].Get("_id"); id != `{"user":"u1","period":"2026-09"}` {
		t.Errorf("compound _id = %#v", id)
	}
	if n, _ := rows[0].Get("n"); n != int64(2) {
		t.Errorf("n = %#v", n)
	}
}

func TestMongoDBRefusesAWritingUser(t *testing.T) {
	db, admin, _ := mongoDB(t)
	_, _, err := openMongo(t, db, admin, true)
	if err == nil || !strings.Contains(err.Error(), "the user can write to "+db) || !strings.Contains(err.Error(), "require_read_only") {
		t.Fatalf("got %v", err)
	}
	src, warnings, err := openMongo(t, db, admin, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "source billing: mongodb: the user can write") {
		t.Errorf("warnings %v", warnings)
	}
	if _, err := collect(t, src, mongoCheck(t, `[{$match: {}}, {$out: copy}]`)); err == nil {
		t.Error("$out ran")
	}
}

// The check's timeout goes to the server as maxTimeMS, so the server gives up too,
// and the URI's read preference is kept, so checks can read from a secondary.
func TestMongoDBSendsMaxTimeMSAndReadPreference(t *testing.T) {
	db, _, reader := mongoDB(t)
	var sent bson.Raw
	monitor := &event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "aggregate" {
			sent = e.Command
		}
	}}
	opts := clientOptions(reader + "&readPreference=secondaryPreferred").SetMonitor(monitor)
	client, err := mongo.Connect(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	src := &mongodb{client: client, db: client.Database(db), readPref: opts.ReadPreference}
	if _, err := collect(t, src, mongoCheck(t, `[{$match: {}}]`)); err != nil {
		t.Fatal(err)
	}
	ms, ok := sent.Lookup("maxTimeMS").AsInt64OK()
	if !ok || ms <= 0 || ms > 5000 {
		t.Errorf("maxTimeMS = %d, %v in %s", ms, ok, sent)
	}
	// A single server gets no $readPreference in the command, so the test checks
	// what the source passes to the driver.
	if src.readPref == nil || src.readPref.Mode().String() != "secondaryPreferred" {
		t.Errorf("read preference = %v", src.readPref)
	}
	if c, _ := sent.Lookup("comment").StringValueOK(); c != "money-checks: c" {
		t.Errorf("comment = %q", c)
	}
}

// The start of the window is the variable $$since; the zero time reads every
// document.
func TestMongoDBWindow(t *testing.T) {
	db, admin, reader := mongoDB(t)
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(admin))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	_, err = client.Database(db).Collection("renewals").InsertMany(ctx, []any{
		bson.D{{Key: "_id", Value: "old"}, {Key: "paid_at", Value: bson.NewDateTimeFromTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))}},
		bson.D{{Key: "_id", Value: "new"}, {Key: "paid_at", Value: bson.NewDateTimeFromTime(time.Date(2026, 10, 8, 5, 0, 0, 0, time.UTC))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := openMongo(t, db, reader, true)
	if err != nil {
		t.Fatal(err)
	}
	ch := mongoCheck(t, `
- $match: {$expr: {$gte: ["$paid_at", "$$since"]}}
- $sort: {_id: 1}
`)
	ch.Collection, ch.Window = "renewals", 24*time.Hour
	ids := func(since time.Time) string {
		var got []string
		err := src.Query(ctx, ch, since, time.Time{}, func(r Row) error {
			got = append(got, r.Values[0].(string))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, ",")
	}
	if got := ids(time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC)); got != "new" {
		t.Errorf("window: %q", got)
	}
	if got := ids(time.Time{}); got != "new,old" {
		t.Errorf("full: %q", got)
	}
}

// settle: the end of the rows is $$until, with or without $$since.
func TestMongoDBUntil(t *testing.T) {
	db, admin, reader := mongoDB(t)
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(admin))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Disconnect(ctx) }()
	_, err = client.Database(db).Collection("renewals").InsertMany(ctx, []any{
		bson.D{{Key: "_id", Value: "old"}, {Key: "paid_at", Value: bson.NewDateTimeFromTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))}},
		bson.D{{Key: "_id", Value: "new"}, {Key: "paid_at", Value: bson.NewDateTimeFromTime(time.Date(2026, 10, 8, 5, 0, 0, 0, time.UTC))}},
		bson.D{{Key: "_id", Value: "fresh"}, {Key: "paid_at", Value: bson.NewDateTimeFromTime(time.Date(2026, 10, 8, 5, 55, 0, 0, time.UTC))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := openMongo(t, db, reader, true)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2026, 10, 8, 5, 45, 0, 0, time.UTC)
	for _, tc := range []struct {
		pipeline string
		window   time.Duration
		want     string
	}{
		{`[{$match: {$expr: {$lt: ["$paid_at", "$$until"]}}}, {$sort: {_id: 1}}]`, 0, "new,old"},
		{`[{$match: {$expr: {$and: [{$gte: ["$paid_at", "$$since"]}, {$lt: ["$paid_at", "$$until"]}]}}}, {$sort: {_id: 1}}]`, 24 * time.Hour, "new"},
	} {
		ch := mongoCheck(t, tc.pipeline)
		ch.Collection, ch.Window, ch.Settle = "renewals", tc.window, 15*time.Minute
		var got []string
		err := src.Query(ctx, ch, time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC), until, func(r Row) error {
			got = append(got, r.Values[0].(string))
			return nil
		})
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.pipeline, got, err, tc.want)
		}
	}
}

func TestMongoDBExpect(t *testing.T) {
	db, _, reader := mongoDB(t)
	src, _, err := openMongo(t, db, reader, true)
	if err != nil {
		t.Fatal(err)
	}
	ch := mongoCheck(t, `[{$match: {$expr: {$gte: ["$paid_at", "$$since"]}}}]`)
	ch.Window = 24 * time.Hour
	for pipeline, want := range map[string]any{
		`[{$count: n}]`: int64(3),
		`[{$match: {user_id: u1}}, {$group: {_id: null, n: {$sum: 1}}}]`: int64(2),
	} {
		ch.Expect = &config.Expect{Pipeline: *yamlNode(t, pipeline)}
		ex := ch.ExpectCheck("mongodb")
		if ex.Collection != "payments" || ex.Window != 0 {
			t.Errorf("%s: collection %q, window %s", pipeline, ex.Collection, ex.Window)
		}
		var got []Row
		err := src.Query(context.Background(), ex, time.Now(), time.Time{}, func(r Row) error {
			got = append(got, r)
			return nil
		})
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %v, %v", pipeline, got, err)
		}
		if n, _ := got[0].Get("n"); n != want {
			t.Errorf("%s: n = %v (%T), want %v", pipeline, n, n, want)
		}
	}
}
