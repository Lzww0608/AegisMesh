package fault

import "github.com/prometheus/client_golang/prometheus"

type PrometheusHealthMetrics struct {
	slowScore *prometheus.GaugeVec
	state     *prometheus.GaugeVec
}

func NewPrometheusHealthMetrics(reg prometheus.Registerer) (*PrometheusHealthMetrics, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &PrometheusHealthMetrics{
		slowScore: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_endpoint_slow_score",
			Help: "Current slow fault score by endpoint.",
		}, []string{"service", "instance", "endpoint"}),
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "aegis_endpoint_state",
			Help: "Endpoint state as one-hot gauges labelled by state.",
		}, []string{"service", "instance", "endpoint", "state"}),
	}

	for _, collector := range []prometheus.Collector{m.slowScore, m.state} {
		if err := reg.Register(collector); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *PrometheusHealthMetrics) RecordHealth(health EndpointHealth) {
	if m == nil {
		return
	}
	m.slowScore.WithLabelValues(health.Service, health.InstanceID, health.Address).Set(health.SlowScore)
	for _, state := range []EndpointState{StateHealthy, StateDegraded, StateEjected, StateProbing, StateDead} {
		value := 0.0
		if health.State == state {
			value = 1
		}
		m.state.WithLabelValues(health.Service, health.InstanceID, health.Address, string(state)).Set(value)
	}
}
