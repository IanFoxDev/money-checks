package serve

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ianfoxdev/money-checks/internal/check"
)

type metrics struct {
	runs        prometheus.Counter
	violations  *prometheus.GaugeVec
	known       *prometheus.GaugeVec
	amount      *prometheus.GaugeVec
	up          *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
	duration    *prometheus.GaugeVec
	errors      *prometheus.CounterVec
	expect      *prometheus.GaugeVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		runs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "money_checks_runs_total", Help: "Runs of all checks.",
		}),
		violations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_violations", Help: "Rows the check returned on its last successful run, not counting known violations.",
		}, []string{"check", "severity", "scope"}),
		known: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_known_violations", Help: "Rows of the last successful run that the known file accepts.",
		}, []string{"check", "scope"}),
		// Minor units are integers; a float64 holds them exactly up to 2^53.
		amount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_amount_minor_units", Help: "Total amount of the violations in minor units of the currency, last successful run.",
		}, []string{"check", "currency", "scope"}),
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_up", Help: "1 if the check's last run finished, 0 if it failed.",
		}, []string{"check"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_last_success_timestamp_seconds", Help: "When the check last finished a run.",
		}, []string{"check", "scope"}),
		duration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_duration_seconds", Help: "How long the check's last run took.",
		}, []string{"check", "scope"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "money_check_errors_total", Help: "Runs of the check that failed.",
		}, []string{"check"}),
		expect: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_expect_value", Help: "The number the expect query of the check found on its last run; side is left or right for a compare check.",
		}, []string{"check", "scope", "side"}),
	}
	reg.MustRegister(m.runs, m.violations, m.known, m.amount, m.up, m.lastSuccess, m.duration, m.errors, m.expect)
	return m
}

// update keeps the last good numbers when a run fails: up drops to 0 and the
// violation gauges still say what was last known, which is what an alert on
// "violations > 0" should keep seeing. The gauges of what was found carry the
// scope, so a window run does not overwrite what the last full run found.
func (m *metrics) update(res check.Result, now time.Time) {
	scope := string(res.Scope)
	m.duration.WithLabelValues(res.Name, scope).Set(res.Duration.Seconds())
	// Also when the expect query made the check fail: the drop is the news.
	for _, e := range res.Expected {
		m.expect.WithLabelValues(res.Name, scope, e.Side).Set(float64(e.Found))
	}
	if res.Status == check.StatusFailed {
		m.up.WithLabelValues(res.Name).Set(0)
		m.errors.WithLabelValues(res.Name).Inc()
		return
	}
	m.errors.WithLabelValues(res.Name).Add(0)
	m.up.WithLabelValues(res.Name).Set(1)
	m.lastSuccess.WithLabelValues(res.Name, scope).Set(float64(now.Unix()))
	m.violations.WithLabelValues(res.Name, res.Severity, scope).Set(float64(res.Violations))
	m.known.WithLabelValues(res.Name, scope).Set(float64(res.Known))
	// A currency that is gone from the totals must not keep its old value.
	m.amount.DeletePartialMatch(prometheus.Labels{"check": res.Name, "scope": scope})
	for _, t := range res.Totals {
		m.amount.WithLabelValues(res.Name, t.Currency, scope).Set(float64(t.Minor))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
