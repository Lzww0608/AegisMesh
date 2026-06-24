package telemetry

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type PrometheusOption func(*prometheusConfig)

type prometheusConfig struct {
	includeEndpointAddress bool
}

type PrometheusMetrics struct {
	requests               *prometheus.CounterVec
	latency                *prometheus.HistogramVec
	inflight               *prometheus.GaugeVec
	latencyEWMA            *prometheus.GaugeVec
	includeEndpointAddress bool
	rows                   sync.Map
}

type prometheusMetricKey struct {
	source          string
	destination     string
	method          string
	endpointID      string
	endpointAddress string
}

type prometheusRowMetrics struct {
	owner       *PrometheusMetrics
	rowValues   []string
	ok          prometheusStatusMetrics
	inflight    prometheus.Gauge
	latencyEWMA prometheus.Gauge
	mu          sync.Mutex
	statuses    map[string]prometheusStatusMetrics
}

type prometheusStatusMetrics struct {
	requests prometheus.Counter
	latency  prometheus.Observer
}

var (
	defaultPrometheusOnce    sync.Once
	defaultPrometheusMetrics *PrometheusMetrics
)

func WithPrometheusEndpointAddressLabels() PrometheusOption {
	return func(config *prometheusConfig) {
		config.includeEndpointAddress = true
	}
}

func NewPrometheusMetrics(reg prometheus.Registerer, options ...PrometheusOption) (*PrometheusMetrics, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	config := prometheusConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}

	rowLabels := []string{"source", "destination", "method", "endpoint_id"}
	if config.includeEndpointAddress {
		rowLabels = append(rowLabels, "endpoint_address")
	}
	requestLabels := append(append([]string{}, rowLabels...), "status")

	m := &PrometheusMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_rpc_requests_total",
			Help: "Total RPC requests observed by the Aegis SDK.",
		}, requestLabels),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aegis_rpc_latency_seconds",
			Help:    "RPC latency observed by the Aegis SDK.",
			Buckets: prometheus.DefBuckets,
		}, requestLabels),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_endpoint_inflight",
			Help: "Current in-flight RPCs by upstream endpoint.",
		}, rowLabels),
		latencyEWMA: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_endpoint_latency_ewma_seconds",
			Help: "EWMA latency by upstream endpoint.",
		}, rowLabels),
		includeEndpointAddress: config.includeEndpointAddress,
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

func (m *PrometheusMetrics) CreateRowMetrics(labels MetricLabels) RowMetrics {
	if m == nil {
		return nil
	}
	labels = normalizeMetricLabels(labels)
	key := m.metricKey(labels)
	if cached, ok := m.rows.Load(key); ok {
		return cached.(RowMetrics)
	}
	created := m.newRowMetrics(labels)
	actual, _ := m.rows.LoadOrStore(key, created)
	return actual.(RowMetrics)
}

func (m *PrometheusMetrics) Record(obs Observation, latencyEWMA time.Duration, inflight int64) {
	if m == nil {
		return
	}
	bound := m.CreateRowMetrics(MetricLabels{
		Source:          obs.Source,
		Destination:     obs.Destination,
		Method:          obs.Method,
		EndpointID:      obs.EndpointID,
		EndpointAddress: obs.Upstream,
	})
	if bound != nil {
		bound.Record(normalizeStatus(obs.Status), obs.Latency, latencyEWMA, inflight)
	}
}

func (m *PrometheusMetrics) newRowMetrics(labels MetricLabels) RowMetrics {
	rowValues := m.rowLabelValues(labels)
	return &prometheusRowMetrics{
		owner:       m,
		rowValues:   rowValues,
		ok:          m.bindStatus(rowValues, "OK"),
		inflight:    m.inflight.WithLabelValues(rowValues...),
		latencyEWMA: m.latencyEWMA.WithLabelValues(rowValues...),
	}
}

func (m *PrometheusMetrics) rowLabelValues(labels MetricLabels) []string {
	if m.includeEndpointAddress {
		return []string{labels.Source, labels.Destination, labels.Method, labels.EndpointID, labels.EndpointAddress}
	}
	return []string{labels.Source, labels.Destination, labels.Method, labels.EndpointID}
}

func (m *PrometheusMetrics) metricKey(labels MetricLabels) prometheusMetricKey {
	key := prometheusMetricKey{
		source:      labels.Source,
		destination: labels.Destination,
		method:      labels.Method,
		endpointID:  labels.EndpointID,
	}
	if m.includeEndpointAddress {
		key.endpointAddress = labels.EndpointAddress
	}
	return key
}

func (m *PrometheusMetrics) bindStatus(rowValues []string, status string) prometheusStatusMetrics {
	values := valuesWithStatus(rowValues, status)
	return prometheusStatusMetrics{
		requests: m.requests.WithLabelValues(values...),
		latency:  m.latency.WithLabelValues(values...),
	}
}

func (m *prometheusRowMetrics) Record(status string, latency time.Duration, latencyEWMA time.Duration, inflight int64) {
	if m == nil {
		return
	}
	statusMetrics := m.statusMetrics(normalizeStatus(status))
	statusMetrics.requests.Inc()
	statusMetrics.latency.Observe(latency.Seconds())
	m.inflight.Set(float64(inflight))
	m.latencyEWMA.Set(latencyEWMA.Seconds())
}

func (m *prometheusRowMetrics) statusMetrics(status string) prometheusStatusMetrics {
	if status == "OK" {
		return m.ok
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statuses == nil {
		m.statuses = make(map[string]prometheusStatusMetrics)
	}
	metrics, ok := m.statuses[status]
	if !ok {
		metrics = m.owner.bindStatus(m.rowValues, status)
		m.statuses[status] = metrics
	}
	return metrics
}

func normalizeMetricLabels(labels MetricLabels) MetricLabels {
	labels.Source = firstNonEmpty(labels.Source, "unknown")
	labels.Destination = firstNonEmpty(labels.Destination, "unknown")
	labels.Method = firstNonEmpty(labels.Method, "unknown")
	labels.EndpointID = firstNonEmpty(labels.EndpointID, "unknown")
	labels.EndpointAddress = firstNonEmpty(labels.EndpointAddress, "unknown")
	return labels
}

func valuesWithStatus(rowValues []string, status string) []string {
	values := make([]string, len(rowValues)+1)
	copy(values, rowValues)
	values[len(rowValues)] = status
	return values
}

func MustDefaultPrometheusMetrics() *PrometheusMetrics {
	return DefaultPrometheusMetrics()
}
