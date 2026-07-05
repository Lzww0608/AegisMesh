package ebpf

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestCollectorMetricsDroppedEvents locks the collector metrics dropped events contract so future changes do not regress it.
func TestCollectorMetricsDroppedEvents(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics, err := NewCollectorMetrics(reg)
	if err != nil {
		t.Fatalf("new collector metrics: %v", err)
	}
	metrics.IncDropped("channel_full")

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	found := false
	for _, family := range families {
		if family.GetName() != "aegis_ebpf_events_dropped_total" {
			continue
		}
		found = true
		if family.GetMetric()[0].GetCounter().GetValue() != 1 {
			t.Fatalf("expected dropped counter 1, got %+v", family)
		}
	}
	if !found {
		t.Fatalf("expected aegis_ebpf_events_dropped_total metric")
	}
}
