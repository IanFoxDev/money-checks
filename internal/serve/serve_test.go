package serve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// script is a source whose rows for the one check change from run to run.
type script struct {
	runs  [][]source.Row // rows per run; nil means the query fails
	n     int
	since []time.Time // what each run got
}

func (s *script) Query(_ context.Context, _ config.Check, since time.Time, each func(source.Row) error) error {
	s.since = append(s.since, since)
	rows := s.runs[s.n]
	s.n++
	if rows == nil {
		return errors.New("connection reset")
	}
	for _, r := range rows {
		if err := each(r); err != nil {
			return err
		}
	}
	return nil
}

func (s *script) Close(context.Context) error { return nil }

type notes struct{ msgs []string }

func (n *notes) Notify(_ context.Context, msg string) error {
	n.msgs = append(n.msgs, msg)
	return nil
}

const conf = `
version: 1
sources: {billing: {postgres: {dsn_env: X}}}
checks:
  - name: two_active_subscriptions
    source: billing
    query: q
    id: id
    amount: {field: amount, unit: minor, currency_field: currency}
`

func newServer(t *testing.T, src *script) (*Server, *notes) {
	t.Helper()
	cfg, err := config.Parse([]byte(conf), ".")
	if err != nil {
		t.Fatal(err)
	}
	n := &notes{}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	now := func() time.Time { return time.Unix(1791400000, 0) }
	return New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), now), n
}

func r(id string, amount int64, currency string) source.Row {
	return source.Row{Columns: []string{"id", "amount", "currency"}, Values: []any{id, amount, currency}}
}

func scrape(t *testing.T, s *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func has(t *testing.T, body string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(body, l+"\n") {
			t.Errorf("missing %q", l)
		}
	}
}

func TestCycleNotifiesOnChangesOnly(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{},                    // fine from the start: no message
		{r("u1", 999, "USD")}, // violations appear
		{r("u1", 999, "USD")}, // same: no message
		{r("u1", 999, "USD"), r("u2", 500, "EUR")}, // more, same status: no message
		nil, // the query fails
		nil, // still failing: no message
		{},  // back, clean
		{r("u3", 1, "USD")},
		{},
	}}
	s, n := newServer(t, src)
	for range src.runs {
		s.Cycle(context.Background())
	}
	want := []string{
		"money-checks: two_active_subscriptions found 1 violation (9.99 USD) [error]",
		"money-checks: two_active_subscriptions failed: connection reset",
		"money-checks: two_active_subscriptions runs again and finds no violations",
		"money-checks: two_active_subscriptions found 1 violation (0.01 USD) [error]",
		"money-checks: two_active_subscriptions no longer finds violations",
	}
	if strings.Join(n.msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n"), strings.Join(want, "\n"))
	}
}

