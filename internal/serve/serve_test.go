package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
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

func (s *script) Query(_ context.Context, _ config.Check, since, _ time.Time, each func(source.Row) error) error {
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
	return newServerWith(t, conf, src)
}

func newServerWith(t *testing.T, conf string, src *script) (*Server, *notes) {
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

func TestCycleNotifiesOnNews(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{},                    // fine from the start: no message
		{r("u1", 999, "USD")}, // violations appear
		{r("u1", 999, "USD")}, // same: no message
		{r("u1", 999, "USD"), r("u2", 500, "EUR")}, // one more, while the check already finds one
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
		"money-checks: two_active_subscriptions found 1 new violation (5.00 EUR), 2 in all [error]",
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
	want := "purchases_reach_stats found 2 keys where billing and stats differ (32.00 USD) in the last 72h [error]"
	if got := describe(res); got != want {
		t.Errorf("%q", got)
	}
	res.Violations = 1
	if got := describe(res); !strings.Contains(got, "found 1 key where billing and stats differ") {
		t.Errorf("%q", got)
	}
}

// All that changed in one cycle is one message, the checks in the order of the
// configuration.
func TestOneMessagePerCycle(t *testing.T) {
	conf := conf + `
  - name: refund_exceeds_payment
    source: billing
    query: q
    id: id
    amount: {field: amount, unit: minor, currency_field: currency}
  - name: negative_wallet_balance
    source: billing
    query: q
    id: id
    amount: {field: amount, unit: minor, currency_field: currency}
`
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD")}, {}, {r("w1", 100, "EUR")}, // first cycle: two are not clean
		{r("u1", 999, "USD")}, {}, {r("w1", 100, "EUR")}, // nothing changed: no message
		{r("u1", 999, "USD"), r("u2", 500, "USD")}, nil, {}, // a new one, a failure, one fixed
	}}
	s, n := newServerWith(t, conf, src)
	for range 3 {
		s.Cycle(context.Background())
	}
	want := []string{
		"money-checks: 2 checks changed\n" +
			"two_active_subscriptions found 1 violation (9.99 USD) [error]\n" +
			"negative_wallet_balance found 1 violation (1.00 EUR) [error]",
		"money-checks: 3 checks changed\n" +
			"two_active_subscriptions found 1 new violation (5.00 USD), 2 in all [error]\n" +
			"refund_exceeds_payment failed: connection reset\n" +
			"negative_wallet_balance no longer finds violations",
	}
	if strings.Join(n.msgs, "\n---\n") != strings.Join(want, "\n---\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n---\n"), strings.Join(want, "\n---\n"))
	}
}

// A message that needs scrolling is not read: above maxLines it ends with how
// many more there are.
func TestMessageLimit(t *testing.T) {
	lines := make([]string, maxLines+2)
	for i := range lines {
		lines[i] = "c" + itoa(int64(i)) + " failed: x"
	}
	got := strings.Split(message(sourceNews{}, lines), "\n")
	if len(got) != maxLines+2 || got[0] != "money-checks: 22 checks changed" || got[maxLines] != "c19 failed: x" || got[maxLines+1] != "and 2 more" {
		t.Errorf("%q", got)
	}
	if got := message(sourceNews{}, lines[:1]); got != "money-checks: c0 failed: x" {
		t.Errorf("one line: %q", got)
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

func TestExpectMetricAndFailure(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{{Columns: []string{"n"}, Values: []any{"1532"}}}, {r("u1", 999, "USD")},
		{}, // the expect query finds nothing; the check does not run
	}}
	s, n := newServerWith(t, conf+"    expect: {query: select count(*) from subscriptions}\n", src)
	ctx := context.Background()

	s.Cycle(ctx)
	has(t, scrape(t, s),
		`money_check_expect_value{check="two_active_subscriptions",scope="full",side=""} 1532`,
		`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 1`,
	)

	s.Cycle(ctx)
	has(t, scrape(t, s),
		`money_check_expect_value{check="two_active_subscriptions",scope="full",side=""} 0`,
		`money_check_up{check="two_active_subscriptions"} 0`,
		`money_check_violations{check="two_active_subscriptions",scope="full",severity="error"} 1`,
	)
	if src.n != 3 {
		t.Errorf("%d queries, want 3", src.n)
	}
	want := "money-checks: two_active_subscriptions failed: expect found 0, at least 1 required"
	if len(n.msgs) != 2 || n.msgs[1] != want {
		t.Errorf("messages %q", n.msgs)
	}
}

