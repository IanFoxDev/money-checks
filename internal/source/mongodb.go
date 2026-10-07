package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.yaml.in/yaml/v3"

	"github.com/ianfoxdev/money-checks/internal/config"
)

type mongodb struct {
	client   *mongo.Client
	db       *mongo.Database
	readPref *readpref.ReadPref // from the URI; nil is primary
}

func openMongoDB(ctx context.Context, m config.MongoDB, warn func(string)) (*mongodb, error) {
	uri, err := config.Getenv(m.URIEnv)
	if err != nil {
		return nil, err
	}
	opts := clientOptions(uri)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w", err)
	}
	src := &mongodb{client: client, db: client.Database(m.Database), readPref: opts.ReadPreference}
	fail := func(err error) (*mongodb, error) {
		_ = client.Disconnect(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("mongodb: %w", err)
	}
	var status struct {
		AuthInfo connectionAuth `bson:"authInfo"`
	}
	cmd := bson.D{{Key: "connectionStatus", Value: 1}, {Key: "showPrivileges", Value: true}}
	if err := src.db.RunCommand(ctx, cmd).Decode(&status); err != nil {
		return fail(err)
	}
	if problem := status.AuthInfo.writeProblem(m.Database); problem != "" {
		if m.RequireReadOnly == nil || *m.RequireReadOnly {
			return fail(fmt.Errorf("%s; connect as a user with the read role, or set require_read_only: false to accept the risk", problem))
		}
		warn("mongodb: " + problem + "; running anyway because require_read_only is false")
	}
	return src, nil
}

// clientOptions reads the URI, including its read preference.
func clientOptions(uri string) *options.ClientOptions {
	return options.Client().ApplyURI(uri)
}

// connectionAuth is the part of connectionStatus that says who is connected and
// what they may do.
type connectionAuth struct {
	Users      []bson.Raw `bson:"authenticatedUsers"`
	Privileges []struct {
		Resource struct {
			DB          *string `bson:"db"`
			Collection  *string `bson:"collection"`
			Cluster     bool    `bson:"cluster"`
			AnyResource bool    `bson:"anyResource"`
		} `bson:"resource"`
		Actions []string `bson:"actions"`
	} `bson:"authenticatedUserPrivileges"`
}

// writeActions change data or the shape of a database.
var writeActions = map[string]bool{
	"insert": true, "update": true, "remove": true,
	"createCollection": true, "dropCollection": true, "dropDatabase": true,
	"renameCollectionSameDB": true, "convertToCapped": true, "collMod": true,
	"createIndex": true, "dropIndex": true, "applyOps": true, "compact": true,
}

