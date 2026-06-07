package experiments_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReproducibleExperimentAssetsExist(t *testing.T) {
	root := repoRoot(t)
	requiredFiles := []string{
		"docker-compose.demo.yml",
		"docker-compose.experiments.yml",
		"docker-compose.observability.yml",
		"Makefile",
		"experiments/config/experiment_matrix.json",
		"experiments/scripts/check_results.py",
		"experiments/scripts/merge_results.py",
		"experiments/scripts/run_required_experiments.sh",
		"experiments/scripts/run_recovery_state_experiment.sh",
		"experiments/scripts/run_retry_repetitions.sh",
		"experiments/scripts/run_single_machine_experiments.sh",
		"experiments/scripts/run_retry_amplification.py",
		"experiments/scripts/run_sustained_load.py",
		"scripts/run_demo.sh",
		"scripts/run_fault_experiment.sh",
		"scripts/reset_faults.sh",
		"experiments/scripts/run_baseline.sh",
		"experiments/scripts/run_slow_fault.sh",
		"experiments/scripts/run_retry_budget.sh",
		"experiments/results/README.md",
		"experiments/results/latency_schema.csv",
		"experiments/results/recovery_schema.csv",
		"experiments/results/retry_schema.csv",
		"experiments/notebooks/plot_latency.py",
		"docs/evaluation.md",
		"docs/experiments.md",
	}

	for _, rel := range requiredFiles {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("expected reproducibility asset %s: %v", rel, err)
		}
		if info.IsDir() {
			t.Fatalf("expected %s to be a file", rel)
		}
	}
}

func TestMakefileExposesDemoAndExperimentTargets(t *testing.T) {
	makefile := readText(t, "Makefile")
	for _, target := range []string{
		"demo-up:",
		"demo-down:",
		"load:",
		"inject-delay:",
		"inject-loss:",
		"reset-faults:",
		"bench:",
		"bench-required:",
		"bench-recovery-state:",
		"bench-retry-repeat:",
		"bench-single-machine:",
		"check-results:",
		"merge-results:",
		"report:",
		"experiments-up:",
		"experiments-down:",
		"record-recovery:",
		"dashboard:",
	} {
		if !strings.Contains(makefile, target) {
			t.Fatalf("expected Makefile target %s", target)
		}
	}
}

func TestExperimentComposeDefinesRetryFaultTopology(t *testing.T) {
	compose := readText(t, "docker-compose.experiments.yml")
	required := []string{
		"retry-user-service",
		"frontend-retry-budgeted",
		"frontend-retry-unbudgeted",
		"--user-service",
		"retry-user-service",
		"8086:8086",
	}
	for _, want := range required {
		if !strings.Contains(compose, want) {
			t.Fatalf("expected experiment compose to contain %q", want)
		}
	}
}

func TestSingleMachineGuideExplainsMergeWorkflow(t *testing.T) {
	doc := readText(t, "docs/experiments.md")
	required := []string{
		"single machine",
		"RUNS_DIR=experiments/results/runs",
		"make merge-results",
		"experiments/results/combined",
	}
	for _, want := range required {
		if !strings.Contains(doc, want) {
			t.Fatalf("expected experiment guide to contain %q", want)
		}
	}
}

func TestRecoveryExperimentDocumentsAggressiveThresholds(t *testing.T) {
	compose := readText(t, "docker-compose.experiments.yml")
	requiredCompose := []string{
		"AEGIS_DEGRADED_THRESHOLD",
		"--health-degraded-threshold",
		"--health-eject-threshold",
		"--health-consecutive-windows",
	}
	for _, want := range requiredCompose {
		if !strings.Contains(compose, want) {
			t.Fatalf("expected experiment compose to contain %q", want)
		}
	}

	doc := readText(t, "docs/experiments.md")
	requiredDoc := []string{
		"bench-retry-repeat",
		"bench-recovery-state",
		"AEGIS_DEGRADED_THRESHOLD=0.05",
		"RECOVERY_DURATION=90s",
	}
	for _, want := range requiredDoc {
		if !strings.Contains(doc, want) {
			t.Fatalf("expected experiment guide to contain %q", want)
		}
	}
}

func TestExperimentMatrixCoversRequiredComparisons(t *testing.T) {
	var matrix struct {
		Scenarios []struct {
			Experiment string `json:"experiment"`
			Variant    string `json:"variant"`
		} `json:"scenarios"`
	}
	data := readText(t, "experiments/config/experiment_matrix.json")
	if err := json.Unmarshal([]byte(data), &matrix); err != nil {
		t.Fatalf("parse experiment matrix: %v", err)
	}
	got := make(map[string]bool)
	for _, scenario := range matrix.Scenarios {
		got[scenario.Experiment+"/"+scenario.Variant] = true
	}
	required := []string{
		"baseline/no_mesh",
		"baseline/aegismesh",
		"single_instance_delay/round_robin",
		"single_instance_delay/adaptive_p2c",
		"cpu_throttle/static_threshold",
		"cpu_throttle/slow_score",
		"retry_budget/without_budget",
		"retry_budget/with_budget",
		"packet_loss/no_ebpf_network_score",
		"packet_loss/ebpf_network_score",
		"recovery_curve/adaptive_p2c",
	}
	for _, want := range required {
		if !got[want] {
			t.Fatalf("expected experiment matrix to include %s", want)
		}
	}
}

func TestExperimentSchemasDeclareComparableMetrics(t *testing.T) {
	assertCSVHeader(t, "experiments/results/latency_schema.csv", "experiment,variant,window_start_unix_ms,window_end_unix_ms,requests,throughput_rps,latency_p50_ms,latency_p95_ms,latency_p99_ms,error_rate")
	assertCSVHeader(t, "experiments/results/recovery_schema.csv", "experiment,variant,timestamp_unix_ms,endpoint,slow_score,p99_latency_ms,route_weight,state")
	assertCSVHeader(t, "experiments/results/retry_schema.csv", "experiment,variant,window_start_unix_ms,original_requests,retry_attempts,total_attempts,retry_amplification,error_rate")
}

func TestEvaluationDocumentNamesRequiredBenchmarkFigures(t *testing.T) {
	doc := readText(t, "docs/evaluation.md")
	requiredSections := []string{
		"Baseline Throughput And Latency",
		"Slow Instance Delay",
		"CPU Throttle Slow Fault",
		"Retry Budget Amplification",
		"Fault Recovery Curve",
		"Do Not Fabricate Results",
	}
	for _, section := range requiredSections {
		if !strings.Contains(doc, section) {
			t.Fatalf("expected evaluation document section %q", section)
		}
	}
}

func assertCSVHeader(t *testing.T, rel, want string) {
	t.Helper()
	text := readText(t, rel)
	firstLine := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if firstLine != want {
		t.Fatalf("unexpected %s header\nwant: %s\n got: %s", rel, want, firstLine)
	}
}

func readText(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}
