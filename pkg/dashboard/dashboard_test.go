package dashboard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGrafanaDashboardContainsRequiredAegisPanels(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/grafana/aegismesh-overview.json")
	if err != nil {
		t.Fatalf("read dashboard: %v", err)
	}

	var dashboard map[string]any
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatalf("parse dashboard json: %v", err)
	}
	if dashboard["title"] != "AegisMesh Overview" {
		t.Fatalf("unexpected dashboard title: %v", dashboard["title"])
	}

	text := string(raw)
	for _, metric := range []string{
		"aegis_rpc_requests_total",
		"aegis_rpc_latency_seconds_bucket",
		"aegis_endpoint_slow_score",
		"aegis_endpoint_state",
		"aegis_endpoint_latency_ewma_seconds",
	} {
		if !strings.Contains(text, metric) {
			t.Fatalf("dashboard missing metric query %s", metric)
		}
	}
}
