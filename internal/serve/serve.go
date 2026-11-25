// Package serve runs the checks on a schedule, exposes the results as Prometheus
// metrics and posts to Slack when a check changes state.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/history"
)

// Server holds the state between runs.
type Server struct {
	cfg     config.Config
	open    check.Opener
	now     func() time.Time
	log     *slog.Logger
	notify  Notifier
	metrics *metrics
	reg     *prometheus.Registry
	states  map[string]*state
	// wait is, per check with every, the runs of serve it still sits out.
	wait map[string]int
	// lastFull is when the last full run started; zero before the first.
	lastFull time.Time
	// down are the sources that could not be reached on the last run that used
	// them.
	down map[string]bool
	// pending are the checks that did not run because their source was down,
	// with the scope they would have had; retry is the wait before the next
	// try, doubled after each miss.
	pending map[string]check.Scope
	retry   time.Duration
	// warned are the warnings of the sources already logged.
	warned map[string]bool
	ready  atomic.Bool
}

// maxTracked is the most violations serve remembers for one check. Above it the
// check is news only when it changes state. See
// docs/adr/0006-serve-remembers-violations.md.
const maxTracked = 100000

// state is what the last runs of a check found, to tell what is news.
type state struct {
	failed bool
	// found is per scope: a window run does not see what a full run found
	// before the window, and must not report it as gone.
	found map[check.Scope]bool
	// seen are the violations by id when complete: every violation of the last
	// full run and the new ones of the window runs since. Before the first full
	// run, and when a check finds more than maxTracked, it is not complete and
	// the check is news only when its state changes.
	seen     map[string]violation
	complete bool
	// version is the version of the check that made the last full run, "" before
	// it or in a state file written before versions.
	version string
	// fixed are the ids that a full run stopped finding in the last keepFixed,
	// so an id found again is regressed, not new.
	fixed map[string]fixedViolation
}

// keepFixed is how long serve remembers a fixed violation. See
// docs/adr/0010-violation-history.md.
const keepFixed = 90 * 24 * time.Hour

// fixedViolation is what serve remembers of a fixed violation.
type fixedViolation struct {
	amount *check.Money
	first  time.Time
	at     time.Time
}

// outcome is what one run of a check changed: the lines to post, the ids that
// are new or changed, the ids that came back and the events for the history.
type outcome struct {
	lines     []string
	added     []string
	regressed int
	events    []history.Event
}

// violation is what serve remembers of one violation.
type violation struct {
	amount *check.Money
	// first is when serve first saw the id; it stays when the amount changes.
	first time.Time
}

// rows are the violations one run of a check found, by id, without the known
// ones; over says there were more than maxTracked.
type rows struct {
	ids  map[string]*check.Money
	over bool
}

// New builds a server. notify may be nil, then state changes are only logged.
func New(cfg config.Config, open check.Opener, notify Notifier, log *slog.Logger, now func() time.Time) *Server {
	reg := prometheus.NewRegistry()
	return &Server{
		cfg: cfg, open: open, now: now, log: log, notify: notify,
		metrics: newMetrics(reg, cfg), reg: reg, states: map[string]*state{}, wait: map[string]int{}, down: map[string]bool{}, pending: map[string]check.Scope{}, warned: map[string]bool{},
	}
}

// Handler serves /metrics, /healthz (the process is alive) and /readyz (a first
// run has finished, so the metrics mean something).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "the first run has not finished", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

// Cycle runs the checks that are due, updates the metrics and notifies about
// changes. The first run and then one every serve.full_every reads every row of
// every check; the runs in between read the window of the checks that have one,
// and skip a check with every until its turn.
func (s *Server) Cycle(ctx context.Context) check.Report {
	scope := check.Window
	if start := s.now(); s.lastFull.IsZero() || start.Sub(s.lastFull) >= s.cfg.Serve.FullEvery {
		scope, s.lastFull = check.Full, start
	}
	cfg := s.cfg
	cfg.Checks = nil
	for _, ch := range s.cfg.Checks {
		if ch.Every == 0 {
			cfg.Checks = append(cfg.Checks, ch)
			continue
		}
		// A full run runs every check, so a heavy check is read in full every
		// full_every whatever its turn; its turns count from there.
		if scope == check.Window && s.wait[ch.Name] > 0 {
			s.wait[ch.Name]--
			continue
		}
		s.wait[ch.Name] = int(ch.Every/s.cfg.Serve.Interval) - 1
		cfg.Checks = append(cfg.Checks, ch)
	}
	return s.run(ctx, cfg, scope)
}