// A violation is new when its id was not there or its amount changed; a full run
// that no longer finds an id says it is fixed.
func TestNewAndFixed(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD"), r("u2", 500, "USD")},
		{r("u1", 999, "USD"), r("u2", 700, "USD"), r("u3", 100, "USD")}, // u2 changed, u3 is new
		{r("u2", 700, "USD"), r("u3", 100, "USD")},                      // u1 fixed
		{r("u3", 100, "USD"), r("u4", 1, "USD")},                        // u2 fixed, u4 new
	}}
	s, n := newServer(t, src)
	for range src.runs {
		s.Cycle(context.Background())
	}
	want := []string{
		"money-checks: two_active_subscriptions found 2 violations (14.99 USD) [error]",
		"money-checks: two_active_subscriptions found 2 new violations (8.00 USD), 3 in all [error]",
		"money-checks: two_active_subscriptions: 1 violation fixed (9.99 USD), 2 left [error]",
		"money-checks: two_active_subscriptions found 1 new violation (0.01 USD), 1 fixed (7.00 USD), 2 in all [error]",
	}
	if strings.Join(n.msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n"), strings.Join(want, "\n"))
	}
	// The age of u3 is from the run that first saw it.
	if v := s.states["two_active_subscriptions"].seen["u3"]; !v.first.Equal(time.Unix(1791400000, 0)) {
		t.Errorf("u3 first seen %s", v.first)
	}
}

// A window run tells the new violations in its window. An old one found again in
// the window is not news, and the ones outside it are not fixed.
func TestNewInWindow(t *testing.T) {
	cfg, err := config.Parse([]byte(strings.Replace(conf, "query: q", "query: select $1\n    window: 1h", 1)+"serve: {full_every: 1h}\n"), ".")
	if err != nil {
		t.Fatal(err)
	}
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD"), r("u2", 500, "USD")}, // full
		{r("u2", 500, "USD")},                      // window: u2 is old
		{r("u2", 500, "USD"), r("u3", 100, "USD")}, // window: u3 is new
		{r("u1", 999, "USD"), r("u3", 100, "USD")}, // full: u2 fixed
	}}
	t0 := time.Unix(1791400000, 0)
	at := t0
	n := &notes{}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	s := New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return at })
	for _, m := range []int{0, 10, 20, 60} {
		at = t0.Add(time.Duration(m) * time.Minute)
		s.Cycle(context.Background())
	}
	want := []string{
		"money-checks: two_active_subscriptions found 2 violations (14.99 USD) [error]",
		"money-checks: two_active_subscriptions found 1 new violation (1.00 USD) in the last 1h [error]",
		"money-checks: two_active_subscriptions: 1 violation fixed (5.00 USD), 2 left [error]",
	}
	if strings.Join(n.msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n"), strings.Join(want, "\n"))
	}
}

// For a compare check the id is the key and the amount the difference: a day
// whose difference grew is new.
func TestNewKeysOfCompare(t *testing.T) {
	res := check.Result{Name: "daily", Source: "billing", RightSource: "stats", Severity: "error", Scope: check.Full, Status: check.StatusViolations}
	st := &state{found: map[check.Scope]bool{}}
	d := func(minor int64) *check.Money { return &check.Money{Currency: "USD", Minor: minor, Exponent: 2} }
	now := time.Unix(1791400000, 0)
	res.Violations = 2
	st.change(res, &rows{ids: map[string]*check.Money{"d1": d(-500), "d2": d(100)}}, now)
	res.Violations = 2
	msg, added := st.change(res, &rows{ids: map[string]*check.Money{"d1": d(-800), "d2": d(100)}}, now)
	if want := "daily found 1 new key where billing and stats differ (8.00 USD), 2 in all [error]"; msg != want || len(added) != 1 {
		t.Errorf("%q %v", msg, added)
	}
	res.Violations = 1
	msg, _ = st.change(res, &rows{ids: map[string]*check.Money{"d1": d(-800)}}, now)
	if want := "daily: 1 key no longer differs (1.00 USD), 1 left [error]"; msg != want {
		t.Errorf("%q", msg)
	}
}

// Above maxTracked violations a check is news only when its state changes.
func TestTooManyToTrack(t *testing.T) {
	many := make([]source.Row, maxTracked+1)
	for i := range many {
		many[i] = r("u"+itoa(int64(i)), 1, "USD")
	}
	src := &script{runs: [][]source.Row{{r("u1", 1, "USD")}, many, append(many, r("x", 1, "USD"))}}
	s, n := newServer(t, src)
	for range src.runs {
		s.Cycle(context.Background())
	}
	if len(n.msgs) != 1 || s.states["two_active_subscriptions"].complete {
		t.Errorf("messages %q", n.msgs)
	}
}

