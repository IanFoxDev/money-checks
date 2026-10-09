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

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// script is a source whose rows for the one check change from run to run.
type script struct {
	runs [][]source.Row // rows per run; nil means the query fails
	n    int
}

func (s *script) Query(_ context.Context, _ config.Check, each func(source.Row) error) error {
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
		`money_check_violations{check="two_active_subscriptions",severity="error"} 2`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="EUR"} 500`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="USD"} 999`,
		`money_check_up{check="two_active_subscriptions"} 1`,
		`money_check_last_success_timestamp_seconds{check="two_active_subscriptions"} 1.7914e+09`,
		`money_check_errors_total{check="two_active_subscriptions"} 0`,
		`money_checks_runs_total 1`,
	)

	s.Cycle(ctx)
	body := scrape(t, s)
	if strings.Contains(body, `currency="EUR"`) {
		t.Error("EUR is gone from the totals but still exported")
	}
	has(t, body, `money_check_violations{check="two_active_subscriptions",severity="error"} 1`)

	s.Cycle(ctx) // fails: up drops, the last known numbers stay
	has(t, scrape(t, s),
		`money_check_up{check="two_active_subscriptions"} 0`,
		`money_check_errors_total{check="two_active_subscriptions"} 1`,
		`money_check_violations{check="two_active_subscriptions",severity="error"} 1`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="USD"} 999`,
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
		`money_check_violations{check="two_active_subscriptions",severity="error"} 0`,
		`money_check_known_violations{check="two_active_subscriptions"} 1`,
	)
	if strings.Contains(scrape(t, s), "money_check_amount_minor_units{") {
		t.Error("the amount of a known violation is exported")
	}
	s.Cycle(ctx)
	has(t, scrape(t, s),
		`money_check_violations{check="two_active_subscriptions",severity="error"} 1`,
		`money_check_amount_minor_units{check="two_active_subscriptions",currency="EUR"} 500`,
	)
	want := "money-checks: two_active_subscriptions found 1 violation (5.00 EUR) [error]"
	if strings.Join(n.msgs, "\n") != want {
		t.Errorf("messages %q", n.msgs)
	}
}