// Retry runs again the checks that did not run because their source was down,
// without moving the schedule of the other runs. A check that would have read
// every row reads every row now. See docs/adr/0007-source-down-one-message.md.
func (s *Server) Retry(ctx context.Context) check.Report {
	cfg := s.cfg
	cfg.Checks = nil
	scope := check.Window
	for _, ch := range s.cfg.Checks {
		if sc, ok := s.pending[ch.Name]; ok {
			cfg.Checks = append(cfg.Checks, ch)
			if sc == check.Full {
				scope = check.Full
			}
		}
	}
	return s.run(ctx, cfg, scope)
}

// RetryAfter is how soon to call Retry, or 0 when nothing waits for its source
// or the next run comes as soon.
func (s *Server) RetryAfter() time.Duration {
	if len(s.pending) == 0 || s.retry >= s.cfg.Serve.Interval {
		return 0
	}
	return s.retry
}

// run runs the checks of cfg in scope, updates the metrics and notifies.
func (s *Server) run(ctx context.Context, cfg config.Config, scope check.Scope) check.Report {
	found := map[string]*rows{}
	r := check.RunExport(ctx, cfg, scope, s.open, s.now, func(name string, v check.Sample) error {
		if v.Reason != "" {
			return nil // known
		}
		rs := found[name]
		if rs == nil {
			rs = &rows{ids: map[string]*check.Money{}}
			found[name] = rs
		}
		if _, ok := rs.ids[v.ID]; !ok && len(rs.ids) >= maxTracked {
			rs.over = true
		}
		if !rs.over {
			rs.ids[v.ID] = v.Amount
		}
		return nil
	})
	// A source opens on every run and warns the same way each time: once is
	// enough for the log.
	for _, w := range r.Warnings {
		if !s.warned[w] {
			s.warned[w] = true
			s.log.Warn(w)
		}
	}
	s.metrics.runs.Inc()
	var lines []string
	var events []history.Event
	changed := 0
	for i, res := range r.Results {
		if res.SourceDown != "" {
			if s.pending[res.Name] != check.Full {
				s.pending[res.Name] = res.Scope
			}
			// Its violations are what they were; the line of the source says why
			// it did not run. See docs/adr/0007-source-down-one-message.md.
			s.metrics.update(res, s.now())
			s.log.Info("check finished", "check", res.Name, "scope", string(res.Scope), "status", res.Status, "duration", res.Duration.String(), "error", res.Error)
			continue
		}
		delete(s.pending, res.Name)
		s.metrics.update(res, s.now())
		st := s.states[res.Name]
		if st == nil {
			st = &state{found: map[check.Scope]bool{}}
			s.states[res.Name] = st
		}
		attrs := []any{"check", res.Name, "scope", string(res.Scope), "status", res.Status, "violations", res.Violations, "duration", res.Duration.String()}
		if res.Error != "" {
			attrs = append(attrs, "error", res.Error)
		}
		s.log.Info("check finished", attrs...)
		got := found[res.Name]
		if got != nil && got.over && st.complete {
			s.log.Warn("too many violations to tell the new ones; the check is news only when its state changes", "check", res.Name, "limit", maxTracked)
		}
		out := st.change(res, got, s.cfg.CheckVersion(cfg.Checks[i]), s.now())
		if res.Status != check.StatusFailed {
			s.metrics.track(res.Name, st, len(out.added), out.regressed, s.now())
		}
		if len(out.added) > 0 {
			s.log.Info("new violations", "check", res.Name, "count", len(out.added), "ids", first(out.added, *cfg.Checks[i].Samples))
		}
		for _, msg := range out.lines {
			s.log.Info("state changed", "check", res.Name, "message", msg)
		}
		if len(out.lines) > 0 {
			changed++
		}
		lines = append(lines, out.lines...)
		events = append(events, out.events...)
	}
	// Before the state file: a crash between the two writes the events again,
	// which the reader takes as one, instead of losing them.
	if path := s.cfg.Serve.HistoryFile; path != "" && len(events) > 0 {
		if err := history.Append(path, events); err != nil {
			s.log.Error("the history file cannot be written", "file", path, "error", err)
		}
	}
	news := s.sources(r.Results)
	if len(news.lines)+len(lines) > 0 {
		msg := message(news, lines, changed)
		s.log.Info("message", "lines", len(news.lines)+len(lines), "text", msg)
		if s.notify != nil {
			if err := s.notify.Notify(ctx, msg); err != nil {
				s.log.Error("notification failed", "error", err)
			}
		}
	}
	if s.cfg.Serve.StateFile != "" {
		if err := s.save(); err != nil {
			s.log.Error("the state file cannot be written; a restart will post every check that is not clean", "file", s.cfg.Serve.StateFile, "error", err)
		}
	}
	switch {
	case len(s.pending) == 0:
		s.retry = 0
	case s.retry == 0:
		s.retry = s.cfg.Serve.Retry
	default:
		s.retry = min(2*s.retry, s.cfg.Serve.Interval)
	}
	s.ready.Store(true)
	return r
}