func TestMetrics(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD"), r("u2", 500, "EUR")},
		{r("u1", 999, "USD")},
		nil,
	}}
	s, _ := newServer(t, src)
	ctx := context.Background()

	s.Cycle(ctx)
	has(t, scrape(t, s),
		`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 2`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="EUR",scope="full"} 500`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="USD",scope="full"} 999`,
		`money_check_up{check="two_active_subscriptions"} 1`,
		`money_check_last_success_timestamp_seconds{check="two_active_subscriptions",scope="full"} 1.7914e+09`,
		`money_check_errors_total{check="two_active_subscriptions"} 0`,
		`money_checks_runs_total 1`,
	)

	s.Cycle(ctx)
	body := scrape(t, s)
	if strings.Contains(body, `currency="EUR"`) {
		t.Error("EUR is gone from the totals but still exported")
	}
	has(t, body, `money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 1`)

	s.Cycle(ctx) // fails: up drops, the last known numbers stay
	has(t, scrape(t, s),
		`money_check_up{check="two_active_subscriptions"} 0`,
		`money_check_errors_total{check="two_active_subscriptions"} 1`,
		`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 1`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="USD",scope="full"} 999`,
	)
}

func TestReadyz(t *testing.T) {
	s, _ := newServer(t, &script{runs: [][]source.Row{{}}})
	get := func(path string) int {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return rec.Code
	}
	if get("/healthz") != 200 || get("/readyz") != 503 {
		t.Errorf("before the first run: healthz %d, readyz %d", get("/healthz"), get("/readyz"))
	}
	s.Cycle(context.Background())
	if get("/readyz") != 200 {
		t.Errorf("after the first run: readyz %d", get("/readyz"))
	}
}

func TestRunStopsWithTheContext(t *testing.T) {
	s, _ := newServer(t, &script{runs: [][]source.Row{{}, {}}})
	s.cfg.Serve.Listen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !s.ready.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestSlack(t *testing.T) {
	var got map[string]string
	ok := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", req.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(req.Body).Decode(&got)
	}))
	defer ok.Close()
	if err := NewSlack(ok.URL+"/services/T0/B0/secret").Notify(context.Background(), "hello"); err != nil || got["text"] != "hello" {
		t.Errorf("got %v, %v", got, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid_token", http.StatusForbidden)
	}))
	defer bad.Close()
	err := NewSlack(bad.URL+"/services/T0/B0/secret").Notify(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("got %v", err)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	addr := closed.URL
	closed.Close()
	err = NewSlack(addr+"/services/T0/B0/secret").Notify(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), addr) {
		t.Errorf("the webhook URL leaks into the error: %v", err)
	}
}

func TestDescribeSeveralCurrencies(t *testing.T) {
	src := &script{runs: [][]source.Row{{r("a", 100, "USD"), r("b", 250, "EUR"), r("c", 1, "USD")}}}
	s, n := newServer(t, src)
	s.Cycle(context.Background())
	if len(n.msgs) != 1 || n.msgs[0] != "money-checks: two_active_subscriptions found 3 violations (2.50 EUR, 1.01 USD) [error]" {
		t.Errorf("%q", n.msgs)
	}
}

func TestDescribeCompare(t *testing.T) {
	res := check.Result{
		Name: "purchases_reach_stats", Source: "billing", RightSource: "stats", Severity: "error", Violations: 2, Scope: check.Window,
		Window: 72 * time.Hour, Totals: []check.Money{{Currency: "USD", Minor: 3200, Exponent: 2}},
	}
	want := "money-checks: purchases_reach_stats found 2 keys where billing and stats differ (32.00 USD) in the last 72h [error]"
	if got := describe(res); got != want {
		t.Errorf("%q", got)
	}
	res.Violations = 1
	if got := describe(res); !strings.Contains(got, "found 1 key where billing and stats differ") {
		t.Errorf("%q", got)
	}
}

// A known violation is exported on its own gauge and is not news for Slack.
func TestKnownViolations(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD")},
		{r("u1", 999, "USD"), r("u2", 500, "EUR")},
	}}
	s, n := newServer(t, src)
	s.cfg.Known = map[string]map[string]config.Known{"two_active_subscriptions": {"u1": {ID: "u1", Reason: "test account"}}}
	ctx := context.Background()

	s.Cycle(ctx)
	has(t, scrape(t, s),
		`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 0`,
		`money_check_known_violations{check="two_active_subscriptions",scope="full"} 1`,
	)
	if strings.Contains(scrape(t, s), "money_check_amount_minor_units{") {
		t.Error("the amount of a known violation is exported")
	}
	s.Cycle(ctx)
	has(t, scrape(t, s),
		`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 1`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="EUR",scope="full"} 500`,
	)
	want := "money-checks: two_active_subscriptions found 1 violation (5.00 EUR) [error]"
	if strings.Join(n.msgs, "\n") != want {
		t.Errorf("messages %q", n.msgs)
	}
}

// serve reads every row at start and every full_every, the window in between.
// Violations in the window are news; a row that leaves the window is not fixed,
// so only a full run says that violations are gone. The gauges keep one series
// per scope.
func TestCycleWindow(t *testing.T) {
	cfg, err := config.Parse([]byte(strings.Replace(conf, "query: q", "query: select $1\n    window: 1h", 1)+"serve: {full_every: 1h}\n"), ".")
	if err != nil {
		t.Fatal(err)
	}
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD")}, // 0m, full: an old violation
		{},                    // 10m, window: it is older than the window
		{r("u2", 500, "EUR")}, // 20m, window: a new one
		{},                    // 30m, window: u2 is gone from the window, not news
		{},                    // 60m, full: both fixed
	}}
	t0 := time.Unix(1791400000, 0)
	at := t0
	n := &notes{}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	s := New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return at })
	ctx := context.Background()
	for i, m := range []int{0, 10, 20, 30, 60} {
		at = t0.Add(time.Duration(m) * time.Minute)
		s.Cycle(ctx)
		if i == 2 {
			has(t, scrape(t, s),
				`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 1`,
				`money_check_violations{check="two_active_subscriptions",scope="window",severity="error"} 1`,
				`money_check_amount_minor_units{check="two_active_subscriptions",currency="USD",scope="full"} 999`,
				`money_check_amount_minor_units{check="two_active_subscriptions",currency="EUR",scope="window"} 500`,
			)
		}
	}
	var got []string
	for i, since := range src.since {
		got = append(got, since.Sub(t0).String())
		if since.IsZero() {
			got[i] = "all"
		}
	}
	if want := "all -50m0s -40m0s -30m0s all"; strings.Join(got, " ") != want {
		t.Errorf("since %s, want %s", strings.Join(got, " "), want)
	}
	want := []string{
		"money-checks: two_active_subscriptions found 1 violation (9.99 USD) [error]",
		"money-checks: two_active_subscriptions found 1 violation (5.00 EUR) in the last 1h [error]",
		"money-checks: two_active_subscriptions no longer finds violations",
	}
	if strings.Join(n.msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n"), strings.Join(want, "\n"))
	}
	has(t, scrape(t, s), `money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 0`)
}
