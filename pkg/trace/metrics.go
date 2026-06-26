package trace

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type PrometheusMetrics struct {
	droppedTraces *prometheus.CounterVec
}

var (
	defaultPrometheusOnce    sync.Once
	defaultPrometheusMetrics *PrometheusMetrics
)

func NewPrometheusMetrics(reg prometheus.Registerer) (*PrometheusMetrics, error) {
	m := &PrometheusMetrics{
		droppedTraces: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_dropped_traces_total",
			Help: "Trace records dropped before JSONL persistence.",
		}, []string{"reason"}),
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	if err := reg.Register(m.droppedTraces); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			m.droppedTraces = are.ExistingCollector.(*prometheus.CounterVec)
		} else {
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

func (m *PrometheusMetrics) IncDropped(reason string) {
	if m == nil {
		return
	}
	m.droppedTraces.WithLabelValues(reason).Inc()
}