// Run serves HTTP on the configured address and runs the checks now and then
// every interval, until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	// A state file that cannot be written is a mistake in the deployment, such as
	// a read-only volume: better to say so now than to post everything after the
	// next restart.
	if path := s.cfg.Serve.StateFile; path != "" {
		s.load()
		if err := s.save(); err != nil {
			return fmt.Errorf("serve.state_file: %w", err)
		}
	}
	if path := s.cfg.Serve.HistoryFile; path != "" {
		if err := s.startHistory(); err != nil {
			return fmt.Errorf("serve.history_file: %w", err)
		}
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Serve.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("serving", "listen", ln.Addr().String(), "interval", s.cfg.Serve.Interval.String(), "checks", len(s.cfg.Checks))
	for _, w := range s.cfg.Warnings {
		s.log.Warn(w)
	}
	if names := s.cfg.WithoutExpect(); len(names) > 0 {
		s.log.Warn(check.WithoutExpectWarning(names))
	}

	tick := time.NewTicker(s.cfg.Serve.Interval)
	defer tick.Stop()
	run := s.Cycle
	for {
		run(ctx)
		var retry <-chan time.Time
		if d := s.RetryAfter(); d > 0 {
			retry = time.After(d)
		}
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
			return nil
		case err := <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-tick.C:
			run = s.Cycle
		case <-retry:
			run = s.Retry
		}
	}
}

