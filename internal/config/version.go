package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"go.yaml.in/yaml/v3"
)

// CheckVersion is a short hash of what decides which rows ch returns and how they are
// read: the kind of its source, the query or pipeline, the collection, the id and
// the amount, settle, and for a compare check both sides, the key, the counts and
// the tolerance. The description, the schedule, the window, show, mask, expect and
// the known file are left out: they change when a check runs or what a report
// shows, not what a full run finds. A comment in a pipeline is left out too. See
// docs/adr/0010-violation-history.md.
func (c Config) CheckVersion(ch Check) string {
	type side struct {
		Kind, Collection, Pipeline, Query string
	}
	read := func(source, collection string, pipeline *yaml.Node, query string) side {
		return side{Kind: c.Sources[source].Kind(), Collection: collection, Pipeline: canonical(pipeline), Query: query}
	}
	v := struct {
		Check          side    `json:",omitzero"`
		Left, Right    side    `json:",omitzero"`
		ID             string  `json:",omitempty"`
		Amount         *Amount `json:",omitempty"`
		Key, Counts    []string
		ToleranceMinor int64  `json:",omitempty"`
		Settle         string `json:",omitempty"`
	}{ID: ch.ID, Amount: ch.Amount}
	if ch.Settle > 0 {
		v.Settle = ch.Settle.String()
	}
	if cmp := ch.Compare; cmp != nil {
		v.Left = read(cmp.Left.Source, cmp.Left.Collection, &cmp.Left.Pipeline, cmp.Left.Query)
		v.Right = read(cmp.Right.Source, cmp.Right.Collection, &cmp.Right.Pipeline, cmp.Right.Query)
		v.Amount, v.Key, v.Counts, v.ToleranceMinor = cmp.Amount, cmp.Key, cmp.Counts, cmp.ToleranceMinor
	} else {
		v.Check = read(ch.Source, ch.Collection, &ch.Pipeline, ch.Query)
	}
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// canonical writes a YAML node without its comments and styles, in its order: a
// pipeline is a list of stages, and the order of the keys of a stage such as
// $sort is part of what it does.
func canonical(n *yaml.Node) string {
	var b strings.Builder
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode:
			b.WriteString("(")
			b.WriteString(n.Tag)
			for _, c := range n.Content {
				b.WriteString(" ")
				walk(c)
			}
			b.WriteString(")")
		case yaml.AliasNode:
			if n.Alias != nil {
				walk(n.Alias)
			}
		case yaml.ScalarNode:
			q, _ := json.Marshal(n.Value)
			b.WriteString(n.Tag)
			b.Write(q)
		}
	}
	walk(n)
	return b.String()
}
