package config

import (
	"strings"
	"testing"
)

const versioned = `
version: 1
sources:
  billing: {mongodb: {uri_env: X, database: shop}}
  ledger: {postgres: {dsn_env: Y}}
  replica: {postgres: {dsn_env: Z}}
checks:
  - name: refunds
    source: billing
    collection: payments
    pipeline:
      - $match: {kind: refund} # refunds only
      - $sort: {created: 1, _id: 1}
    id: _id
  - name: balances
    description: Negative balances.
    source: ledger
    query: select id from accounts where balance < 0
    id: id
`

func versions(t *testing.T, conf string) map[string]string {
	t.Helper()
	cfg, err := Parse([]byte(conf), ".")
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]string{}
	for _, ch := range cfg.Checks {
		v[ch.Name] = cfg.CheckVersion(ch)
	}
	return v
}

func TestVersion(t *testing.T) {
	base := versions(t, versioned)
	if len(base["refunds"]) != 8 || base["refunds"] == base["balances"] {
		t.Fatalf("versions %v", base)
	}
	if again := versions(t, versioned); again["refunds"] != base["refunds"] {
		t.Errorf("not stable: %v %v", base, again)
	}
	same := map[string]string{
		"description":       strings.Replace(versioned, "Negative balances.", "Accounts below zero.", 1),
		"comment":           strings.Replace(versioned, "# refunds only", "# only refunds", 1),
		"style":             strings.Replace(versioned, "{kind: refund}", "{kind: \"refund\"}", 1),
		"schedule":          strings.Replace(versioned, "    id: id\n", "    id: id\n    every: 1h\n", 1),
		"source of a kind":  strings.Replace(versioned, "source: ledger", "source: replica", 1),
		"show and samples":  strings.Replace(versioned, "    id: id\n", "    id: id\n    show: [id]\n    samples: 3\n", 1),
		"timeout and sever": strings.Replace(versioned, "    id: id\n", "    id: id\n    timeout: 1m\n    severity: warn\n", 1),
	}
	for name, conf := range same {
		if v := versions(t, conf); v["refunds"] != base["refunds"] || v["balances"] != base["balances"] {
			t.Errorf("%s changed the version", name)
		}
	}
	changed := map[string]string{
		"query":      strings.Replace(versioned, "balance < 0", "balance < -1", 1),
		"id":         strings.Replace(versioned, "query: select id from accounts where balance < 0\n    id: id", "query: select id as account from accounts where balance < 0\n    id: account", 1),
		"order":      strings.Replace(versioned, "{created: 1, _id: 1}", "{_id: 1, created: 1}", 1),
		"collection": strings.Replace(versioned, "collection: payments", "collection: refunds", 1),
	}
	for name, conf := range changed {
		v := versions(t, conf)
		if v["refunds"] == base["refunds"] && v["balances"] == base["balances"] {
			t.Errorf("%s kept the version", name)
		}
	}
}

func TestHistoryFile(t *testing.T) {
	cfg, err := Parse([]byte(versioned+"serve: {history_file: history.jsonl, state_file: state.json}\n"), "/etc/money-checks")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Serve.HistoryFile != "/etc/money-checks/history.jsonl" {
		t.Errorf("history_file %q", cfg.Serve.HistoryFile)
	}
	_, err = Parse([]byte(versioned+"serve: {history_file: state.json, state_file: state.json}\n"), ".")
	if err == nil || !strings.Contains(err.Error(), "serve.history_file: the same file as serve.state_file") {
		t.Errorf("err %v", err)
	}
}