// change records res and says what to post about it, which ids are new and
// what to write to the history. The first run only reports problems; a check
// that is fine from the start is not news. Then every new violation is news, an
// id not seen before or one whose amount changed, also when the check already
// had violations, and an id fixed before that is found again has a line of its
// own. Only a full run says that violations are gone: a row that leaves the
// window is not fixed. The first full run of a changed check is a baseline:
// one line says what the change did. See docs/adr/0010-violation-history.md.
func (st *state) change(res check.Result, got *rows, version string, now time.Time) outcome {
	if res.Status == check.StatusFailed {
		if st.failed {
			return outcome{}
		}
		st.failed = true
		return outcome{lines: []string{res.Name + " failed: " + cut(res.Error)}}
	}
	wasFailed, had := st.failed, st.found[res.Scope]
	found := res.Status == check.StatusViolations
	st.failed, st.found[res.Scope] = false, found
	if got == nil {
		got = &rows{}
	}
	if got.over {
		st.seen, st.complete, st.fixed = nil, false, nil
		return outcome{lines: lines(stateChange(res, wasFailed, had, found))}
	}
	event := func(id, kind string, a *check.Money) history.Event {
		return history.Event{Time: now, Check: res.Name, ID: id, Event: kind, Scope: string(res.Scope), Amount: amount(a), Version: version}
	}
	for id, f := range st.fixed {
		if now.Sub(f.at) > keepFixed {
			delete(st.fixed, id)
		}
	}
	if st.complete && st.version == "" {
		// A state file from before versions: the check is taken as unchanged.
		st.version = version
	}
	if st.complete && st.version != version {
		if res.Scope == check.Window {
			// A window cannot tell what the change dropped: wait for a full run.
			return outcome{}
		}
		return st.baseline(res, got, version, now, event)
	}

	var out outcome
	var added []string
	var addedSums, fixedSums, backSums []check.Money
	var back []string
	for id, a := range got.ids {
		v, ok := st.seen[id]
		switch {
		case ok && same(v.amount, a):
			continue
		case ok:
			out.events = append(out.events, event(id, history.Changed, a))
		case !st.fixed[id].at.IsZero():
			back = append(back, id)
			if a != nil {
				backSums = append(backSums, *a)
			}
			continue
		default:
			out.events = append(out.events, event(id, history.New, a))
		}
		added = append(added, id)
		if a != nil {
			addedSums = append(addedSums, *a)
		}
	}
	sort.Strings(added)
	sort.Strings(back)
	var since, until time.Time // of the fixes that came back
	for _, id := range back {
		f := st.fixed[id]
		if since.IsZero() || f.at.Before(since) {
			since = f.at
		}
		if f.at.After(until) {
			until = f.at
		}
		delete(st.fixed, id)
		out.events = append(out.events, event(id, history.Regressed, got.ids[id]))
	}
	next := st.seen
	if res.Scope == check.Full || next == nil {
		next = map[string]violation{}
	}
	for id, a := range got.ids {
		v, ok := st.seen[id]
		if !ok {
			v.first = now
		}
		v.amount = a
		next[id] = v
	}
	fixed := 0
	if res.Scope == check.Full {
		for id, v := range st.seen {
			if _, ok := got.ids[id]; !ok {
				fixed++
				if v.amount != nil {
					fixedSums = append(fixedSums, *v.amount)
				}
				if st.complete {
					if st.fixed == nil {
						st.fixed = map[string]fixedViolation{}
					}
					st.fixed[id] = fixedViolation{amount: v.amount, first: v.first, at: now}
					out.events = append(out.events, event(id, history.Fixed, v.amount))
				}
			}
		}
	}
	complete := st.complete
	st.seen, st.complete = next, complete || res.Scope == check.Full
	if !complete {
		// The first full picture: every violation is found in old data, not
		// today, and none is news.
		out.events = nil
		if res.Scope == check.Full {
			st.version = version
			ids := make([]string, 0, len(got.ids))
			for id := range got.ids {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				out.events = append(out.events, event(id, history.Baseline, got.ids[id]))
			}
		}
		out.lines = lines(stateChange(res, wasFailed, had, found))
		return out
	}
	sortEvents(out.events)
	out.added, out.regressed = added, len(back)
	backLine := ""
	if len(back) > 0 {
		backLine = cameBack(res, len(back), backSums, now.Sub(until), now.Sub(since))
	}

	// When every violation is new, the state change says it all. After a
	// failure the violations seen before are still known, so only what changed
	// meanwhile is news. See docs/adr/0007-source-down-one-message.md.
	var line string
	switch {
	case !wasFailed && len(added) == int(res.Violations) && fixed == 0:
		line = stateChange(res, wasFailed, had, found)
	case wasFailed:
		switch {
		case !found:
			line = res.Name + " runs again and finds no violations"
		case len(added) == 0 && fixed == 0 && len(back) > 0:
		case len(added) == 0 && fixed == 0:
			line = res.Name + " runs again, no new " + noun(res, 2)
			if res.Scope == check.Window {
				line += " in the last " + check.Span(res.Window) + " [" + res.Severity + "]"
			} else {
				line += ", " + itoa(res.Violations) + " in all [" + res.Severity + "]"
			}
		default:
			line = news(res, added, addedSums, fixed, fixedSums)
			if rest, ok := strings.CutPrefix(line, res.Name+":"); ok {
				line = res.Name + " runs again:" + rest
			} else {
				line = res.Name + " runs again and" + strings.TrimPrefix(line, res.Name)
			}
		}
	case len(added) == 0 && fixed == 0:
	case !found:
		line = res.Name + " no longer finds violations"
	default:
		line = news(res, added, addedSums, fixed, fixedSums)
	}
	out.lines = lines(line, backLine)
	return out
}