// New violations are counted from the second run on; the age is of the oldest
// violation the check still finds.
func TestNewAndAgeMetrics(t *testing.T) {
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD")},
		{r("u1", 999, "USD"), r("u2", 500, "USD")},
		{r("u2", 600, "USD")}, // u1 fixed, u2 changed: new, but as old as before
		{},
	}}
	t0 := time.Unix(1791400000, 0)
	at := t0
	n := &notes{}
	cfg, err := config.Parse([]byte(conf), ".")
	if err != nil {
		t.Fatal(err)
	}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	s := New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return at })
	want := [][]string{
		{`money_check_new_violations_total{check="two_active_subscriptions"} 0`, `money_check_oldest_violation_age_seconds{check="two_active_subscriptions"} 0`},
		{`money_check_new_violations_total{check="two_active_subscriptions"} 1`, `money_check_oldest_violation_age_seconds{check="two_active_subscriptions"} 3600`},
		{`money_check_new_violations_total{check="two_active_subscriptions"} 2`, `money_check_oldest_violation_age_seconds{check="two_active_subscriptions"} 3600`},
		{`money_check_new_violations_total{check="two_active_subscriptions"} 2`, `money_check_oldest_violation_age_seconds{check="two_active_subscriptions"} 0`},
	}
	for i := range src.runs {
		at = t0.Add(time.Duration(i) * time.Hour)
		s.Cycle(context.Background())
		has(t, scrape(t, s), want[i]...)
	}
}

// counter is a source that finds nothing and records which check ran when.
type counter struct {
	at   func() time.Time
	runs map[string][]int
	t0   time.Time
}

func (c *counter) Query(_ context.Context, ch config.Check, _, _ time.Time, _ func(source.Row) error) error {
	c.runs[ch.Name] = append(c.runs[ch.Name], int(c.at().Sub(c.t0)/time.Hour))
	return nil
}

func (*counter) Close(context.Context) error { return nil }

// A check with every sits out the runs before its turn; a full run runs it
// whatever its turn, and its turns count from there.
func TestEvery(t *testing.T) {
	cfg, err := config.Parse([]byte(`
version: 1
sources: {billing: {postgres: {dsn_env: X}}}
checks:
  - {name: light, source: billing, query: q, id: id}
  - {name: heavy, source: billing, query: q, id: id, every: 3h}
  - {name: daily, source: billing, query: q, id: id, every: 4h}
serve: {interval: 1h, full_every: 6h}
`), ".")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1791400000, 0)
	at := t0
	src := &counter{at: func() time.Time { return at }, runs: map[string][]int{}, t0: t0}
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) { return src, nil }
	s := New(cfg, open, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return at })
	for h := range 9 {
		at = t0.Add(time.Duration(h) * time.Hour)
		s.Cycle(context.Background())
	}
	for name, want := range map[string]string{"light": "[0 1 2 3 4 5 6 7 8]", "heavy": "[0 3 6]", "daily": "[0 4 6]"} {
		if got := fmt.Sprint(src.runs[name]); got != want {
			t.Errorf("%s ran at %s, want %s", name, got, want)
		}
	}
	has(t, scrape(t, s),
		`money_check_interval_seconds{check="heavy"} 10800`,
		`money_check_interval_seconds{check="light"} 3600`,
	)
}

