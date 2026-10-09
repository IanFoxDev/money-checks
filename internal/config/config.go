// Package config reads checks.yaml: the databases to read and the checks to run.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ianfoxdev/money-checks/internal/money"
)

// Config is a whole checks.yaml.
type Config struct {
	Version    int               `yaml:"version"`
	Currencies map[string]int    `yaml:"currencies"`
	Defaults   Defaults          `yaml:"defaults"`
	Sources    map[string]Source `yaml:"sources"`
	Checks     []Check           `yaml:"checks"`
	Serve      Serve             `yaml:"serve"`
	// KnownFile lists violations that are accepted, relative to this file.
	KnownFile string `yaml:"known_file"`

	// Money knows the currencies, with the overrides from Currencies.
	Money money.Currencies `yaml:"-"`
	// Known is KnownFile read: check name, then id.
	Known map[string]map[string]Known `yaml:"-"`
}

// Defaults apply to every check that does not set its own value.
type Defaults struct {
	Timeout time.Duration `yaml:"timeout"`
	Samples *int          `yaml:"samples"`
}

// Source is one database. Connection strings come from environment variables,
// never from the file.
type Source struct {
	MongoDB  *MongoDB  `yaml:"mongodb"`
	Postgres *Postgres `yaml:"postgres"`
}

// Kind is "mongodb" or "postgres".
func (s Source) Kind() string {
	if s.MongoDB != nil {
		return "mongodb"
	}
	return "postgres"
}

// MongoDB reads with aggregation pipelines. The read preference comes from the URI.
type MongoDB struct {
	URIEnv   string `yaml:"uri_env"`
	Database string `yaml:"database"`
	// RequireReadOnly stops the run when the user can write. Default true.
	RequireReadOnly *bool `yaml:"require_read_only"`
}

// Postgres runs every check in a READ ONLY transaction.
type Postgres struct {
	DSNEnv string `yaml:"dsn_env"`
}

// Check is one invariant: a query that returns the rows that break it.
type Check struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Source      string `yaml:"source"`
	Severity    string `yaml:"severity"` // "error" (default) or "warn"

	// MongoDB.
	Collection   string    `yaml:"collection"`
	Pipeline     yaml.Node `yaml:"pipeline"`
	PipelineFile string    `yaml:"pipeline_file"`

	// PostgreSQL.
	Query     string `yaml:"query"`
	QueryFile string `yaml:"query_file"`

	ID      string        `yaml:"id"`
	Amount  *Amount       `yaml:"amount"`
	Show    []string      `yaml:"show"`
	Mask    []string      `yaml:"mask"`
	Samples *int          `yaml:"samples"`
	Timeout time.Duration `yaml:"timeout"`
}

// Amount says which column holds the money of a violation and in what unit.
type Amount struct {
	Field         string `yaml:"field"`
	Unit          string `yaml:"unit"` // "minor" or "major", no default
	Currency      string `yaml:"currency"`
	CurrencyField string `yaml:"currency_field"`
}

// Serve configures the long-running mode.
type Serve struct {
	Interval time.Duration `yaml:"interval"`
	Listen   string        `yaml:"listen"`
	Slack    *Slack        `yaml:"slack"`
}

// Slack posts when a check starts or stops finding violations, or fails.
type Slack struct {
	WebhookEnv string `yaml:"webhook_env"`
}

// Defaults when neither the check nor the defaults section sets a value.
const (
	DefaultTimeout  = 30 * time.Second
	DefaultSamples  = 10
	DefaultInterval = 10 * time.Minute
	DefaultListen   = ":8080"
	MaxSamples      = 1000
)

// Load reads and checks a configuration file. pipeline_file and query_file are
// resolved against the file's directory.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(data, filepath.Dir(path))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse decodes and checks a configuration. Unknown keys are errors with the line
// they are on. All problems are reported at once.
func Parse(data []byte, dir string) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, err
	}
	var errs []error
	cur, err := money.NewCurrencies(c.Currencies)
	if err != nil {
		errs = append(errs, fmt.Errorf("currencies: %w", err))
	}
	c.Money = cur
	errs = append(errs, c.readFiles(dir)...)
	c.applyDefaults()
	errs = append(errs, c.check()...)
	errs = append(errs, c.readKnown(dir)...)
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

