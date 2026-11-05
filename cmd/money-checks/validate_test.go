package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A configuration with a check on each kind of query: a pipeline, its expect, and
// both sides of a compare check.
const validateConfig = `
version: 1
sources:
  shop: {mongodb: {uri_env: MC_VALIDATE_MONGO, database: shop}}
  ledger: {postgres: {dsn_env: MC_VALIDATE_PG}}
known_file: known.yaml
serve:
  slack: {webhook_env: MC_VALIDATE_SLACK}
checks:
  - name: refund_twice
    source: shop
    collection: refunds
    pipeline: [{$group: {_id: "$payment_id", n: {$sum: 1}}}, {$match: {n: {$gt: 1}}}]
    expect:
      pipeline: [{$count: n}]
  - name: payments_reach_ledger
    compare:
      key: [day]
      left:
        source: shop
        collection: payments
        pipeline: [{$group: {_id: null, day: {$first: "$day"}}}]
      right:
        source: ledger
        query: select day from payments group by day
`

func writeValidate(t *testing.T, conf string, known bool) string {
	t.Helper()
	path := writeConfig(t, conf)
	if known {
		k := "version: 1\nknown:\n  - {check: refund_twice, id: p_1, reason: refunded by hand}\n"
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "known.yaml"), []byte(k), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestValidate(t *testing.T) {
	path := writeValidate(t, validateConfig, true)
	code, out, errOut := runCLI(t, "validate", "-c", path)
	if code != exitOK || out != "checks.yaml: 2 checks on 2 sources, 1 known violation; nothing connected.\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	// payments_reach_ledger has no expect on either side.
	if !strings.Contains(errOut, "warning: checks without expect") || !strings.Contains(errOut, "payments_reach_ledger") {
		t.Errorf("stderr %q", errOut)
	}
}

// A pipeline that the configuration accepts but MongoDB would not: before
// validate, only the first run of the check said so.
func TestValidatePipeline(t *testing.T) {
	for _, c := range []struct{ name, from, to string }{
		{"check", `{$match: {n: {$gt: 1}}}`, `{$match: {at: {$date: "yesterday"}}}`},
		{"expect", `[{$count: n}]`, `[{$match: {at: {$date: "yesterday"}}}, {$count: n}]`},
		{"compare side", `{$first: "$day"}`, `{$first: {$date: "yesterday"}}`},
	} {
		path := writeValidate(t, strings.Replace(validateConfig, c.from, c.to, 1), true)
		code, out, errOut := runCLI(t, "validate", "-c", path)
		if code != exitError || out != "" || !strings.Contains(errOut, "pipeline") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", c.name, code, out, errOut)
		}
	}
}

// The known file is kept apart from the checks, as a secret: --without-known
// validates the checks alone.
func TestValidateWithoutKnown(t *testing.T) {
	path := writeValidate(t, validateConfig, false)
	if code, _, errOut := runCLI(t, "validate", "-c", path); code != exitError || !strings.Contains(errOut, "known_file") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if code, out, errOut := runCLI(t, "validate", "-c", path, "--without-known"); code != exitOK || !strings.Contains(out, "0 known violations") {
		t.Errorf("--without-known: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// With --env every variable the sources and Slack name must be set: the check
// before a deploy, where the secrets are.
func TestValidateEnv(t *testing.T) {
	path := writeValidate(t, validateConfig, true)
	t.Setenv("MC_VALIDATE_MONGO", "mongodb://127.0.0.1")
	t.Setenv("MC_VALIDATE_PG", "")
	t.Setenv("MC_VALIDATE_SLACK", "")
	code, out, errOut := runCLI(t, "validate", "-c", path, "--env")
	if code != exitError || out != "" || !strings.Contains(errOut, "source ledger: environment variable MC_VALIDATE_PG is empty") ||
		!strings.Contains(errOut, "serve.slack: environment variable MC_VALIDATE_SLACK is empty") || strings.Contains(errOut, "MC_VALIDATE_MONGO") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	t.Setenv("MC_VALIDATE_PG", "postgres://127.0.0.1/ledger")
	t.Setenv("MC_VALIDATE_SLACK", "https://hooks.slack.invalid/x")
	if code, _, errOut := runCLI(t, "validate", "-c", path, "--env"); code != exitOK {
		t.Errorf("all set: exit %d, stderr %q", code, errOut)
	}
}

func TestValidateBadConfig(t *testing.T) {
	path := writeConfig(t, "version: 1\nsurces: {}\n")
	if code, out, errOut := runCLI(t, "validate", "-c", path); code != exitError || out != "" || !strings.Contains(errOut, "surces") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
