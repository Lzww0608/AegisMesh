package deathstarbench

import (
	"strings"
	"testing"
)

// TestParseConfigReadsSocialNetworkIntegration locks the parse config reads social network integration contract so future changes do not regress it.
func TestParseConfigReadsSocialNetworkIntegration(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
benchmark: social-network
repo: https://github.com/delimitrou/DeathStarBench
compose_file: socialNetwork/docker-compose.yml
controller: 127.0.0.1:9000
frontend:
  url: http://localhost:8080
  workload: wrk2
services:
  nginx-thrift:
    aegis_name: frontend
    port: 8080
  user-service:
    aegis_name: user-service
    port: 9090
`))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.Benchmark != "social-network" || cfg.Services["user-service"].AegisName != "user-service" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

// TestIntegrationPlanIncludesControllerAndWorkloadCommands locks the integration plan includes controller and workload commands contract so future changes do not regress it.
func TestIntegrationPlanIncludesControllerAndWorkloadCommands(t *testing.T) {
	cfg := Config{
		Benchmark:   "social-network",
		Repo:        "https://github.com/delimitrou/DeathStarBench",
		ComposeFile: "socialNetwork/docker-compose.yml",
		Controller:  "127.0.0.1:9000",
		Frontend: FrontendConfig{
			URL:      "http://localhost:8080",
			Workload: "wrk2",
		},
		Services: map[string]ServiceMapping{
			"nginx-thrift": {AegisName: "frontend", Port: 8080},
		},
	}

	plan := cfg.Plan()
	if !strings.Contains(plan.ComposeCommand, "socialNetwork/docker-compose.yml") {
		t.Fatalf("expected compose file in plan: %+v", plan)
	}
	if !strings.Contains(plan.Environment["AEGIS_CONTROLLER"], "127.0.0.1:9000") {
		t.Fatalf("expected controller env, got %+v", plan.Environment)
	}
	if !strings.Contains(plan.WorkloadCommand, "wrk2") || !strings.Contains(plan.WorkloadCommand, "http://localhost:8080") {
		t.Fatalf("expected workload command, got %q", plan.WorkloadCommand)
	}
}