func (c *Config) readFiles(dir string) []error {
	var errs []error
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.QueryFile != "" && ch.Query != "" {
			errs = append(errs, fmt.Errorf("check %s: set query or query_file, not both", ch.Name))
			continue
		}
		if ch.PipelineFile != "" && ch.Pipeline.Kind != 0 {
			errs = append(errs, fmt.Errorf("check %s: set pipeline or pipeline_file, not both", ch.Name))
			continue
		}
		if ch.QueryFile != "" {
			b, err := os.ReadFile(filepath.Join(dir, ch.QueryFile))
			if err != nil {
				errs = append(errs, fmt.Errorf("check %s: query_file: %w", ch.Name, err))
				continue
			}
			ch.Query = string(b)
		}
		if ch.PipelineFile != "" {
			b, err := os.ReadFile(filepath.Join(dir, ch.PipelineFile))
			if err != nil {
				errs = append(errs, fmt.Errorf("check %s: pipeline_file: %w", ch.Name, err))
				continue
			}
			// JSON is YAML, so one decoder reads both.
			var doc yaml.Node
			if err := yaml.Unmarshal(b, &doc); err != nil {
				errs = append(errs, fmt.Errorf("check %s: pipeline_file %s: %w", ch.Name, ch.PipelineFile, err))
				continue
			}
			if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
				ch.Pipeline = *doc.Content[0]
			}
		}
	}
	return errs
}

func (c *Config) applyDefaults() {
	if c.Defaults.Timeout == 0 {
		c.Defaults.Timeout = DefaultTimeout
	}
	if c.Defaults.Samples == nil {
		n := DefaultSamples
		c.Defaults.Samples = &n
	}
	for name, s := range c.Sources {
		if s.MongoDB != nil && s.MongoDB.RequireReadOnly == nil {
			yes := true
			s.MongoDB.RequireReadOnly = &yes
			c.Sources[name] = s
		}
	}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.Severity == "" {
			ch.Severity = "error"
		}
		if ch.Timeout == 0 {
			ch.Timeout = c.Defaults.Timeout
		}
		if ch.Samples == nil {
			ch.Samples = c.Defaults.Samples
		}
		if s, ok := c.Sources[ch.Source]; ok && s.MongoDB != nil && ch.ID == "" {
			ch.ID = "_id"
		}
	}
	if c.Serve.Interval == 0 {
		c.Serve.Interval = DefaultInterval
	}
	if c.Serve.Listen == "" {
		c.Serve.Listen = DefaultListen
	}
}

var checkName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (c *Config) check() []error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version: want 1, got %d", c.Version))
	}
	if c.Defaults.Timeout < 0 {
		errs = append(errs, errors.New("defaults.timeout: must be positive"))
	}
	if n := *c.Defaults.Samples; n < 0 || n > MaxSamples {
		errs = append(errs, fmt.Errorf("defaults.samples: want 0..%d, got %d", MaxSamples, n))
	}
	if len(c.Sources) == 0 {
		errs = append(errs, errors.New("sources: at least one is required"))
	}
	for _, name := range sortedKeys(c.Sources) {
		errs = append(errs, c.Sources[name].check(name)...)
	}
	if len(c.Checks) == 0 {
		errs = append(errs, errors.New("checks: at least one is required"))
	}
	seen := map[string]bool{}
	for i, ch := range c.Checks {
		where := fmt.Sprintf("checks[%d]", i)
		if ch.Name != "" {
			where = "check " + ch.Name
		}
		if !checkName.MatchString(ch.Name) {
			errs = append(errs, fmt.Errorf("%s: name must match %s, got %q", where, checkName, ch.Name))
		}
		if seen[ch.Name] {
			errs = append(errs, fmt.Errorf("%s: the name is used twice", where))
		}
		seen[ch.Name] = true
		errs = append(errs, c.checkCheck(where, ch)...)
	}
	if c.Serve.Interval < time.Minute {
		errs = append(errs, fmt.Errorf("serve.interval: at least 1m, got %s", c.Serve.Interval))
	}
	if c.Serve.Slack != nil && c.Serve.Slack.WebhookEnv == "" {
		errs = append(errs, errors.New("serve.slack.webhook_env: the name of the environment variable with the webhook URL"))
	}
	return errs
}

