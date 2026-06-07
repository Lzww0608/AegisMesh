package telemetry

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestPrometheusMetricsExportsRPCAndEndpointSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(reg)
	if err != nil {
		t.Fatalf("create prometheus metrics: %v", err)
	}

	metrics.Record(Observation{
		Source:      "frontend",
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     75 * time.Millisecond,
	}, 75*time.Millisecond, 0)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather prometheus metrics: %v", err)
	}

	if !hasMetricFamily(families, "aegis_rpc_requests_total") {
		t.Fatalf("expected aegis_rpc_requests_total metric family")
	}
	if !hasMetricFamily(families, "aegis_rpc_latency_seconds") {
		t.Fatalf("expected aegis_rpc_latency_seconds metric family")
	}
	if !hasMetricFamily(families, "aegis_endpoint_latency_ewma_seconds") {
		t.Fatalf("expected aegis_endpoint_latency_ewma_seconds metric family")
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