// A source that cannot be reached is one line when it goes down and one when it
// is back. Its checks keep their violations meanwhile: nothing is fixed while it
// is away, and nothing is posted again when it returns.
func TestSourceDownAndBack(t *testing.T) {
	conf := conf + `
  - name: refund_exceeds_payment
    source: billing
    query: q
    id: id
    amount: {field: amount, unit: minor, currency_field: currency}
`
	cfg, err := config.Parse([]byte(conf), ".")
	if err != nil {
		t.Fatal(err)
	}
	src := &script{runs: [][]source.Row{
		{r("u1", 999, "USD")}, {}, // a violation
		{r("u1", 999, "USD")}, {}, // back: the same
		{r("u1", 999, "USD"), r("u2", 500, "USD")}, {}, // one new
	}}
	down := false
	open := func(context.Context, string, config.Source, func(string)) (source.Source, error) {
		if down {
			return nil, fmt.Errorf("source billing: postgres: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
		}
		return src, nil
	}
	n := &notes{}
	s := New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return time.Unix(1791400000, 0) })
	ctx := context.Background()

	s.Cycle(ctx)
	down = true
	s.Cycle(ctx)
	has(t, scrape(t, s), `money_check_source_up{source="billing"} 0`, `money_check_up{check="two_active_subscriptions"} 0`)
	s.Cycle(ctx) // still down: nothing
	down = false
	s.Cycle(ctx)
	has(t, scrape(t, s), `money_check_source_up{source="billing"} 1`)
	s.Cycle(ctx)
	want := []string{
		"money-checks: two_active_subscriptions found 1 violation (9.99 USD) [error]",
		"money-checks: source billing is down, 2 checks did not run: postgres: dial tcp: connection refused",
		"money-checks: source billing is up again",
		"money-checks: two_active_subscriptions found 1 new violation (5.00 USD), 2 in all [error]",
	}
	if strings.Join(n.msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n"), strings.Join(want, "\n"))
	}
}

// Sources come first and are counted in the first line; a long error is cut.
func TestMessageWithSources(t *testing.T) {
	src := sourceNews{lines: []string{"source stats is down, 4 checks did not run: x"}, down: 1}
	got := message(src, []string{"two_active_subscriptions found 1 new violation (5.00 EUR), 2 in all [error]"})
	want := "money-checks: 1 check changed, 1 source down\n" +
		"source stats is down, 4 checks did not run: x\n" +
		"two_active_subscriptions found 1 new violation (5.00 EUR), 2 in all [error]"
	if got != want {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("a", maxError-1) + "\u00e9" + "tail"
	if c := cut(long); c != strings.Repeat("a", maxError-1)+"..." {
		t.Errorf("cut: %q", c)
	}
}

// The checks of a source that was down run again after serve.retry, then twice
// as long, up to the interval; only those checks, and the next cycle comes when
// it would have.
func TestRetry(t *testing.T) {
	conf := strings.Replace(conf, "version: 1\n", "version: 1\nserve: {interval: 10m, retry: 1m}\n", 1) + `
  - name: daily_totals
    source: stats
    query: q
    id: id
`
	conf = strings.Replace(conf, "sources: {billing: {postgres: {dsn_env: X}}}", "sources: {billing: {postgres: {dsn_env: X}}, stats: {postgres: {dsn_env: Y}}}", 1)
	cfg, err := config.Parse([]byte(conf), ".")
	if err != nil {
		t.Fatal(err)
	}
	billing := &script{runs: [][]source.Row{{r("u1", 999, "USD")}, {r("u1", 999, "USD")}}}
	stats := &script{runs: [][]source.Row{{}, {}}}
	down := false
	open := func(_ context.Context, name string, _ config.Source, _ func(string)) (source.Source, error) {
		if name == "stats" {
			return stats, nil
		}
		if down {
			return nil, fmt.Errorf("source billing: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
		}
		return billing, nil
	}
	n := &notes{}
	s := New(cfg, open, n, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return time.Unix(1791400000, 0) })
	ctx := context.Background()

	s.Cycle(ctx)
	if d := s.RetryAfter(); d != 0 {
		t.Errorf("retry after %s with every source up", d)
	}
	down = true
	s.Cycle(ctx)
	for _, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 0} {
		if d := s.RetryAfter(); d != want {
			t.Errorf("retry after %s, want %s", d, want)
		}
		if want == 0 {
			break
		}
		s.Retry(ctx)
	}
	if stats.n != 2 {
		t.Errorf("stats ran %d times, want 2: retries run only the checks that did not", stats.n)
	}
	down = false
	r := s.Retry(ctx)
	if len(r.Results) != 1 || r.Results[0].Name != "two_active_subscriptions" || r.Results[0].Scope != check.Full {
		t.Errorf("retry ran %+v", r.Results)
	}
	if d := s.RetryAfter(); d != 0 {
		t.Errorf("retry after %s once the source is back", d)
	}
	if !s.lastFull.Equal(time.Unix(1791400000, 0)) {
		t.Errorf("a retry moved the full run: %s", s.lastFull)
	}
	want := []string{
		"money-checks: two_active_subscriptions found 1 violation (9.99 USD) [error]",
		"money-checks: source billing is down, 1 check did not run: dial tcp: connection refused",
		"money-checks: source billing is up again",
	}
	if strings.Join(n.msgs, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(n.msgs, "\n"), strings.Join(want, "\n"))
	}
}
