// Package serve runs the checks on a schedule, exposes the results as Prometheus
// metrics and posts to Slack when a check changes state.
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
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
	states  map[string]string // last status per check
	ready   atomic.Bool
}

// New builds a server. notify may be nil, then state changes are only logged.
func New(cfg config.Config, open check.Opener, notify Notifier, log *slog.Logger, now func() time.Time) *Server {
	reg := prometheus.NewRegistry()
	return &Server{
		cfg: cfg, open: open, now: now, log: log, notify: notify,
		metrics: newMetrics(reg), reg: reg, states: map[string]string{},
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
func (s *Server) Cycle(ctx context.Context) check.Report {
	r := check.Run(ctx, s.cfg, s.open, s.now)
	for _, w := range r.Warnings {
		s.log.Warn(w)
	}
	s.metrics.runs.Inc()
	for _, res := range r.Results {
		s.metrics.update(res, s.now())
		prev, seen := s.states[res.Name]
		s.states[res.Name] = res.Status
		attrs := []any{"check", res.Name, "status", res.Status, "violations", res.Violations, "duration", res.Duration.String()}
		if res.Error != "" {
			attrs = append(attrs, "error", res.Error)
		}
		s.log.Info("check finished", attrs...)
		if msg := change(prev, seen, res); msg != "" {
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

// change says what to post when a check's status differs from the last run.
// The first run only reports problems; a check that is fine from the start is
// not news.
func change(prev string, seen bool, res check.Result) string {
	if seen && prev == res.Status {
		return ""
	}
	switch res.Status {
	case check.StatusViolations:
		return describe(res)
	case check.StatusFailed:
		return "money-checks: " + res.Name + " failed: " + res.Error
	default:
		if !seen {
			return ""
		}
		if prev == check.StatusFailed {
			return "money-checks: " + res.Name + " runs again and finds no violations"
		}
		return "money-checks: " + res.Name + " no longer finds violations"
	}
}

func describe(res check.Result) string {
	msg := "money-checks: " + res.Name + " found "
	if res.Violations == 1 {
		msg += "1 violation"
	} else {
		msg += itoa(res.Violations) + " violations"
	}
	for i, t := range res.Totals {
		if i == 0 {
			msg += " ("
		} else {
			msg += ", "
		}
		msg += t.String()
		if i == len(res.Totals)-1 {
			msg += ")"
		}
	}
	return msg + " [" + res.Severity + "]"
}
