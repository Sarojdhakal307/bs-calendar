package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"bscalendar/services/calendar-api/internal/outbox"
)

// Metrics are exported on METRICS_ADDR at /metrics (docs/reliable.md §10).
type Metrics struct {
	reg           *prometheus.Registry
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	dataVersion   prometheus.Gauge
	rateLimited   prometheus.Counter
	telemetry     *prometheus.CounterVec
	outboxResults *prometheus.CounterVec
	outboxPending prometheus.Gauge
	outboxDead    prometheus.Gauge
	outboxOldest  prometheus.Gauge
}

// NewMetrics registers all collectors.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_requests_total", Help: "HTTP requests by route, method and status."},
			[]string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_request_duration_seconds", Help: "HTTP request latency by route.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}}, []string{"route"}),
		dataVersion: prometheus.NewGauge(prometheus.GaugeOpts{Name: "calendar_data_version", Help: "Year-table version loaded in memory."}),
		rateLimited: prometheus.NewCounter(prometheus.CounterOpts{Name: "api_rate_limited_total", Help: "Requests rejected by rate limiting."}),
		telemetry: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "client_telemetry_events_total", Help: "Client telemetry events by type."},
			[]string{"type"}),
		outboxResults: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "outbox_deliveries_total", Help: "Outbox delivery attempts by kind and result."},
			[]string{"kind", "result"}),
		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "outbox_pending", Help: "Undelivered outbox rows."}),
		outboxDead:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "outbox_dead", Help: "Dead-lettered outbox rows."}),
		outboxOldest:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "outbox_oldest_pending_age_seconds", Help: "Age of the oldest undelivered row."}),
	}
	reg.MustRegister(m.requests, m.duration, m.dataVersion, m.rateLimited, m.telemetry, m.outboxResults,
		m.outboxPending, m.outboxDead, m.outboxOldest,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) observe(route, method string, status int, d time.Duration) {
	m.requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route).Observe(d.Seconds())
}

// SetDataVersion records the loaded table version.
func (m *Metrics) SetDataVersion(v int64) { m.dataVersion.Set(float64(v)) }

// OutboxResult counts a delivery attempt.
func (m *Metrics) OutboxResult(kind, result string) {
	m.outboxResults.WithLabelValues(kind, result).Inc()
}

// OutboxBacklog records outbox statistics.
func (m *Metrics) OutboxBacklog(s outbox.Stats) {
	m.outboxPending.Set(float64(s.Pending))
	m.outboxDead.Set(float64(s.Dead))
	m.outboxOldest.Set(s.OldestPendingAgeSec)
}

// Handler serves /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}
