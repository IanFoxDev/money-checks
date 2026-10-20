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
	// Warnings are what is odd in the files but not wrong enough to stop: an
	// entry of the known file for a check this configuration does not have.
	Warnings []string `yaml:"-"`
}

// Defaults apply to every check that does not set its own value.
type Defaults struct {
	Timeout time.Duration `yaml:"timeout"`
	Samples *int          `yaml:"samples"`
	// RequireExpect makes a check without expect a configuration error.
	RequireExpect bool `yaml:"require_expect"`
}

// Source is one database. Connection strings come from environment variables,
// never from the file.
type Source struct {
	MongoDB    *MongoDB    `yaml:"mongodb"`
	Postgres   *Postgres   `yaml:"postgres"`
	ClickHouse *ClickHouse `yaml:"clickhouse"`
}

// Kind is "mongodb", "postgres" or "clickhouse".
func (s Source) Kind() string {
	switch {
	case s.MongoDB != nil:
		return "mongodb"
	case s.ClickHouse != nil:
		return "clickhouse"
	default:
		return "postgres"
	}
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

// ClickHouse is read over HTTP with readonly set on every query; see
// docs/adr/0003-clickhouse.md.
type ClickHouse struct {
	URLEnv   string `yaml:"url_env"`
	Database string `yaml:"database"`
	// RequireReadOnly stops the run when the user has a grant that writes. Default true.
	RequireReadOnly *bool `yaml:"require_read_only"`
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

	// PostgreSQL and ClickHouse.
	Query     string `yaml:"query"`
	QueryFile string `yaml:"query_file"`

	ID      string        `yaml:"id"`
	Amount  *Amount       `yaml:"amount"`
	Show    []string      `yaml:"show"`
	Mask    []string      `yaml:"mask"`
	Samples *int          `yaml:"samples"`
	Timeout time.Duration `yaml:"timeout"`
	// Window, when set, lets a run read only the rows of the last Window: the
	// query gets the start of the window as $1 (postgres), $$since (mongodb) or
	// {since:DateTime64(6, 'UTC')} (clickhouse).
	Window time.Duration `yaml:"window"`
	// Every, when set, makes serve run the check every Every instead of every
	// serve.interval; a multiple of it. A full run still runs every check.
	Every time.Duration `yaml:"every"`
	// Settle, on a compare check, leaves out the rows of the last Settle on both
	// sides, which are still on their way from one to the other: the query gets
	// the end as $2 (postgres), $$until (mongodb) or {until:DateTime64(6, 'UTC')}
	// (clickhouse).
	Settle time.Duration `yaml:"settle"`

	// Expect, when set, must find data before the check runs; see
	// docs/adr/0005-expect-and-test.md.
	Expect *Expect `yaml:"expect"`

	// Compare, when set, replaces source, the query and id: the check reads
	// totals from two sides and reports the keys where they differ.
	Compare *Compare `yaml:"compare"`
}

// Compare reads one row per key from each side and reports the keys where the
// sides differ; see docs/adr/0004-compare.md.
type Compare struct {
	Left  Side     `yaml:"left"`
	Right Side     `yaml:"right"`
	Key   []string `yaml:"key"`
	// Amount is read the same way on both sides.
	Amount *Amount `yaml:"amount"`
	// Counts are integer columns that must be equal on both sides.
	Counts []string `yaml:"counts"`
	// ToleranceMinor is the difference of the amount, in minor units, that is
	// not a violation.
	ToleranceMinor int64 `yaml:"tolerance_minor"`
	// MaxRows is the most rows a side may return; more is an error of the check.
	MaxRows int `yaml:"max_rows"`
}

// Side is one side of a compare check: a query or a pipeline against a source.
type Side struct {
	Source       string    `yaml:"source"`
	Collection   string    `yaml:"collection"`
	Pipeline     yaml.Node `yaml:"pipeline"`
	PipelineFile string    `yaml:"pipeline_file"`
	Query        string    `yaml:"query"`
	QueryFile    string    `yaml:"query_file"`
	Expect       *Expect   `yaml:"expect"`
}

// Expect is a query that returns one number, such as a count of the rows the
// check reads. Below Min the check fails: it sees no data, and finding no
// violations would mean nothing.
type Expect struct {
	// Collection defaults to the collection of the check.
	Collection   string    `yaml:"collection"`
	Pipeline     yaml.Node `yaml:"pipeline"`
	PipelineFile string    `yaml:"pipeline_file"`
	Query        string    `yaml:"query"`
	QueryFile    string    `yaml:"query_file"`
	// Min is the smallest number that passes. Default 1.
	Min *int64 `yaml:"min"`
}

// Side is the check a source runs for one side of a compare check: its query,
// with the timeout and the window of ch. name is "left" or "right".
func (ch Check) Side(name string) Check {
	s := ch.Compare.Left
	if name == "right" {
		s = ch.Compare.Right
	}
	return Check{
		Name: ch.Name + " " + name, Source: s.Source, Severity: ch.Severity,
		Collection: s.Collection, Pipeline: s.Pipeline, Query: s.Query,
		Timeout: ch.Timeout, Window: ch.Window, Settle: ch.Settle, Samples: ch.Samples,
		Expect: s.Expect,
	}
}

// ExpectCheck is the check a source runs for the expect query of ch, a check or
// a side of a compare check: the query of expect with the timeout of ch, and the
// window and settle of ch only when the query uses their parameters, so a count
// that does not need the window is not given one. kind is the kind of the
// source, as Source.Kind says it.
func (ch Check) ExpectCheck(kind string) Check {
	e := ch.Expect
	ex := Check{
		Name: ch.Name + " expect", Source: ch.Source, Severity: ch.Severity,
		Collection: e.Collection, Pipeline: e.Pipeline, Query: e.Query,
		Timeout: ch.Timeout, Samples: ch.Samples,
	}
	if ex.Collection == "" {
		ex.Collection = ch.Collection
	}
	if ex.uses(kind, "since") {
		ex.Window = ch.Window
	}
	if ex.uses(kind, "until") {
		ex.Settle = ch.Settle
	}
	return ex
}

// uses says whether the query or pipeline of ch has the parameter since (the
// start of the window) or until (the end of settle).
func (ch Check) uses(kind, param string) bool {
	switch kind {
	case "mongodb":
		return usesVar(&ch.Pipeline, param)
	case "clickhouse":
		return clickhouseParam(param).MatchString(ch.Query)
	default:
		if param == "since" {
			return strings.Contains(ch.Query, "$1")
		}
		return strings.Contains(ch.Query, "$2")
	}
}

// WithoutExpect names the checks that cannot tell "no violations" from "no
// data". A compare check with an expect on one side can: when the other side
// sees nothing, every key of the first is a violation.
func (c Config) WithoutExpect() []string {
	var names []string
	for _, ch := range c.Checks {
		switch {
		case ch.Compare != nil && (ch.Compare.Left.Expect != nil || ch.Compare.Right.Expect != nil):
		case ch.Compare == nil && ch.Expect != nil:
		default:
			names = append(names, ch.Name)
		}
	}
	return names
}

// HasAmount says whether the violations of the check have an amount.
func (ch Check) HasAmount() bool {
	return ch.Amount != nil || ch.Compare != nil && ch.Compare.Amount != nil
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
	// FullEvery is how often serve reads every row of the checks with a window;
	// the runs in between read the window only.
	FullEvery time.Duration `yaml:"full_every"`
	Listen    string        `yaml:"listen"`
	Slack     *Slack        `yaml:"slack"`
	// StateFile, when set, keeps the violations serve has seen across restarts,
	// so a restart does not post them again. A relative path is relative to the
	// configuration file.
	StateFile string `yaml:"state_file"`
}

// Slack posts when a check starts or stops finding violations, or fails.
type Slack struct {
	WebhookEnv string `yaml:"webhook_env"`
}

// Defaults when neither the check nor the defaults section sets a value.
const (
	DefaultTimeout   = 30 * time.Second
	DefaultSamples   = 10
	DefaultInterval  = 10 * time.Minute
	DefaultFullEvery = 24 * time.Hour
	DefaultListen    = ":8080"
	MaxSamples       = 1000
	DefaultMaxRows   = 100000
	DefaultExpectMin = 1
)

// Load reads and checks a configuration file. pipeline_file and query_file are
// resolved against the file's directory.
func Load(path string) (Config, error) {
	return load(path, true)
}

// LoadWithoutKnown is Load without the known file, which may not exist where
// the checks are tested: known violations are a fact about one database.
func LoadWithoutKnown(path string) (Config, error) {
	return load(path, false)
}

func load(path string, known bool) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	c, err := parse(data, filepath.Dir(path), known)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse decodes and checks a configuration. Unknown keys are errors with the line
// they are on. All problems are reported at once.
func Parse(data []byte, dir string) (Config, error) {
	return parse(data, dir, true)
}

func parse(data []byte, dir string, known bool) (Config, error) {
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
	if c.Serve.StateFile != "" {
		c.Serve.StateFile = Path(dir, c.Serve.StateFile)
	}
	c.applyDefaults()
	errs = append(errs, c.check()...)
	if known {
		errs = append(errs, c.readKnown(dir)...)
	} else {
		c.KnownFile = ""
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

func (c *Config) readFiles(dir string) []error {
	var errs []error
	for i := range c.Checks {
		ch := &c.Checks[i]
		if err := readFiles(dir, &ch.Query, ch.QueryFile, &ch.Pipeline, ch.PipelineFile); err != nil {
			errs = append(errs, fmt.Errorf("check %s: %w", ch.Name, err))
		}
		if e := ch.Expect; e != nil {
			if err := readFiles(dir, &e.Query, e.QueryFile, &e.Pipeline, e.PipelineFile); err != nil {
				errs = append(errs, fmt.Errorf("check %s: expect: %w", ch.Name, err))
			}
		}
		if cmp := ch.Compare; cmp != nil {
			for _, side := range []struct {
				name string
				s    *Side
			}{{"left", &cmp.Left}, {"right", &cmp.Right}} {
				if err := readFiles(dir, &side.s.Query, side.s.QueryFile, &side.s.Pipeline, side.s.PipelineFile); err != nil {
					errs = append(errs, fmt.Errorf("check %s: compare.%s: %w", ch.Name, side.name, err))
				}
				if e := side.s.Expect; e != nil {
					if err := readFiles(dir, &e.Query, e.QueryFile, &e.Pipeline, e.PipelineFile); err != nil {
						errs = append(errs, fmt.Errorf("check %s: compare.%s.expect: %w", ch.Name, side.name, err))
					}
				}
			}
		}
	}
	return errs
}

// Path is a file the configuration names: relative to the directory of the
// configuration, or absolute.
func Path(dir, file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(dir, file)
}

// readFiles reads query_file into query and pipeline_file into pipeline.
func readFiles(dir string, query *string, queryFile string, pipeline *yaml.Node, pipelineFile string) error {
	if queryFile != "" && *query != "" {
		return errors.New("set query or query_file, not both")
	}
	if pipelineFile != "" && pipeline.Kind != 0 {
		return errors.New("set pipeline or pipeline_file, not both")
	}
	if queryFile != "" {
		b, err := os.ReadFile(Path(dir, queryFile))
		if err != nil {
			return fmt.Errorf("query_file: %w", err)
		}
		*query = string(b)
	}
	if pipelineFile != "" {
		b, err := os.ReadFile(Path(dir, pipelineFile))
		if err != nil {
			return fmt.Errorf("pipeline_file: %w", err)
		}
		// JSON is YAML, so one decoder reads both.
		var doc yaml.Node
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("pipeline_file %s: %w", pipelineFile, err)
		}
		if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
			*pipeline = *doc.Content[0]
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Defaults.Timeout == 0 {
		c.Defaults.Timeout = DefaultTimeout
	}
	if c.Defaults.Samples == nil {
		n := DefaultSamples
		c.Defaults.Samples = &n
	}
	for _, s := range c.Sources {
		if s.MongoDB != nil && s.MongoDB.RequireReadOnly == nil {
			yes := true
			s.MongoDB.RequireReadOnly = &yes
		}
		if s.ClickHouse != nil && s.ClickHouse.RequireReadOnly == nil {
			yes := true
			s.ClickHouse.RequireReadOnly = &yes
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
		if ch.Compare != nil && ch.Compare.MaxRows == 0 {
			ch.Compare.MaxRows = DefaultMaxRows
		}
		expects := []*Expect{ch.Expect}
		if ch.Compare != nil {
			expects = append(expects, ch.Compare.Left.Expect, ch.Compare.Right.Expect)
		}
		for _, e := range expects {
			if e != nil && e.Min == nil {
				one := int64(DefaultExpectMin)
				e.Min = &one
			}
		}
	}
	if c.Serve.Interval == 0 {
		c.Serve.Interval = DefaultInterval
	}
	if c.Serve.FullEvery == 0 {
		c.Serve.FullEvery = DefaultFullEvery
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
	if c.Serve.FullEvery < c.Serve.Interval {
		errs = append(errs, fmt.Errorf("serve.full_every: at least serve.interval (%s), got %s", c.Serve.Interval, c.Serve.FullEvery))
	}
	if c.Serve.Slack != nil && c.Serve.Slack.WebhookEnv == "" {
		errs = append(errs, errors.New("serve.slack.webhook_env: the name of the environment variable with the webhook URL"))
	}
	return errs
}

func (s Source) check(name string) []error {
	where := "sources." + name
	set := 0
	for _, p := range []bool{s.MongoDB != nil, s.Postgres != nil, s.ClickHouse != nil} {
		if p {
			set++
		}
	}
	switch {
	case set == 0:
		return []error{fmt.Errorf("%s: set mongodb, postgres or clickhouse", where)}
	case set > 1:
		return []error{fmt.Errorf("%s: set one of mongodb, postgres and clickhouse, not several", where)}
	case s.ClickHouse != nil:
		var errs []error
		if s.ClickHouse.URLEnv == "" {
			errs = append(errs, fmt.Errorf("%s.clickhouse.url_env: the name of the environment variable with the HTTP URL", where))
		}
		if s.ClickHouse.Database == "" {
			errs = append(errs, fmt.Errorf("%s.clickhouse.database: required", where))
		}
		return errs
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
	switch {
	case ch.Window < 0:
		add("window: must be positive")
	case ch.Window > 0 && ch.Window < c.Serve.Interval:
		// Rows written between two runs would be missed until the next full pass.
		add("window: at least serve.interval (%s), got %s", c.Serve.Interval, ch.Window)
	}
	switch every := ch.Every; {
	case every < 0:
		add("every: must be positive")
	case every > 0 && (every < c.Serve.Interval || every%c.Serve.Interval != 0):
		add("every: a multiple of serve.interval (%s), got %s", c.Serve.Interval, every)
	case every > 0 && ch.Window > 0 && ch.Window < every:
		// As for serve.interval: the rows between two runs must be in the window.
		add("window: at least every (%s), got %s", every, ch.Window)
	}
	if n := *ch.Samples; n < 0 || n > MaxSamples {
		add("samples: want 0..%d, got %d", MaxSamples, n)
	}
	switch {
	case ch.Settle < 0:
		add("settle: must be positive")
	case ch.Settle > 0 && ch.Compare == nil:
		add("settle: only a compare check has it; a check of one database sees its rows as they are written")
	case ch.Settle > 0 && ch.Window > 0 && ch.Settle >= ch.Window:
		add("settle: shorter than window (%s), got %s", ch.Window, ch.Settle)
	}
	if ch.Compare != nil {
		return append(errs, c.checkCompare(where, ch)...)
	}
	c.checkQuery(ch, true, add)
	c.checkExpect(ch, "expect", add)
	if a := ch.Amount; a != nil {
		c.checkAmount(*a, add)
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

// checkQuery checks the source and the query or pipeline of a check or of a side
// of a compare check. needID is false for a side, which has a key instead.
func (c *Config) checkQuery(ch Check, needID bool, add func(string, ...any)) {
	src, ok := c.Sources[ch.Source]
	switch {
	case ch.Source == "":
		add("source: required")
	case !ok:
		add("source: unknown %q, known: %s", ch.Source, strings.Join(sortedKeys(c.Sources), ", "))
	case src.MongoDB != nil:
		if ch.Query != "" {
			add("query is for postgres and clickhouse sources; %s is mongodb, use collection and pipeline", ch.Source)
		}
		if ch.Collection == "" {
			add("collection: required for a mongodb source")
		}
		for _, err := range checkPipeline(&ch.Pipeline) {
			add("pipeline: %w", err)
		}
		switch since := usesVar(&ch.Pipeline, "since"); {
		case ch.Window > 0 && !since:
			add("window: the pipeline must compare a time field with $$since, the start of the window, inside $expr")
		case ch.Window == 0 && since:
			add("pipeline: $$since is the start of the window; set window")
		}
		switch until := usesVar(&ch.Pipeline, "until"); {
		case ch.Settle > 0 && !until:
			add("settle: the pipeline must compare a time field with $$until, the end of the rows that have settled, inside $expr")
		case ch.Settle == 0 && until:
			add("pipeline: $$until is the end of the rows that have settled; set settle")
		}
	default:
		if ch.Collection != "" || ch.Pipeline.Kind != 0 {
			add("collection and pipeline are for mongodb sources; %s is %s, use query", ch.Source, src.Kind())
		}
		if strings.TrimSpace(ch.Query) == "" {
			add("set query or query_file")
		}
		if needID && ch.ID == "" {
			add("id: the column that identifies a violating row")
		}
		switch since := clickhouseParam("since").MatchString(ch.Query); {
		case src.Postgres != nil && ch.Window > 0 && !strings.Contains(ch.Query, "$1"):
			add("window: the query must compare a time column with $1, the start of the window")
		case src.ClickHouse != nil && ch.Window > 0 && !since:
			add("window: the query must compare a time column with {since:DateTime64(6, 'UTC')}, the start of the window")
		case src.ClickHouse != nil && ch.Window == 0 && since:
			add("query: {since:...} is the start of the window; set window")
		}
		switch until := clickhouseParam("until").MatchString(ch.Query); {
		case src.Postgres != nil && ch.Settle > 0 && (!strings.Contains(ch.Query, "$2") || !strings.Contains(ch.Query, "$1")):
			// PostgreSQL cannot type $2 when $1 is not used.
			add("settle: the query must compare a time column with $2, the end of the rows that have settled, and with $1, the start of the window (0001-01-01 without a window)")
		case src.Postgres != nil && ch.Settle == 0 && strings.Contains(ch.Query, "$2"):
			add("query: $2 is the end of the rows that have settled; set settle")
		case src.ClickHouse != nil && ch.Settle > 0 && !until:
			add("settle: the query must compare a time column with {until:DateTime64(6, 'UTC')}, the end of the rows that have settled")
		case src.ClickHouse != nil && ch.Settle == 0 && until:
			add("query: {until:...} is the end of the rows that have settled; set settle")
		}
		if src.ClickHouse != nil {
			for _, name := range []string{"since", "until"} {
				for _, m := range clickhouseParamType(name).FindAllStringSubmatch(ch.Query, -1) {
					if strings.Join(strings.Fields(m[1]), "") != "DateTime64(6,'UTC')" {
						add("query: write {%s:DateTime64(6, 'UTC')}, not {%s:%s}: another type drops the microseconds or reads the time in the server time zone", name, name, strings.TrimSpace(m[1]))
					}
				}
			}
		}
	}
}

// checkExpect checks the expect query of a check or of a side, ch. where is
// how the errors name it.
func (c *Config) checkExpect(ch Check, where string, add func(string, ...any)) {
	e := ch.Expect
	if e == nil {
		if c.Defaults.RequireExpect && ch.Compare == nil && where == "expect" {
			add("expect: required by defaults.require_expect; a query that must find data, such as a count of the rows the check reads")
		}
		return
	}
	if *e.Min < 0 {
		add("%s.min: must be 0 or more", where)
	}
	src, ok := c.Sources[ch.Source]
	if !ok {
		return // checkQuery says so
	}
	kind := src.Kind()
	ex := ch.ExpectCheck(kind)
	in := func(format string, a ...any) { add(where+": "+format, a...) }
	if kind == "mongodb" && e.Collection == "" && ch.Collection == "" {
		return // checkQuery of the check says that collection is required
	}
	c.checkQuery(ex, false, in)
	if kind != "postgres" {
		return
	}
	// The window and settle of the check are given to the expect query only
	// when it uses them, so a $1 the check cannot give is an error here, not a
	// failed run.
	switch since, until := ex.uses(kind, "since"), ex.uses(kind, "until"); {
	case since && ch.Window == 0 && ch.Settle == 0:
		in("query: $1 is the start of the window; the check has no window")
	case since && ch.Window == 0 && !until:
		in("query: $1 without a window is given only with $2, the end of the rows that have settled")
	}
}

func (c *Config) checkAmount(a Amount, add func(string, ...any)) {
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

// checkCompare checks a compare check. Its sides take the place of source, the
// query and id; its rows are totals, so show and mask do not apply.
func (c *Config) checkCompare(where string, ch Check) []error {
	var errs []error
	add := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf(where+": "+format, a...))
	}
	cmp := ch.Compare
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"source", ch.Source != ""}, {"collection", ch.Collection != ""}, {"pipeline", ch.Pipeline.Kind != 0},
		{"query", ch.Query != ""}, {"id", ch.ID != ""}, {"amount", ch.Amount != nil},
		{"show", len(ch.Show) > 0}, {"mask", len(ch.Mask) > 0},
	} {
		if f.set {
			add("%s: a compare check sets it under compare or on each side, not on the check", f.name)
		}
	}
	if ch.Expect != nil {
		add("expect: a compare check sets it on each side, compare.left.expect and compare.right.expect")
	}
	if c.Defaults.RequireExpect && cmp.Left.Expect == nil && cmp.Right.Expect == nil {
		add("expect: defaults.require_expect is set; give compare.left or compare.right an expect query")
	}
	for _, name := range []string{"left", "right"} {
		side := ch.Side(name)
		c.checkQuery(side, false, func(format string, a ...any) {
			add("compare."+name+": "+format, a...)
		})
		c.checkExpect(side, "compare."+name+".expect", add)
	}
	key := map[string]bool{}
	if len(cmp.Key) == 0 {
		add("compare.key: the columns both sides group by, such as [day, currency]")
	}
	for _, k := range cmp.Key {
		if key[k] {
			add("compare.key: %q is listed twice", k)
		}
		key[k] = true
	}
	if a := cmp.Amount; a != nil {
		c.checkAmount(*a, func(format string, args ...any) { add("compare."+format, args...) })
		if a.CurrencyField != "" && !key[a.CurrencyField] {
			add("compare.amount.currency_field: %q must be in key, so both amounts of a key are in one currency", a.CurrencyField)
		}
		if key[a.Field] {
			add("compare.amount.field: %q is in key", a.Field)
		}
	}
	counts := map[string]bool{}
	for _, n := range cmp.Counts {
		switch {
		case key[n]:
			add("compare.counts: %q is in key", n)
		case counts[n]:
			add("compare.counts: %q is listed twice", n)
		case cmp.Amount != nil && n == cmp.Amount.Field:
			add("compare.counts: %q is the amount", n)
		}
		counts[n] = true
	}
	if cmp.ToleranceMinor < 0 {
		add("compare.tolerance_minor: must be 0 or more")
	}
	if cmp.MaxRows < 0 {
		add("compare.max_rows: must be positive")
	}
	return errs
}

// clickhouseParam finds a parameter, since or until, in a ClickHouse query.
func clickhouseParam(name string) *regexp.Regexp {
	return regexp.MustCompile(`\{\s*` + name + `\s*:`)
}

// clickhouseParamType finds the types the query gives a parameter.
func clickhouseParamType(name string) *regexp.Regexp {
	return regexp.MustCompile(`\{\s*` + name + `\s*:([^}]*)\}`)
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

// usesVar says whether a pipeline uses the variable $$name of money-checks.
// Inside a stage or an operator that binds a variable of that name, such as
// let: {since: ...} of $lookup, $$since is that variable, not the parameter:
// the pipeline of the $lookup, the in of $let, the in or cond of $map, $filter
// and $reduce do not count. The values bound still do, they are outside.
func usesVar(n *yaml.Node, name string) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value == "$$"+name || strings.HasPrefix(n.Value, "$$"+name+".")
	case yaml.MappingNode:
		bound := false
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i].Value, n.Content[i+1]
			if (k == "let" || k == "vars") && v.Kind == yaml.MappingNode && hasKey(v, name) || k == "as" && v.Value == name {
				bound = true
			}
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if bound && (k == "pipeline" || k == "in" || k == "cond") {
				continue
			}
			if usesVar(n.Content[i+1], name) {
				return true
			}
		}
		return false
	}
	for _, child := range n.Content {
		if usesVar(child, name) {
			return true
		}
	}
	return false
}

func hasKey(n *yaml.Node, key string) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
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
