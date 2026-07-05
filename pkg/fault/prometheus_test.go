package fault

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestPrometheusHealthMetricsExportsSlowScoreAndState locks the prometheus health metrics exports slow score and state contract so future changes do not regress it.
func TestPrometheusHealthMetricsExportsSlowScoreAndState(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics, err := NewPrometheusHealthMetrics(reg)
	if err != nil {
		t.Fatalf("create health metrics: %v", err)
	}

	metrics.RecordHealth(EndpointHealth{
		Service:    "user-service",
		InstanceID: "user-c",
		Address:    "127.0.0.1:7003",
		State:      StateEjected,
		SlowScore:  3.2,
	})

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	if !hasMetricFamily(families, "aegis_endpoint_slow_score") {
		t.Fatalf("expected aegis_endpoint_slow_score metric family")
	}
	if !hasMetricFamily(families, "aegis_endpoint_state") {
		t.Fatalf("expected aegis_endpoint_state metric family")
	}
}

// hasMetricFamily provides the shared has metric family helper for fault-state scoring and recovery.
func hasMetricFamily(families []*dto.MetricFamily, name string) bool {
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}
