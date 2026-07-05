package telemetry

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestPrometheusMetricsExportsRPCAndEndpointSeries locks the prometheus metrics exports rpc and endpoint series contract so future changes do not regress it.
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
		EndpointID:  "user-a",
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

// TestPrometheusMetricsDefaultsToEndpointIDLabels locks the prometheus metrics defaults to endpoint id labels contract so future changes do not regress it.
func TestPrometheusMetricsDefaultsToEndpointIDLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(reg)
	if err != nil {
		t.Fatalf("create prometheus metrics: %v", err)
	}

	metrics.Record(Observation{
		Source:      "frontend",
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     75 * time.Millisecond,
	}, 75*time.Millisecond, 0)
	metrics.Record(Observation{
		Source:      "frontend",
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7002",
		Status:      "OK",
		Latency:     75 * time.Millisecond,
	}, 75*time.Millisecond, 0)

	family := mustMetricFamily(t, reg, "aegis_rpc_requests_total")
	if len(family.Metric) != 1 {
		t.Fatalf("expected one requests series for one endpoint_id, got %d", len(family.Metric))
	}
	assertLabelValue(t, family.Metric[0], "endpoint_id", "user-a")
	assertNoLabel(t, family.Metric[0], "endpoint_address")
}

// TestPrometheusMetricsCanExportEndpointAddressForDebugging locks the prometheus metrics can export endpoint address for debugging contract so future changes do not regress it.
func TestPrometheusMetricsCanExportEndpointAddressForDebugging(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics, err := NewPrometheusMetrics(reg, WithPrometheusEndpointAddressLabels())
	if err != nil {
		t.Fatalf("create prometheus metrics: %v", err)
	}

	metrics.Record(Observation{
		Source:      "frontend",
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     75 * time.Millisecond,
	}, 75*time.Millisecond, 0)
	metrics.Record(Observation{
		Source:      "frontend",
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7002",
		Status:      "OK",
		Latency:     75 * time.Millisecond,
	}, 75*time.Millisecond, 0)

	family := mustMetricFamily(t, reg, "aegis_rpc_requests_total")
	if len(family.Metric) != 2 {
		t.Fatalf("expected address debug mode to keep two request series, got %d", len(family.Metric))
	}
	seen := map[string]bool{}
	for _, metric := range family.Metric {
		assertLabelValue(t, metric, "endpoint_id", "user-a")
		for _, label := range metric.Label {
			if label.GetName() == "endpoint_address" {
				seen[label.GetValue()] = true
			}
		}
	}
	if !seen["127.0.0.1:7001"] || !seen["127.0.0.1:7002"] {
		t.Fatalf("expected endpoint address labels for both debug series, got %+v", seen)
	}
}

// TestPrometheusMetricsRecordCompatibilityUsesCachedRow locks the prometheus metrics record compatibility uses cached row contract so future changes do not regress it.
func TestPrometheusMetricsRecordCompatibilityUsesCachedRow(t *testing.T) {
	metrics, err := NewPrometheusMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("create prometheus metrics: %v", err)
	}
	obs := Observation{
		Source:      "frontend",
		Destination: "user-service",
		Method:      "/demo.shop.v1.UserService/GetUser",
		EndpointID:  "user-a",
		Upstream:    "127.0.0.1:7001",
		Status:      "OK",
		Latency:     75 * time.Millisecond,
	}
	metrics.Record(obs, 75*time.Millisecond, 0)

	allocs := testing.AllocsPerRun(1000, func() {
		metrics.Record(obs, 75*time.Millisecond, 0)
	})
	if allocs != 0 {
		t.Fatalf("expected warmed compatibility Record to allocate zero objects, got %.2f", allocs)
	}
}

// hasMetricFamily provides the shared has metric family helper for recorder aggregation.
func hasMetricFamily(families []*dto.MetricFamily, name string) bool {
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}

// mustMetricFamily returns the requested value and fails the test immediately when setup is invalid.
func mustMetricFamily(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather prometheus metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("expected metric family %s", name)
	return nil
}

// assertLabelValue provides the shared assert label value helper for recorder aggregation.
func assertLabelValue(t *testing.T, metric *dto.Metric, name, want string) {
	t.Helper()
	for _, label := range metric.Label {
		if label.GetName() == name {
			if label.GetValue() != want {
				t.Fatalf("expected label %s=%q, got %q", name, want, label.GetValue())
			}
			return
		}
	}
	t.Fatalf("expected label %s=%q", name, want)
}

// assertNoLabel provides the shared assert no label helper for recorder aggregation.
func assertNoLabel(t *testing.T, metric *dto.Metric, name string) {
	t.Helper()
	for _, label := range metric.Label {
		if label.GetName() == name {
			t.Fatalf("expected no %s label, got %q", name, label.GetValue())
		}
	}
}
