package source

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoEvents adds to the test database 2000 events with an index on at, 500
// refs with an index on k, and 500 plain documents without one, and opens it as
// the user with the read role.
func mongoEvents(t *testing.T) Source {
	t.Helper()
	db, admin, reader := mongoDB(t)
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(admin))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(ctx) })
	d := client.Database(db)
	now := time.Now()
	var events, refs, plain []any
	for i := range 2000 {
		events = append(events, bson.D{{Key: "_id", Value: i}, {Key: "ref", Value: i % 500}, {Key: "amount", Value: i % 100}, {Key: "at", Value: now.Add(-time.Duration(i) * time.Minute)}})
	}
	for i := range 500 {
		refs = append(refs, bson.D{{Key: "_id", Value: i}, {Key: "k", Value: i}})
		plain = append(plain, bson.D{{Key: "_id", Value: i}, {Key: "k", Value: i}})
	}
	for name, docs := range map[string][]any{"events": events, "refs": refs, "plain": plain} {
		if _, err := d.Collection(name).InsertMany(ctx, docs); err != nil {
			t.Fatal(err)
		}
	}
	for name, key := range map[string]string{"events": "at", "refs": "k"} {
		if _, err := d.Collection(name).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: key, Value: 1}}}); err != nil {
			t.Fatal(err)
		}
	}
	src, _, err := openMongo(t, db, reader, true)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func explainMongo(t *testing.T, src Source, pipeline string, window bool, analyze bool) Plan {
	t.Helper()
	ch := mongoCheck(t, pipeline)
	ch.Collection = "events"
	since := time.Time{}
	if window {
		ch.Window, since = time.Hour, time.Now().Add(-time.Hour)
	}
	p, err := src.(Explainer).Explain(context.Background(), ch, since, time.Time{}, analyze)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMongoDBExplainWindowUsesTheIndex(t *testing.T) {
	src := mongoEvents(t)
	const pipeline = `[{$match: {$expr: {$gte: ["$at", "$$since"]}}}]`
	p := explainMongo(t, src, pipeline, true, false)
	if len(p.Scans) != 0 || !slices.Equal(p.Indexes, []string{"at_1 on events"}) || p.Rows != -1 || p.Analyzed {
		t.Fatalf("scans %v, indexes %v, rows %d; want the index on at and no estimate", p.Scans, p.Indexes, p.Rows)
	}
	if p = explainMongo(t, src, pipeline, true, true); p.Rows < 55 || p.Rows > 65 || !p.Analyzed {
		t.Fatalf("with analyze: rows %d; want about 60 read", p.Rows)
	}
}

func TestMongoDBExplainFullScan(t *testing.T) {
	src := mongoEvents(t)
	const pipeline = `[{$match: {amount: 7}}, {$group: {_id: null, n: {$sum: 1}}}]`
	if p := explainMongo(t, src, pipeline, false, false); !slices.Equal(p.Scans, []string{"events"}) {
		t.Fatalf("scans %v; want events read in full", p.Scans)
	}
	if p := explainMongo(t, src, pipeline, false, true); !slices.Equal(p.Scans, []string{"events"}) || p.Rows != 2000 {
		t.Fatalf("with analyze: scans %v, rows %d; want 2000 read", p.Scans, p.Rows)
	}
}

// A $lookup with a pipeline is not part of the query plan: without analyze the
// summary says so rather than "no full scans".
func TestMongoDBExplainLookupWithPipeline(t *testing.T) {
	src := mongoEvents(t)
	lookup := func(from string) string {
		return `[{$match: {$expr: {$gte: ["$at", "$$since"]}}}, {$lookup: {from: ` + from + `, let: {r: "$ref"}, pipeline: [{$match: {$expr: {$eq: ["$k", "$$r"]}}}], as: x}}]`
	}
	p := explainMongo(t, src, lookup("plain"), true, false)
	if len(p.Scans) != 0 || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "$lookup from plain is not checked") {
		t.Fatalf("scans %v, notes %v", p.Scans, p.Notes)
	}
	if p = explainMongo(t, src, lookup("plain"), true, true); !slices.Equal(p.Scans, []string{"plain"}) || len(p.Notes) != 0 || p.Rows < 60*500 {
		t.Fatalf("with analyze: scans %v, notes %v, rows %d; want plain read once for each of about 60 events", p.Scans, p.Notes, p.Rows)
	}
	if p = explainMongo(t, src, lookup("refs"), true, true); len(p.Scans) != 0 || !slices.Contains(p.Indexes, "k_1 on refs") {
		t.Fatalf("refs with analyze: scans %v, indexes %v", p.Scans, p.Indexes)
	}
}

// A $lookup on localField and foreignField that the query planner takes in shows
// how it reads the other collection without analyze.
func TestMongoDBExplainLookupInThePlan(t *testing.T) {
	src := mongoEvents(t)
	lookup := func(from string) string {
		return `[{$match: {$expr: {$gte: ["$at", "$$since"]}}}, {$lookup: {from: ` + from + `, localField: ref, foreignField: k, as: x}}]`
	}
	if p := explainMongo(t, src, lookup("refs"), true, false); len(p.Scans) != 0 || !slices.Contains(p.Indexes, "k_1 on refs") || len(p.Notes) != 0 {
		t.Fatalf("refs: scans %v, indexes %v, notes %v", p.Scans, p.Indexes, p.Notes)
	}
	if p := explainMongo(t, src, lookup("plain"), true, false); !slices.Equal(p.Scans, []string{"plain"}) {
		t.Fatalf("plain: scans %v; want it read in full", p.Scans)
	}
}

func TestMongoDBExplainSendsMaxTimeMSOnExplain(t *testing.T) {
	db, _, reader := mongoDB(t)
	var sent bson.Raw
	monitor := &event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "explain" {
			sent = e.Command
		}
	}}
	opts := clientOptions(reader).SetMonitor(monitor)
	client, err := mongo.Connect(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	src := &mongodb{client: client, db: client.Database(db)}
	if _, err := src.Explain(context.Background(), mongoCheck(t, `[{$match: {}}]`), time.Time{}, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if ms, ok := sent.Lookup("maxTimeMS").AsInt64OK(); !ok || ms <= 0 || ms > 5000 {
		t.Errorf("maxTimeMS = %d, %v in %s", ms, ok, sent)
	}
	if v, _ := sent.Lookup("verbosity").StringValueOK(); v != "executionStats" {
		t.Errorf("verbosity %q", v)
	}
}

func TestMongoDBExplainAnalyzeTimeout(t *testing.T) {
	src := mongoEvents(t)
	ch := mongoCheck(t, `[{$match: {$expr: {$gt: [{$reduce: {input: {$range: [0, 3000000]}, initialValue: 0, in: {$add: ["$$value", 1]}}}, 0]}}}]`)
	ch.Collection, ch.Timeout = "events", 300*time.Millisecond
	start := time.Now()
	_, err := src.(Explainer).Explain(context.Background(), ch, time.Time{}, time.Time{}, true)
	if err == nil {
		t.Fatalf("no error after %s", time.Since(start))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stopped after %s: %v", d, err)
	}
}
