package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry             *prometheus.Registry
	events               *prometheus.CounterVec
	reactions            *prometheus.CounterVec
	alertmanagerRequests *prometheus.CounterVec
	alertmanagerDuration prometheus.Histogram
	holmesRequests       *prometheus.CounterVec
	holmesDuration       prometheus.Histogram
	persistenceErrors    prometheus.Counter
	queueDepth           prometheus.Gauge
	holmesActive         prometheus.Gauge
	sessions             prometheus.Gauge
	watchdogLastSeen     prometheus.Gauge
	watchdogReceived     prometheus.Counter
}

func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertlens_events_total", Help: "Slack events by bounded outcome.",
		}, []string{"outcome"}),
		reactions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertlens_reactions_total", Help: "Slack reaction operations by outcome.",
		}, []string{"operation", "outcome"}),
		alertmanagerRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertlens_alertmanager_requests_total", Help: "Alertmanager requests by outcome.",
		}, []string{"outcome"}),
		alertmanagerDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "alertlens_alertmanager_request_duration_seconds", Help: "Alertmanager request duration.",
		}),
		holmesRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "alertlens_holmes_requests_total", Help: "Holmes requests by outcome.",
		}, []string{"outcome"}),
		holmesDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "alertlens_holmes_request_duration_seconds", Help: "Holmes request duration.",
		}),
		persistenceErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "alertlens_persistence_errors_total", Help: "State persistence failures.",
		}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alertlens_queue_depth", Help: "Current queued work items.",
		}),
		holmesActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alertlens_holmes_active", Help: "Current Holmes requests.",
		}),
		sessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alertlens_sessions", Help: "Current persisted sessions.",
		}),
		watchdogLastSeen: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "alertlens_watchdog_last_seen_timestamp", Help: "Unix timestamp of the last Watchdog event.",
		}),
		watchdogReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "alertlens_watchdog_received_total", Help: "Watchdog events received.",
		}),
	}
	m.registry.MustRegister(
		m.events, m.reactions,
		m.alertmanagerRequests, m.alertmanagerDuration,
		m.holmesRequests, m.holmesDuration,
		m.persistenceErrors, m.queueDepth, m.holmesActive, m.sessions,
		m.watchdogLastSeen, m.watchdogReceived,
	)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) Event(outcome string) {
	m.events.WithLabelValues(outcome).Inc()
}

func (m *Metrics) Reaction(operation, outcome string) {
	m.reactions.WithLabelValues(operation, outcome).Inc()
}

func (m *Metrics) Alertmanager(outcome string, duration time.Duration) {
	m.alertmanagerRequests.WithLabelValues(outcome).Inc()
	m.alertmanagerDuration.Observe(duration.Seconds())
}

func (m *Metrics) Holmes(outcome string, duration time.Duration) {
	m.holmesRequests.WithLabelValues(outcome).Inc()
	m.holmesDuration.Observe(duration.Seconds())
}

func (m *Metrics) PersistenceError()          { m.persistenceErrors.Inc() }
func (m *Metrics) QueueDepth(depth int)       { m.queueDepth.Set(float64(depth)) }
func (m *Metrics) HolmesActive(delta float64) { m.holmesActive.Add(delta) }
func (m *Metrics) Sessions(count int)         { m.sessions.Set(float64(count)) }

func (m *Metrics) Watchdog(now time.Time) {
	m.watchdogLastSeen.Set(float64(now.Unix()))
	m.watchdogReceived.Inc()
}
