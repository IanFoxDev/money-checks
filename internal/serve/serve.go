// Package serve runs the checks on a schedule, exposes the results as Prometheus
// metrics and posts to Slack when a check changes state.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
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
	// lastFull is when the last full run started; zero before the first.
	lastFull time.Time
	ready    atomic.Bool
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
		metrics: newMetrics(reg), reg: reg, states: map[string]*state{},
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

// Cycle runs every check once, updates the metrics and notifies about changes.
// The first run and then one every serve.full_every reads every row; the runs in
// between read the window of the checks that have one.
func (s *Server) Cycle(ctx context.Context) check.Report {
	scope := check.Window
	if start := s.now(); s.lastFull.IsZero() || start.Sub(s.lastFull) >= s.cfg.Serve.FullEvery {
		scope, s.lastFull = check.Full, start
	}
	found := map[string]*rows{}
	r := check.RunExport(ctx, s.cfg, scope, s.open, s.now, func(name string, v check.Sample) error {
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
	for _, w := range r.Warnings {
		s.log.Warn(w)
	}
	s.metrics.runs.Inc()
	for i, res := range r.Results {
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
		msg, added := st.change(res, got, s.now())
		if len(added) > 0 {
			s.log.Info("new violations", "check", res.Name, "count", len(added), "ids", first(added, *s.cfg.Checks[i].Samples))
		}
		if msg != "" {
			s.log.Info("state changed", "check", res.Name, "message", msg)
			if s.notify != nil {
				if err := s.notify.Notify(ctx, msg); err != nil {
					s.log.Error("notification failed", "check", res.Name, "error", err)
				}
			}
		}
	}
	s.ready.Store(true)
	return r
}

// Run serves HTTP on the configured address and runs the checks now and then
// every interval, until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Serve.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("serving", "listen", ln.Addr().String(), "interval", s.cfg.Serve.Interval.String(), "checks", len(s.cfg.Checks))
	if names := s.cfg.WithoutExpect(); len(names) > 0 {
		s.log.Warn(check.WithoutExpectWarning(names))
	}

	tick := time.NewTicker(s.cfg.Serve.Interval)
	defer tick.Stop()
	for {
		s.Cycle(ctx)
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
		}
	}
}

// change records res and says what to post about it, or "", and which ids are
// new. The first run only reports problems; a check that is fine from the start
// is not news. Then every new violation is news, an id not seen before or one
// whose amount changed, also when the check already had violations. Only a full
// run says that violations are gone: a row that leaves the window is not fixed.
func (st *state) change(res check.Result, got *rows, now time.Time) (string, []string) {
	if res.Status == check.StatusFailed {
		if st.failed {
			return "", nil
		}
		st.failed = true
		return "money-checks: " + res.Name + " failed: " + res.Error, nil
	}
	wasFailed, had := st.failed, st.found[res.Scope]
	found := res.Status == check.StatusViolations
	st.failed, st.found[res.Scope] = false, found
	if got == nil {
		got = &rows{}
	}
	if got.over {
		st.seen, st.complete = nil, false
		return stateChange(res, wasFailed, had, found), nil
	}

	var added []string
	var addedSums, fixedSums []check.Money
	fixed := 0
	for id, a := range got.ids {
		if v, ok := st.seen[id]; !ok || !same(v.amount, a) {
			added = append(added, id)
			if a != nil {
				addedSums = append(addedSums, *a)
			}
		}
	}
	sort.Strings(added)
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
	if res.Scope == check.Full {
		for id, v := range st.seen {
			if _, ok := got.ids[id]; !ok {
				fixed++
				if v.amount != nil {
					fixedSums = append(fixedSums, *v.amount)
				}
			}
		}
	}
	complete := st.complete
	st.seen, st.complete = next, complete || res.Scope == check.Full

	// Before a full picture, after a failure, or when every violation is new,
	// the state change says it all.
	if !complete || wasFailed || len(added) == int(res.Violations) && fixed == 0 {
		return stateChange(res, wasFailed, had, found), added
	}
	if len(added) == 0 && fixed == 0 {
		return "", nil
	}
	if !found {
		return "money-checks: " + res.Name + " no longer finds violations", nil
	}
	compare := res.RightSource != ""
	msg := "money-checks: " + res.Name
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
	return msg + " [" + res.Severity + "]", added
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
		return "money-checks: " + res.Name + " runs again and finds no violations"
	case had && res.Scope == check.Full:
		return "money-checks: " + res.Name + " no longer finds violations"
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
	msg := "money-checks: " + res.Name + " found " + itoa(res.Violations) + " " + noun(res, int(res.Violations))
	if len(res.Totals) > 0 {
		msg += " (" + list(res.Totals) + ")"
	}
	if res.Scope == check.Window {
		msg += " in the last " + check.Span(res.Window)
	}
	return msg + " [" + res.Severity + "]"
}
