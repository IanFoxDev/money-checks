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
	amount      *prometheus.GaugeVec
	up          *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
	duration    *prometheus.GaugeVec
	errors      *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		runs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "money_checks_runs_total", Help: "Runs of all checks.",
		}),
		violations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_violations", Help: "Rows the check returned on its last successful run.",
		}, []string{"check", "severity"}),
		// Minor units are integers; a float64 holds them exactly up to 2^53.
		amount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_amount_minor_units", Help: "Total amount of the violations in minor units of the currency, last successful run.",
		}, []string{"check", "currency"}),
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_up", Help: "1 if the check's last run finished, 0 if it failed.",
		}, []string{"check"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_last_success_timestamp_seconds", Help: "When the check last finished a run.",
		}, []string{"check"}),
		duration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "money_check_duration_seconds", Help: "How long the check's last run took.",
		}, []string{"check"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "money_check_errors_total", Help: "Runs of the check that failed.",
		}, []string{"check"}),
	}
	reg.MustRegister(m.runs, m.violations, m.amount, m.up, m.lastSuccess, m.duration, m.errors)
	return m
}

// update keeps the last good numbers when a run fails: up drops to 0 and the
// violation gauges still say what was last known, which is what an alert on
// "violations > 0" should keep seeing.
func (m *metrics) update(res check.Result, now time.Time) {
	m.duration.WithLabelValues(res.Name).Set(res.Duration.Seconds())
	if res.Status == check.StatusFailed {
		m.up.WithLabelValues(res.Name).Set(0)
		m.errors.WithLabelValues(res.Name).Inc()
		return
	}
	m.errors.WithLabelValues(res.Name).Add(0)
	m.up.WithLabelValues(res.Name).Set(1)
	m.lastSuccess.WithLabelValues(res.Name).Set(float64(now.Unix()))
	m.violations.WithLabelValues(res.Name, res.Severity).Set(float64(res.Violations))
	// A currency that is gone from the totals must not keep its old value.
	m.amount.DeletePartialMatch(prometheus.Labels{"check": res.Name})
	for _, t := range res.Totals {
		m.amount.WithLabelValues(res.Name, t.Currency).Set(float64(t.Minor))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