// baseline is the first full run of a changed check: the rows it finds that the
// old version did not are found in old data, the rows it no longer finds are
// dropped, not fixed, and one line says what the change did, or none when it
// changed no row. The rows both versions find keep their first seen.
func (st *state) baseline(res check.Result, got *rows, version string, now time.Time, event func(string, string, *check.Money) history.Event) outcome {
	var out outcome
	next := make(map[string]violation, len(got.ids))
	for id, a := range got.ids {
		v, ok := st.seen[id]
		if !ok {
			v.first = now
			out.events = append(out.events, event(id, history.Baseline, a))
		}
		v.amount = a
		next[id] = v
		delete(st.fixed, id)
	}
	firstTime := len(out.events)
	for id, v := range st.seen {
		if _, ok := got.ids[id]; !ok {
			out.events = append(out.events, event(id, history.Dropped, v.amount))
		}
	}
	dropped := len(out.events) - firstTime
	sortEvents(out.events)
	st.seen, st.version = next, version
	if firstTime+dropped > 0 {
		out.lines = []string{res.Name + " changed: " + itoa(res.Violations) + " " + noun(res, int(res.Violations)) +
			", " + itoa(int64(firstTime)) + " found for the first time, " + itoa(int64(dropped)) + " no longer found [" + res.Severity + "]"}
	}
	return out
}

// cameBack is the line about violations that were fixed and are found again,
// with how long ago they were fixed.
func cameBack(res check.Result, n int, ms []check.Money, latest, earliest time.Duration) string {
	msg := res.Name + ": " + itoa(int64(n)) + " " + noun(res, n) + " came back" + sums(ms, res.RightSource != "") + ", fixed "
	if a, b := ago(latest), ago(earliest); a != b {
		msg += a + " to " + b + " ago"
	} else {
		msg += a + " ago"
	}
	if res.Scope == check.Full {
		msg += ", " + itoa(res.Violations) + " in all"
	}
	return msg + " [" + res.Severity + "]"
}

// ago is a duration as a message says it: hours up to two days, then days.
func ago(d time.Duration) string {
	switch h := int64(d / time.Hour); {
	case h < 1:
		return "less than an hour"
	case h < 48:
		return itoa(h) + " " + plural(int(h), "hour", "hours")
	default:
		return itoa(h/24) + " days"
	}
}

// lines are the lines that are not empty.
func lines(ls ...string) []string {
	var out []string
	for _, l := range ls {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// sortEvents puts the events of one run in the order of their ids, so the
// history of the same run reads the same way every time.
func sortEvents(es []history.Event) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].ID < es[j].ID })
}

func amount(m *check.Money) *history.Amount {
	if m == nil {
		return nil
	}
	return &history.Amount{Currency: m.Currency, Minor: m.Minor, Exponent: m.Exponent}
}

// news is the line about the violations a check found new and the ones it no
// longer finds, with their totals.
func news(res check.Result, added []string, addedSums []check.Money, fixed int, fixedSums []check.Money) string {
	compare := res.RightSource != ""
	msg := res.Name
	if len(added) > 0 {
		msg += " found " + itoa(int64(len(added))) + " new " + noun(res, len(added)) + sums(addedSums, compare)
	} else {
		msg += ":"
	}
	switch {
	case fixed > 0 && len(added) > 0:
		msg += ", " + itoa(int64(fixed)) + " fixed" + sums(fixedSums, compare)
	case fixed > 0 && compare:
		msg += " " + itoa(int64(fixed)) + " " + plural(fixed, "key no longer differs", "keys no longer differ") + sums(fixedSums, compare)
	case fixed > 0:
		msg += " " + itoa(int64(fixed)) + " " + plural(fixed, "violation", "violations") + " fixed" + sums(fixedSums, compare)
	}
	switch {
	case res.Scope == check.Window:
		msg += " in the last " + check.Span(res.Window)
	case len(added) > 0:
		msg += ", " + itoa(res.Violations) + " in all"
	default:
		msg += ", " + itoa(res.Violations) + " left"
	}
	return msg + " [" + res.Severity + "]"
}

// sourceNews are the lines about sources that went down or came back.
type sourceNews struct {
	lines    []string
	down, up int
}

