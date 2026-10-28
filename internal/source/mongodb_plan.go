package source

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/ianfoxdev/money-checks/internal/config"
)

// Explain sends the aggregation of the check inside an explain command, with
// queryPlanner verbosity, or executionStats with analyze, which runs it. The
// deadline of the check goes on the explain command as maxTimeMS: set inside the
// aggregate, the server ignores it. See docs/adr/0009-explain.md.
func (m *mongodb) Explain(ctx context.Context, ch config.Check, since, until time.Time, analyze bool) (Plan, error) {
	pipeline, err := Pipeline(&ch.Pipeline)
	if err != nil {
		return Plan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	agg := bson.D{
		{Key: "aggregate", Value: ch.Collection},
		{Key: "pipeline", Value: pipeline},
		{Key: "cursor", Value: bson.D{}},
		{Key: "comment", Value: "money-checks explain: " + ch.Name},
	}
	var let bson.D
	if ch.Window > 0 {
		let = append(let, bson.E{Key: "since", Value: bson.NewDateTimeFromTime(since)})
	}
	if ch.Settle > 0 {
		let = append(let, bson.E{Key: "until", Value: bson.NewDateTimeFromTime(until)})
	}
	if len(let) > 0 {
		agg = append(agg, bson.E{Key: "let", Value: let})
	}
	verbosity := "queryPlanner"
	if analyze {
		verbosity = "executionStats"
	}
	opts := options.RunCmd()
	if m.readPref != nil {
		opts.SetReadPreference(m.readPref)
	}
	raw, err := m.db.RunCommand(ctx, bson.D{{Key: "explain", Value: agg}, {Key: "verbosity", Value: verbosity}}, opts).Raw()
	if err != nil {
		return Plan{}, err
	}
	text, err := bson.MarshalExtJSON(raw, false, false)
	if err != nil {
		return Plan{}, fmt.Errorf("mongodb: the plan: %w", err)
	}
	p, err := mongoSummary(string(text), analyze)
	if err != nil || analyze || len(p.Scans) == 0 {
		return p, err
	}
	// The query planner gives no estimate of rows; the count of a collection
	// read in full comes from its metadata and reads no documents.
	var rows int64
	for _, name := range p.Scans {
		copts := options.Collection()
		if m.readPref != nil {
			copts.SetReadPreference(m.readPref)
		}
		n, err := m.db.Collection(name, copts).EstimatedDocumentCount(ctx)
		if err != nil {
			p.Notes = append(p.Notes, "the documents of "+name+" could not be counted: "+err.Error())
			return p, nil
		}
		rows += n
	}
	p.Rows = rows
	p.Notes = append(p.Notes, "rows are the documents of the collections read in full, each counted once; MongoDB does not estimate the rest")
	return p, nil
}

// mongoSummary walks an explain result. A query planner part names its
// collection; a $lookup stage that the query planner did not take in says how it
// read the other collection only in executionStats.
func mongoSummary(raw string, analyze bool) (Plan, error) {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Plan{}, fmt.Errorf("mongodb: the plan is not explain output: %w", err)
	}
	s := &plan{Plan: Plan{Raw: raw, Analyzed: analyze, Rows: -1}}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if qp, ok := x["queryPlanner"].(map[string]any); ok {
				ns, _ := qp["namespace"].(string)
				mongoPlanStage(s, qp["winningPlan"], mongoCollection(ns))
				if st, ok := x["executionStats"].(map[string]any); ok {
					s.rows += mongoNumber(st["totalDocsExamined"])
				}
			}
			if l, ok := x["$lookup"].(map[string]any); ok {
				from, _ := l["from"].(string)
				if _, ran := x["totalDocsExamined"]; ran {
					s.rows += mongoNumber(x["totalDocsExamined"])
					if mongoNumber(x["collectionScans"]) > 0 {
						s.scan(from)
					}
					if used, ok := x["indexesUsed"].([]any); ok {
						for _, i := range used {
							if name, ok := i.(string); ok {
								s.index(name, from)
							}
						}
					}
				} else if from != "" {
					s.note("$lookup from " + from + " is not checked: the plan does not show how it reads " + from + "; explain with --analyze")
				}
			}
			if g, ok := x["$graphLookup"].(map[string]any); ok {
				from, _ := g["from"].(string)
				s.note("$graphLookup from " + from + " is not checked: explain does not show how it reads " + from)
			}
			for _, k := range sortedKeys(x) {
				switch k {
				case "queryPlanner", "executionStats":
				case "command":
					// The explained command as sent: its $lookup has no stats.
				default:
					walk(x[k])
				}
			}
		}
	}
	walk(doc)
	p := s.done()
	if !analyze {
		// The query planner of MongoDB gives no estimate of rows.
		p.Rows = -1
	}
	return p, nil
}

// mongoPlanStage reads one stage of a winning plan and the stages under it.
func mongoPlanStage(s *plan, v any, coll string) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			mongoPlanStage(s, e, coll)
		}
	case map[string]any:
		name, _ := x["indexName"].(string)
		switch x["stage"] {
		case "COLLSCAN":
			s.scan(coll)
		case "EQ_LOOKUP":
			foreign, _ := x["foreignCollection"].(string)
			foreign = mongoCollection(foreign)
			if x["strategy"] == "IndexedLoopJoin" {
				s.index(name, foreign)
			} else {
				// HashJoin reads the other collection once in full, NestedLoopJoin
				// once for every document.
				s.scan(foreign)
			}
		case "IDHACK":
			if name == "" {
				name = "_id_"
			}
			s.index(name, coll)
		default:
			if name != "" {
				s.index(name, coll)
			}
		}
		for _, k := range sortedKeys(x) {
			switch k {
			case "inputStage", "inputStages", "queryPlan", "outerStage", "innerStage", "thenStage", "elseStage":
				mongoPlanStage(s, x[k], coll)
			}
		}
	}
}

// mongoCollection is the collection of a namespace "database.collection".
func mongoCollection(ns string) string {
	if _, c, ok := strings.Cut(ns, "."); ok {
		return c
	}
	return ns
}

// mongoNumber reads a number of relaxed Extended JSON: a plain number, or
// {"$numberLong": "..."} for one too large for a double.
func mongoNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case map[string]any:
		var n float64
		if s, ok := x["$numberLong"].(string); ok {
			_, _ = fmt.Sscan(s, &n)
		}
		return n
	}
	return 0
}

// sortedKeys gives the keys of a JSON object in order, so a summary lists the
// same plan the same way every time.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ Explainer = (*mongodb)(nil)