func (s Source) check(name string) []error {
	where := "sources." + name
	switch {
	case s.MongoDB == nil && s.Postgres == nil:
		return []error{fmt.Errorf("%s: set mongodb or postgres", where)}
	case s.MongoDB != nil && s.Postgres != nil:
		return []error{fmt.Errorf("%s: set mongodb or postgres, not both", where)}
	case s.MongoDB != nil:
		var errs []error
		if s.MongoDB.URIEnv == "" {
			errs = append(errs, fmt.Errorf("%s.mongodb.uri_env: the name of the environment variable with the URI", where))
		}
		if s.MongoDB.Database == "" {
			errs = append(errs, fmt.Errorf("%s.mongodb.database: required", where))
		}
		return errs
	default:
		if s.Postgres.DSNEnv == "" {
			return []error{fmt.Errorf("%s.postgres.dsn_env: the name of the environment variable with the DSN", where)}
		}
		return nil
	}
}

func (c *Config) checkCheck(where string, ch Check) []error {
	var errs []error
	add := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf(where+": "+format, a...))
	}
	if ch.Severity != "error" && ch.Severity != "warn" {
		add("severity: want error or warn, got %q", ch.Severity)
	}
	if ch.Timeout < 0 {
		add("timeout: must be positive")
	}
	if n := *ch.Samples; n < 0 || n > MaxSamples {
		add("samples: want 0..%d, got %d", MaxSamples, n)
	}
	src, ok := c.Sources[ch.Source]
	switch {
	case ch.Source == "":
		add("source: required")
	case !ok:
		add("source: unknown %q, known: %s", ch.Source, strings.Join(sortedKeys(c.Sources), ", "))
	case src.MongoDB != nil:
		if ch.Query != "" {
			add("query is for postgres sources; %s is mongodb, use collection and pipeline", ch.Source)
		}
		if ch.Collection == "" {
			add("collection: required for a mongodb source")
		}
		for _, err := range checkPipeline(&ch.Pipeline) {
			add("pipeline: %w", err)
		}
	case src.Postgres != nil:
		if ch.Collection != "" || ch.Pipeline.Kind != 0 {
			add("collection and pipeline are for mongodb sources; %s is postgres, use query", ch.Source)
		}
		if strings.TrimSpace(ch.Query) == "" {
			add("set query or query_file")
		}
		if ch.ID == "" {
			add("id: the column that identifies a violating row")
		}
	}
	if a := ch.Amount; a != nil {
		if a.Field == "" {
			add("amount.field: required")
		}
		if a.Unit != "minor" && a.Unit != "major" {
			add("amount.unit: want minor or major, got %q", a.Unit)
		}
		switch {
		case (a.Currency == "") == (a.CurrencyField == ""):
			add("amount: set currency or currency_field")
		case a.Currency != "":
			if _, ok := c.Money.Exponent(a.Currency); !ok {
				add("amount.currency: unknown %q; add it under currencies with its number of decimal places", a.Currency)
			}
		}
	}
	show := map[string]bool{}
	for _, f := range ch.Show {
		show[f] = true
	}
	for _, f := range ch.Mask {
		if !show[f] {
			add("mask: %q is not in show; only shown columns can be masked", f)
		}
	}
	return errs
}

// writeStages change data. A check must never run them.
var writeStages = map[string]bool{"$out": true, "$merge": true}

// checkPipeline wants a list of stages, each a mapping with one key that starts
// with $, and no stage that writes anywhere in the tree.
func checkPipeline(n *yaml.Node) []error {
	if n.Kind == 0 {
		return []error{errors.New("required for a mongodb source (or pipeline_file)")}
	}
	if n.Kind != yaml.SequenceNode {
		return []error{fmt.Errorf("line %d: want a list of stages", n.Line)}
	}
	var errs []error
	for i, st := range n.Content {
		if st.Kind != yaml.MappingNode || len(st.Content) != 2 || !strings.HasPrefix(st.Content[0].Value, "$") {
			errs = append(errs, fmt.Errorf("stage %d (line %d): want one key that starts with $, like {$match: {...}}", i, st.Line))
		}
	}
	walk(n, func(key *yaml.Node) {
		if writeStages[key.Value] {
			errs = append(errs, fmt.Errorf("line %d: %s writes to the database; checks only read", key.Line, key.Value))
		}
	})
	return errs
}

// walk calls f for every mapping key in the tree.
func walk(n *yaml.Node, f func(key *yaml.Node)) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			f(n.Content[i])
			walk(n.Content[i+1], f)
		}
		return
	}
	for _, child := range n.Content {
		walk(child, f)
	}
}

// Getenv reads the environment variable a source or Slack names, and says which
// one is missing when it is empty.
func Getenv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is empty", name)
	}
	return v, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