// sources says which sources went down or came back in this run, from its
// results, and updates money_check_source_up. A source is up when a check that
// uses it got past opening it.
func (s *Server) sources(results []check.Result) sourceNews {
	var order []string
	failed, cause, up := map[string]int{}, map[string]string{}, map[string]bool{}
	note := func(name string) {
		if _, ok := failed[name]; !ok {
			failed[name] = 0
			order = append(order, name)
		}
	}
	for _, res := range results {
		if res.SourceDown != "" {
			note(res.SourceDown)
			failed[res.SourceDown]++
			if cause[res.SourceDown] == "" {
				cause[res.SourceDown] = strings.TrimPrefix(res.Error, "source "+res.SourceDown+" is down: ")
			}
			continue
		}
		for _, name := range []string{res.Source, res.RightSource} {
			if name != "" {
				note(name)
				up[name] = true
			}
		}
	}
	var n sourceNews
	for _, name := range order {
		isUp := up[name] && failed[name] == 0
		if isUp {
			s.metrics.sourceUp.WithLabelValues(name).Set(1)
		} else {
			s.metrics.sourceUp.WithLabelValues(name).Set(0)
		}
		switch {
		case !isUp && !s.down[name]:
			n.down++
			line := "source " + name + " is down, " + itoa(int64(failed[name])) + " " + plural(failed[name], "check", "checks") + " did not run: " + cut(cause[name])
			n.lines = append(n.lines, line)
			s.log.Warn("source down", "source", name, "checks", failed[name], "error", cause[name])
		case isUp && s.down[name]:
			n.up++
			n.lines = append(n.lines, "source "+name+" is up again")
			s.log.Info("source up again", "source", name)
		}
		s.down[name] = !isUp
	}
	return n
}

// maxError is the most of an error a message shows; the log has all of it. A
// driver error can carry the whole topology it tried.
const maxError = 200

func cut(s string) string {
	if len(s) <= maxError {
		return s
	}
	i := maxError
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "..."
}

// maxLines is the most lines of one message; the log has all of them.
const maxLines = 20

// message is what a cycle posts: one line as it is, more under a first line
// that counts the checks that changed and the sources, sources first, so one
// event is one message. See
// docs/adr/0007-source-down-one-message.md.
func message(src sourceNews, checks []string, changed int) string {
	lines := append(append([]string(nil), src.lines...), checks...)
	if len(lines) == 1 {
		return "money-checks: " + lines[0]
	}
	var counts []string
	if changed > 0 {
		counts = append(counts, itoa(int64(changed))+" "+plural(changed, "check", "checks")+" changed")
	}
	if src.down > 0 {
		counts = append(counts, itoa(int64(src.down))+" "+plural(src.down, "source", "sources")+" down")
	}
	if src.up > 0 {
		counts = append(counts, itoa(int64(src.up))+" "+plural(src.up, "source", "sources")+" up again")
	}
	msg := "money-checks: " + strings.Join(counts, ", ")
	for i, l := range lines {
		if i == maxLines {
			return msg + "\nand " + itoa(int64(len(lines)-maxLines)) + " more"
		}
		msg += "\n" + l
	}
	return msg
}

// stateChange is the message about a check that starts or stops finding
// violations or runs again after failing, or "".
func stateChange(res check.Result, wasFailed, had, found bool) string {
	switch {
	case found && (!had || wasFailed):
		return describe(res)
	case found:
		return ""
	case wasFailed:
		return res.Name + " runs again and finds no violations"
	case had && res.Scope == check.Full:
		return res.Name + " no longer finds violations"
	}
	return ""
}

// same says whether two amounts of one id are equal; a changed amount makes the
// violation new, as it makes a known one count again.
func same(a, b *check.Money) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Currency == b.Currency && a.Minor == b.Minor
}

// noun is "violation(s)" or, for a compare check, "key(s) where ... differ".
func noun(res check.Result, n int) string {
	switch {
	case res.RightSource != "" && n == 1:
		return "key where " + res.Source + " and " + res.RightSource + " differ"
	case res.RightSource != "":
		return "keys where " + res.Source + " and " + res.RightSource + " differ"
	case n == 1:
		return "violation"
	}
	return "violations"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sums is " (5.00 EUR, 1.01 USD)", or "" without amounts.
func sums(ms []check.Money, abs bool) string {
	t, err := check.Total(ms, abs)
	if err != nil || len(t) == 0 {
		return ""
	}
	return " (" + list(t) + ")"
}

func list(ms []check.Money) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = m.String()
	}
	return strings.Join(parts, ", ")
}

func first(ids []string, n int) []string {
	if len(ids) > n {
		return ids[:n]
	}
	return ids
}

func describe(res check.Result) string {
	msg := res.Name + " found " + itoa(res.Violations) + " " + noun(res, int(res.Violations))
	if len(res.Totals) > 0 {
		msg += " (" + list(res.Totals) + ")"
	}
	if res.Scope == check.Window {
		msg += " in the last " + check.Span(res.Window)
	}
	return msg + " [" + res.Severity + "]"
}
