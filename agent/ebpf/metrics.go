package ebpf

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

type CollectorMetrics struct {
	droppedEvents *prometheus.CounterVec
}

var (
	defaultCollectorMetricsOnce sync.Once
	defaultCollectorMetrics   *CollectorMetrics
)

func NewCollectorMetrics(reg prometheus.Registerer) (*CollectorMetrics, error) {
	m := &CollectorMetrics{
		droppedEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aegis_ebpf_events_dropped_total",
			Help: "eBPF TCP events dropped before user-space processing.",
		}, []string{"reason"}),
	}
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	if err := reg.Register(m.droppedEvents); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			m.droppedEvents = are.ExistingCollector.(*prometheus.CounterVec)
		} else {
			return nil, err
		}
	}
	return m, nil
}

func DefaultCollectorMetrics() *CollectorMetrics {
	defaultCollectorMetricsOnce.Do(func() {
		metrics, err := NewCollectorMetrics(prometheus.DefaultRegisterer)
		if err != nil {
			panic(err)
		}
		defaultCollectorMetrics = metrics
	})
	return defaultCollectorMetrics
}

func (m *CollectorMetrics) IncDropped(reason string) {
	if m == nil {
		return
	}
	m.droppedEvents.WithLabelValues(reason).Inc()
}
