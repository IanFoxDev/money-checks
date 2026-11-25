package serve

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/history"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// clocked is a server whose clock the test moves, with a history file.
func clocked(t *testing.T, src *script) (*Server, *notes, *time.Time, string) {
	t.Helper()
	cfg, err := config.Parse([]byte(conf), ".")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Serve.HistoryFile = filepath.Join(t.TempDir(), "history.jsonl")
	at := time.Unix(1791400000, 0).UTC()
	n := &notes{}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	s := New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return at })
	return s, n, &at, cfg.Serve.HistoryFile
}

func events(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	es, err := history.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Event+" "+e.ID)
	}
	return out
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n%s\nwant:\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A violation fixed by a full run and found again is regressed, with its own
// line, its own metric and its own event; after keepFixed it is new again.
func TestRegressed(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD"), r("u2", 500, "USD")},
		{r("u2", 500, "USD")},                                         // u1 fixed
		{r("u1", 999, "USD"), r("u2", 500, "USD")},                    // u1 back after 6 days
		{r("u2", 500, "USD")},                                         // u1 fixed again
		{r("u1", 999, "USD"), r("u2", 500, "USD"), r("u3", 1, "USD")}, // u1 back after 91 days: new
	}}
	s, n, at, path := clocked(t, src)
	t0 := *at
	for _, d := range []time.Duration{0, time.Hour, 6*24*time.Hour + time.Hour, 7 * 24 * time.Hour, 98 * 24 * time.Hour} {
		*at = t0.Add(d)
		s.Cycle(context.Background())
	}
	equal(t, "messages", n.msgs, []string{
		"money-checks: two_active_subscriptions found 2 violations (14.99 USD) [error]",
		"money-checks: two_active_subscriptions: 1 violation fixed (9.99 USD), 1 left [error]",
		"money-checks: two_active_subscriptions: 1 violation came back (9.99 USD), fixed 6 days ago, 2 in all [error]",
		"money-checks: two_active_subscriptions: 1 violation fixed (9.99 USD), 1 left [error]",
		"money-checks: two_active_subscriptions found 2 new violations (10.00 USD), 3 in all [error]",
	})
	equal(t, "events", events(t, path), []string{
		"baseline u1", "baseline u2",
		"fixed u1",
		"regressed u1",
		"fixed u1",
		"new u1", "new u3",
	})
	has(t, scrape(t, s),
		`money_check_regressed_violations_total{check="two_active_subscriptions"} 1`,
		`money_check_new_violations_total{check="two_active_subscriptions"} 2`)
}

// New and regressed in one run are two lines; a check that was clean and finds
// only a violation that came back says so, not that it found violations.
func TestRegressedWithNew(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 100, "USD")},
		{},
		{r("u1", 100, "USD")},
		{},
		{r("u1", 100, "USD"), r("u2", 200, "USD")},
	}}
	s, n, at, _ := clocked(t, src)
	t0 := *at
	for i := range src.runs {
		*at = t0.Add(time.Duration(i) * 3 * time.Hour)
		s.Cycle(context.Background())
	}
	equal(t, "messages", n.msgs, []string{
		"money-checks: two_active_subscriptions found 1 violation (1.00 USD) [error]",
		"money-checks: two_active_subscriptions no longer finds violations",
		"money-checks: two_active_subscriptions: 1 violation came back (1.00 USD), fixed 3 hours ago, 1 in all [error]",
		"money-checks: two_active_subscriptions no longer finds violations",
		"money-checks: 1 check changed\ntwo_active_subscriptions found 1 new violation (2.00 USD), 2 in all [error]\ntwo_active_subscriptions: 1 violation came back (1.00 USD), fixed 3 hours ago, 2 in all [error]",
	})
}

// The first full run after a change of the query is a baseline: one line, the
// rows both versions find keep their age, and nothing is fixed or new.
func TestChangedCheck(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 100, "USD"), r("u2", 200, "USD")},
		{r("u2", 200, "USD"), r("u3", 300, "USD"), r("u4", 400, "USD")}, // changed query
		{r("u2", 200, "USD"), r("u3", 300, "USD"), r("u4", 400, "USD")}, // changed again, same rows
		{r("u2", 200, "USD"), r("u4", 400, "USD"), r("u5", 1, "USD")},   // as usual
	}}
	s, n, at, path := clocked(t, src)
	t0 := *at
	for i, q := range []string{"q", "q2", "q3", "q3"} {
		*at = t0.Add(time.Duration(i) * time.Hour)
		s.cfg.Checks[0].Query = q
		s.Cycle(context.Background())
	}
	equal(t, "messages", n.msgs, []string{
		"money-checks: two_active_subscriptions found 2 violations (3.00 USD) [error]",
		"money-checks: two_active_subscriptions changed: 3 violations, 2 found for the first time, 1 no longer found [error]",
		"money-checks: two_active_subscriptions found 1 new violation (0.01 USD), 1 fixed (3.00 USD), 3 in all [error]",
	})
	equal(t, "events", events(t, path), []string{
		"baseline u1", "baseline u2",
		"dropped u1", "baseline u3", "baseline u4",
		"fixed u3", "new u5",
	})
	st := s.states["two_active_subscriptions"]
	if !st.seen["u2"].first.Equal(t0) || !st.seen["u4"].first.Equal(t0.Add(time.Hour)) {
		t.Errorf("first seen %+v", st.seen)
	}
	if _, ok := st.fixed["u1"]; ok {
		t.Error("a dropped row is not fixed")
	}
	has(t, scrape(t, s), `money_check_new_violations_total{check="two_active_subscriptions"} 1`)
}

