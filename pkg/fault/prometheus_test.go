package fault

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

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

func hasMetricFamily(families []*dto.MetricFamily, name string) bool {
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}