// writeProblem says why the user is not read-only on database, or "" when it is.
// No authenticated user means the server may not check access at all, and then
// anyone can write.
func (a connectionAuth) writeProblem(database string) string {
	if len(a.Users) == 0 {
		return "connected without a user, so nothing proves the connection is read-only"
	}
	found := map[string]bool{}
	for _, p := range a.Privileges {
		r := p.Resource
		covers := r.AnyResource || r.Cluster || r.DB != nil && (*r.DB == "" || *r.DB == database)
		if !covers {
			continue
		}
		for _, act := range p.Actions {
			if writeActions[act] {
				found[act] = true
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	acts := make([]string, 0, len(found))
	for a := range found {
		acts = append(acts, a)
	}
	sort.Strings(acts)
	return fmt.Sprintf("the user can write to %s (%s)", database, strings.Join(acts, ", "))
}

// Query runs the pipeline with the check's timeout as the context deadline, which
// the driver also sends as maxTimeMS, so the server stops the work when the check
// times out. The read preference is the URI's.
func (m *mongodb) Query(ctx context.Context, ch config.Check, each func(Row) error) error {
	pipeline, err := Pipeline(&ch.Pipeline)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, ch.Timeout)
	defer cancel()
	// Collection.Aggregate never sends maxTimeMS (DRIVERS-2722), so the command is
	// sent as is: the driver adds maxTimeMS from the deadline to commands.
	cmd := bson.D{
		{Key: "aggregate", Value: ch.Collection},
		{Key: "pipeline", Value: pipeline},
		{Key: "cursor", Value: bson.D{}},
		{Key: "comment", Value: "money-checks: " + ch.Name},
	}
	opts := options.RunCmd()
	if m.readPref != nil {
		opts.SetReadPreference(m.readPref)
	}
	cur, err := m.db.RunCommandCursor(ctx, cmd, opts)
	if err != nil {
		return err
	}
	defer func() { _ = cur.Close(context.WithoutCancel(ctx)) }()
	for cur.Next(ctx) {
		var doc bson.D
		if err := cur.Decode(&doc); err != nil {
			return err
		}
		row := Row{Columns: make([]string, len(doc)), Values: make([]any, len(doc))}
		for i, e := range doc {
			row.Columns[i] = e.Key
			row.Values[i] = mongoValue(e.Value)
		}
		if err := each(row); err != nil {
			return err
		}
	}
	return cur.Err()
}

func (m *mongodb) Close(ctx context.Context) error {
	return m.client.Disconnect(ctx)
}

// mongoValue turns a BSON value into one of the Row value types. Documents and
// arrays, such as a compound _id from $group, become relaxed Extended JSON text.
func mongoValue(v any) any {
	switch x := v.(type) {
	case nil, bson.Null, bson.Undefined:
		return nil
	case int32:
		return int64(x)
	case int64, float64, string, bool:
		return x
	case bson.Decimal128:
		coef, exp, err := x.BigInt()
		if err != nil { // NaN or Infinity
			return x.String()
		}
		return Decimal{Coef: coef, Scale: exp}
	case bson.ObjectID:
		return x.Hex()
	case bson.DateTime:
		return x.Time().UTC().Format(time.RFC3339Nano)
	case bson.D:
		return extJSON(x)
	case bson.A:
		b, err := bson.MarshalExtJSON(bson.D{{Key: "v", Value: x}}, false, false)
		if err != nil {
			return fmt.Sprint(x)
		}
		// {"v":[...]} -> [...]
		return string(bytes.TrimSuffix(bytes.TrimPrefix(b, []byte(`{"v":`)), []byte("}")))
	default:
		return fmt.Sprint(x)
	}
}

func extJSON(d bson.D) string {
	b, err := bson.MarshalExtJSON(d, false, false)
	if err != nil {
		return fmt.Sprint(d)
	}
	return string(b)
}

// Pipeline turns the YAML pipeline into BSON stages with the key order kept
// ($sort and compound _id depend on it). Values may use Extended JSON, such as
// {$date: "2026-10-01T00:00:00Z"} or {$numberDecimal: "10.99"}. A stage that
// writes is refused here as well as when the configuration is loaded.
func Pipeline(n *yaml.Node) (bson.A, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"p":`)
	if err := writeJSON(&buf, n); err != nil {
		return nil, fmt.Errorf("pipeline: %w", err)
	}
	buf.WriteString("}")
	var doc bson.D
	if err := bson.UnmarshalExtJSON(buf.Bytes(), false, &doc); err != nil {
		return nil, fmt.Errorf("pipeline: %w", err)
	}
	stages, ok := doc[0].Value.(bson.A)
	if !ok {
		return nil, errors.New("pipeline: want a list of stages")
	}
	if key := findWriteStage(stages); key != "" {
		return nil, fmt.Errorf("pipeline: %s writes to the database; checks only read", key)
	}
	return stages, nil
}

func findWriteStage(v any) string {
	switch x := v.(type) {
	case bson.D:
		for _, e := range x {
			if e.Key == "$out" || e.Key == "$merge" {
				return e.Key
			}
			if k := findWriteStage(e.Value); k != "" {
				return k
			}
		}
	case bson.A:
		for _, e := range x {
			if k := findWriteStage(e); k != "" {
				return k
			}
		}
	}
	return ""
}

// writeJSON writes a YAML node as JSON with mapping keys in their order.
func writeJSON(buf *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return errors.New("empty document")
		}
		return writeJSON(buf, n.Content[0])
	case yaml.AliasNode:
		return writeJSON(buf, n.Alias)
	case yaml.MappingNode:
		buf.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				buf.WriteByte(',')
			}
			k, _ := json.Marshal(n.Content[i].Value)
			buf.Write(k)
			buf.WriteByte(':')
			if err := writeJSON(buf, n.Content[i+1]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSON(buf, c); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		buf.Write(b)
	default:
		return errors.New("empty pipeline")
	}
	return nil
}