// A window run of a changed check waits for the full run: it cannot see what
// the change dropped.
func TestChangedCheckWaitsForFullRun(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 100, "USD")},
		{r("u1", 100, "USD"), r("u2", 200, "USD")}, // window, changed query
		{r("u2", 200, "USD")},                      // full
	}}
	s, n, at, path := clocked(t, src)
	s.cfg.Checks[0].Query, s.cfg.Checks[0].Window, s.cfg.Serve.FullEvery = "select $1", time.Hour, 2*time.Hour
	t0 := *at
	for i, q := range []string{"select $1", "select $1, 2", "select $1, 2"} {
		*at = t0.Add(time.Duration(i) * time.Hour)
		s.cfg.Checks[0].Query = q
		s.Cycle(context.Background())
	}
	equal(t, "messages", n.msgs, []string{
		"money-checks: two_active_subscriptions found 1 violation (1.00 USD) [error]",
		"money-checks: two_active_subscriptions changed: 1 violation, 1 found for the first time, 1 no longer found [error]",
	})
	equal(t, "events", events(t, path), []string{"baseline u1", "dropped u1", "baseline u2"})
}

// The state file keeps the version and the fixed ids over a restart, and one of
// version 1 is read with neither: the check is taken as unchanged.
func TestStateFileVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, _ := serverWithState(t, path, &script{runs: [][]source.Row{{r("u1", 999, "USD"), r("u2", 1, "USD")}, {r("u2", 1, "USD")}}})
	first.Cycle(context.Background())
	first.Cycle(context.Background())
	data, _ := os.ReadFile(path)
	for _, want := range []string{`"version":2`, `"version":"` + first.cfg.CheckVersion(first.cfg.Checks[0]) + `"`, `"fixed":{"u1":{"amount":{"currency":"USD","minor":999,"exponent":2},"first_seen":`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %s in\n%s", want, data)
		}
	}
	again, n := serverWithState(t, path, &script{runs: [][]source.Row{{r("u1", 999, "USD"), r("u2", 1, "USD")}}})
	again.Cycle(context.Background())
	equal(t, "after a restart", n.msgs, []string{"money-checks: two_active_subscriptions: 1 violation came back (9.99 USD), fixed less than an hour ago, 2 in all [error]"})

	v1 := `{"version": 1, "checks": {"two_active_subscriptions": {"complete": true, "found": {"full": true}, "violations": {"u1": {"amount": {"currency": "USD", "minor": 999, "exponent": 2}, "first_seen": "2026-10-07T00:00:00Z"}}}}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	old, n := serverWithState(t, path, &script{runs: [][]source.Row{{r("u1", 999, "USD"), r("u2", 1, "USD")}}})
	old.Cycle(context.Background())
	equal(t, "from version 1", n.msgs, []string{"money-checks: two_active_subscriptions found 1 new violation (0.01 USD), 2 in all [error]"})
}

// A history file that does not exist yet starts with a baseline of what the
// state file holds; one that exists is appended to.
func TestHistoryStartsFromState(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	first, _ := serverWithState(t, state, &script{runs: [][]source.Row{{r("u2", 1, "USD"), r("u1", 999, "USD")}}})
	first.Cycle(context.Background())

	s, _ := serverWithState(t, state, &script{runs: [][]source.Row{{r("u1", 999, "USD"), r("u3", 5, "USD")}}})
	s.cfg.Serve.HistoryFile = filepath.Join(dir, "history.jsonl")
	if err := s.startHistory(); err != nil {
		t.Fatal(err)
	}
	s.Cycle(context.Background())
	if err := s.startHistory(); err != nil {
		t.Fatal(err)
	}
	equal(t, "events", events(t, s.cfg.Serve.HistoryFile), []string{"baseline u1", "baseline u2", "fixed u2", "new u3"})
	info, err := os.Stat(s.cfg.Serve.HistoryFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", info, err)
	}
	data, _ := os.ReadFile(s.cfg.Serve.HistoryFile)
	if line := strings.SplitN(string(data), "\n", 2)[0]; !strings.HasPrefix(line, `{"time":"2026-10-07T`) || !strings.Contains(line, `"scope":"full","amount":{"currency":"USD","minor":999,"exponent":2},"version":"`) {
		t.Errorf("first line %s", line)
	}
}
