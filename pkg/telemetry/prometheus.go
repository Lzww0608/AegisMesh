package telemetry

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type PrometheusMetrics struct {
	requests    *prometheus.CounterVec
	latency     *prometheus.HistogramVec
	inflight    *prometheus.GaugeVec
	latencyEWMA *prometheus.GaugeVec
}

var (
	defaultPrometheusOnce    sync.Once
	defaultPrometheusMetrics *PrometheusMetrics
)

func NewPrometheusMetrics(reg prometheus.Registerer) (*PrometheusMetrics, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &PrometheusMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_rpc_requests_total",
			Help: "Total RPC requests observed by the Aegis SDK.",
		}, []string{"source", "destination", "method", "upstream", "status"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_rpc_latency_seconds",
			Help:    "RPC latency observed by the Aegis SDK.",
			Buckets: prometheus.DefBuckets,
		}, []string{"source", "destination", "method", "upstream", "status"}),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_endpoint_inflight",
			Help: "Current in-flight RPCs by upstream endpoint.",
		}, []string{"source", "destination", "method", "upstream"}),
		latencyEWMA: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_endpoint_latency_ewma_seconds",
			Help: "EWMA latency by upstream endpoint.",
		}, []string{"source", "destination", "method", "upstream"}),
	}

	for _, collector := range []prometheus.Collector{m.requests, m.latency, m.inflight, m.latencyEWMA} {
		if err := reg.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func DefaultPrometheusMetrics() *PrometheusMetrics {
	defaultPrometheusOnce.Do(func() {
		metrics, err := NewPrometheusMetrics(prometheus.DefaultRegisterer)
		if err != nil {
			panic(err)
		}
		defaultPrometheusMetrics = metrics
	})
	return defaultPrometheusMetrics
}

func (m *PrometheusMetrics) Record(obs Observation, latencyEWMA time.Duration, inflight int64) {
	if m == nil {
		return
	}
	obs.Source = firstNonEmpty(obs.Source, "unknown")
	obs.Destination = firstNonEmpty(obs.Destination, "unknown")
	obs.Method = firstNonEmpty(obs.Method, "unknown")
	obs.Upstream = firstNonEmpty(obs.Upstream, "unknown")
	obs.Status = normalizeStatus(obs.Status)

	m.requests.WithLabelValues(obs.Source, obs.Destination, obs.Method, obs.Upstream, obs.Status).Inc()
	m.latency.WithLabelValues(obs.Source, obs.Destination, obs.Method, obs.Upstream, obs.Status).Observe(obs.Latency.Seconds())
	m.inflight.WithLabelValues(obs.Source, obs.Destination, obs.Method, obs.Upstream).Set(float64(inflight))
	m.latencyEWMA.WithLabelValues(obs.Source, obs.Destination, obs.Method, obs.Upstream).Set(latencyEWMA.Seconds())
}

func MustDefaultPrometheusMetrics() *PrometheusMetrics {
	return DefaultPrometheusMetrics()
}
