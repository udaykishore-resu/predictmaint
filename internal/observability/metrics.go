package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/udaykishore-resu/predictmaint/internal/domain/alerting"
	"github.com/udaykishore-resu/predictmaint/internal/domain/detect"
)

// Metrics holds every Prometheus series the service exports: HTTP RED
// metrics plus the domain metrics that tell you whether the programme is
// healthy (alert volume, withheld alerts, work orders, feedback).
type Metrics struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInflight prometheus.Gauge

	metricsIngested *prometheus.CounterVec
	evaluations     *prometheus.CounterVec
	riskHist        *prometheus.HistogramVec
	alertOutcomes   *prometheus.CounterVec
	workOrders      *prometheus.CounterVec
	feedback        *prometheus.CounterVec
	kafkaBatches    *prometheus.CounterVec
	precisionProxy  *prometheus.GaugeVec
	alertsPerDay    *prometheus.GaugeVec
	mttaSeconds     *prometheus.GaugeVec
	assetsTracked   prometheus.Gauge
}

// NewMetrics registers all series on a fresh registry (so tests can create
// many without duplicate-registration panics).
func NewMetrics(namespace string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{reg: reg}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Subsystem: "http",
		Name: "requests_total", Help: "HTTP requests by route, method and status class."}, []string{"route", "method", "status"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Subsystem: "http",
		Name: "request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets}, []string{"route", "method"})
	m.httpInflight = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace, Subsystem: "http",
		Name: "requests_in_flight", Help: "HTTP requests currently being served."})

	m.metricsIngested = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace,
		Name: "metrics_ingested_total", Help: "Sensor metrics processed by result."}, []string{"result"})
	m.evaluations = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace,
		Name: "evaluations_total", Help: "Detector evaluations per asset class."}, []string{"class"})
	m.riskHist = prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace,
		Name: "risk_score", Help: "Distribution of fused risk scores.", Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1}}, []string{"class"})
	m.alertOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace,
		Name: "alert_outcomes_total", Help: "Alert policy outcomes (fire, suppressed, rate_limited, clear, pending)."}, []string{"site", "class", "outcome"})
	m.workOrders = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace,
		Name: "work_orders_total", Help: "Work order actions by result."}, []string{"site", "class", "result"})
	m.feedback = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace,
		Name: "feedback_total", Help: "Technician verdicts."}, []string{"class", "verdict"})
	m.kafkaBatches = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace,
		Name: "kafka_batches_total", Help: "Kafka batches by result."}, []string{"result"})
	m.precisionProxy = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace,
		Name: "precision_proxy", Help: "confirmed / (confirmed + dismissed) per site."}, []string{"site"})
	m.alertsPerDay = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace,
		Name: "alerts_per_day", Help: "Alert rate per site."}, []string{"site"})
	m.mttaSeconds = prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace,
		Name: "mtta_seconds", Help: "Mean time to technician acknowledgement per site."}, []string{"site"})
	m.assetsTracked = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace,
		Name: "assets_tracked", Help: "Assets with in-memory state."})

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequests, m.httpDuration, m.httpInflight,
		m.metricsIngested, m.evaluations, m.riskHist, m.alertOutcomes, m.workOrders, m.feedback, m.kafkaBatches,
		m.precisionProxy, m.alertsPerDay, m.mttaSeconds, m.assetsTracked,
	)
	return m
}

// Handler serves the registry in Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Registry exposes the underlying registry (tests).
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// ObserveHTTP records one served request.
func (m *Metrics) ObserveHTTP(route, method string, status int, d time.Duration) {
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status/100)+"xx").Inc()
	m.httpDuration.WithLabelValues(route, method).Observe(d.Seconds())
}

// InFlight adjusts the in-flight gauge.
func (m *Metrics) InFlight(delta float64) { m.httpInflight.Add(delta) }

// KafkaBatch records a consumed batch result ("ok" | "error").
func (m *Metrics) KafkaBatch(result string) { m.kafkaBatches.WithLabelValues(result).Inc() }

// SetStats publishes programme-level gauges for a site.
func (m *Metrics) SetStats(s alerting.Stats) {
	site := s.Site
	if site == "" {
		site = "all"
	}
	m.precisionProxy.WithLabelValues(site).Set(s.PrecisionProxy)
	m.alertsPerDay.WithLabelValues(site).Set(s.AlertsPerDay)
	m.mttaSeconds.WithLabelValues(site).Set(s.MTTASeconds)
}

// SetAssetsTracked publishes the asset count.
func (m *Metrics) SetAssetsTracked(n int) { m.assetsTracked.Set(float64(n)) }

// --- engine.Observer implementation ---

// MetricsIngested implements engine.Observer.
func (m *Metrics) MetricsIngested(accepted, rejected, duplicates int) {
	m.metricsIngested.WithLabelValues("accepted").Add(float64(accepted))
	m.metricsIngested.WithLabelValues("rejected").Add(float64(rejected))
	m.metricsIngested.WithLabelValues("duplicate").Add(float64(duplicates))
}

// Evaluated implements engine.Observer.
func (m *Metrics) Evaluated(class string, a detect.Assessment) {
	m.evaluations.WithLabelValues(class).Inc()
	m.riskHist.WithLabelValues(class).Observe(a.Risk)
}

// AlertOutcome implements engine.Observer.
func (m *Metrics) AlertOutcome(site, class string, outcome alerting.Outcome) {
	m.alertOutcomes.WithLabelValues(site, class, string(outcome)).Inc()
}

// WorkOrder implements engine.Observer.
func (m *Metrics) WorkOrder(site, class, result string) {
	m.workOrders.WithLabelValues(site, class, result).Inc()
}

// Feedback implements engine.Observer.
func (m *Metrics) Feedback(class string, verdict alerting.Verdict) {
	m.feedback.WithLabelValues(class, string(verdict)).Inc()
}
