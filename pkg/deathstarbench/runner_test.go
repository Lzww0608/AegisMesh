package deathstarbench

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const wrkFixture = `Running 1s test @ http://localhost:8080
  1 threads and 1 connections
  Latency Distribution
     50%    1.23ms
     95%    4.56ms
     99%    7.89ms
  1234 requests in 1.00s, 1.00MB read
Requests/sec:   1234.50
`

type fakeExecutor struct {
	commands []Command
	failName string
}

func (f *fakeExecutor) Run(_ context.Context, command Command) (CommandResult, error) {
	f.commands = append(f.commands, command)
	if command.Name == f.failName {
		return CommandResult{ExitCode: 7, Stderr: "boom"}, errors.New("stage failed")
	}
	switch command.Name {
	case "workload":
		return CommandResult{Stdout: wrkFixture}, nil
	case "collect_compose_logs":
		return CommandResult{Stdout: "nginx-thrift started\n"}, nil
	case "collect_compose_ps":
		return CommandResult{Stdout: `{"Name":"nginx-thrift","State":"running"}`}, nil
	default:
		return CommandResult{Stdout: command.Name + " ok\n"}, nil
	}
}

func TestRunnerRunCreatesArtifactsAndValidation(t *testing.T) {
	repoDir := makeRepo(t)
	outDir := t.TempDir()
	exec := &fakeExecutor{}
	cfg := testConfig()

	manifest, err := Runner{}.Run(context.Background(), cfg, RunOptions{
		RepoDir:   repoDir,
		OutputDir: outDir,
		Executor:  exec,
		WaitReady: func(context.Context, string, time.Duration) error { return nil },
		Now:       fixedNow,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if manifest.Status != "completed" {
		t.Fatalf("unexpected status: %+v", manifest)
	}
	for _, name := range []string{ArtifactPlan, ArtifactOverlay, ArtifactManifest, ArtifactWorkloadStdout, ArtifactComposeLogs, ArtifactLatencyCSV} {
		if info, err := os.Stat(filepath.Join(outDir, name)); err != nil || info.Size() == 0 {
			t.Fatalf("expected non-empty artifact %s, info=%v err=%v", name, info, err)
		}
	}
	assertCommandOrder(t, exec.commands, []string{"compose_up", "workload", "collect_compose_ps", "collect_compose_logs", "compose_down"})
	if got := exec.commands[1].Env["AEGIS_SERVICE_MAP"]; !strings.Contains(got, "user-service=user-service:9090") {
		t.Fatalf("workload missing service map env: %q", got)
	}

	validation, err := ValidateRunDir(outDir, ValidationOptions{})
	if err != nil {
		t.Fatalf("validate: %v (%+v)", err, validation)
	}
	if validation.Status != "pass" {
		t.Fatalf("expected pass validation: %+v", validation)
	}
}

func TestRunnerCleansUpAfterWorkloadFailure(t *testing.T) {
	repoDir := makeRepo(t)
	exec := &fakeExecutor{failName: "workload"}

	manifest, err := Runner{}.Run(context.Background(), testConfig(), RunOptions{
		RepoDir:   repoDir,
		OutputDir: t.TempDir(),
		Executor:  exec,
		WaitReady: func(context.Context, string, time.Duration) error { return nil },
		Now:       fixedNow,
	})
	if err == nil {
		t.Fatal("expected workload failure")
	}
	if manifest.Status != "failed" {
		t.Fatalf("unexpected status: %+v", manifest)
	}
	assertCommandOrder(t, exec.commands, []string{"compose_up", "workload", "collect_compose_ps", "collect_compose_logs", "compose_down"})
}
func TestRunnerUsesReadyURLForReadiness(t *testing.T) {
	cfg := testConfig()
	cfg.Frontend.URL = "http://localhost:8080/social"
	cfg.Frontend.ReadyURL = "http://localhost:8080/health"
	var gotReadyURL string

	_, err := Runner{}.Run(context.Background(), cfg, RunOptions{
		RepoDir:   makeRepo(t),
		OutputDir: t.TempDir(),
		Executor:  &fakeExecutor{},
		WaitReady: func(_ context.Context, url string, _ time.Duration) error {
			gotReadyURL = url
			return nil
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotReadyURL != cfg.Frontend.ReadyURL {
		t.Fatalf("runner waited on %q, want ready_url %q", gotReadyURL, cfg.Frontend.ReadyURL)
	}
}
func TestRunnerNormalizesRelativePathsForExternalRepo(t *testing.T) {
	workspace := t.TempDir()
	chdir(t, workspace)
	repoDir := filepath.Join(workspace, "DeathStarBench")
	writeRepo(t, repoDir)
	outDir := filepath.Join("artifacts", "run1")
	exec := &fakeExecutor{}

	manifest, err := Runner{}.Run(context.Background(), testConfig(), RunOptions{
		RepoDir:   "DeathStarBench",
		OutputDir: outDir,
		Executor:  exec,
		WaitReady: func(context.Context, string, time.Duration) error { return nil },
		Now:       fixedNow,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if manifest.RepoDir != repoDir {
		t.Fatalf("manifest repo dir = %q, want %q", manifest.RepoDir, repoDir)
	}
	if len(exec.commands) == 0 {
		t.Fatal("expected compose command")
	}
	composeLine := exec.commands[0].Line
	overlayPath := filepath.Join(workspace, outDir, ArtifactOverlay)
	composePath := filepath.Join(repoDir, testConfig().ComposeFile)
	if !strings.Contains(composeLine, overlayPath) || !strings.Contains(composeLine, composePath) {
		t.Fatalf("compose command did not use absolute paths: %s", composeLine)
	}
	if exec.commands[0].Dir != repoDir {
		t.Fatalf("compose dir = %q, want %q", exec.commands[0].Dir, repoDir)
	}
	if _, err := os.Stat(overlayPath); err != nil {
		t.Fatalf("expected overlay at absolute output dir: %v", err)
	}
}

func TestWaitHTTPReadyRejects404(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	err := waitHTTPReady(context.Background(), server.URL, 600*time.Millisecond)
	if err == nil {
		t.Fatal("expected 404 readiness response to fail")
	}
	if !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("expected status detail, got %v", err)
	}
}

func TestValidateRunDirRejectsPlanOnlyAndRequiresGovernedTraffic(t *testing.T) {
	outDir := t.TempDir()
	if err := writeJSON(filepath.Join(outDir, ArtifactPlan), testConfig().Plan()); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRunDir(outDir, ValidationOptions{}); err == nil {
		t.Fatal("expected plan-only validation failure")
	}

	repoDir := makeRepo(t)
	_, err := Runner{}.Run(context.Background(), testConfig(), RunOptions{
		RepoDir:   repoDir,
		OutputDir: outDir,
		Executor:  &fakeExecutor{},
		WaitReady: func(context.Context, string, time.Duration) error { return nil },
		Now:       fixedNow,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	validation, err := ValidateRunDir(outDir, ValidationOptions{RequireGovernedTraffic: true})
	if err == nil {
		t.Fatalf("expected governed-traffic validation failure: %+v", validation)
	}

	var manifest RunManifest
	if err := readJSON(filepath.Join(outDir, ArtifactManifest), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Injection.TrafficGovernance = TrafficGovernanceSidecarProxy
	if err := writeJSON(filepath.Join(outDir, ArtifactManifest), manifest); err != nil {
		t.Fatal(err)
	}
	validation, err = ValidateRunDir(outDir, ValidationOptions{RequireGovernedTraffic: true})
	if err == nil {
		t.Fatalf("expected inconsistent governed-traffic manifest to fail: %+v", validation)
	}

	if err := os.Remove(filepath.Join(outDir, ArtifactComposePS)); err != nil {
		t.Fatal(err)
	}
	validation, err = ValidateRunDir(outDir, ValidationOptions{})
	if err == nil {
		t.Fatalf("expected missing compose ps artifact to fail: %+v", validation)
	}
	if err := os.WriteFile(filepath.Join(outDir, ArtifactComposePS), []byte(`{"Name":"nginx-thrift","State":"running"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(outDir, ArtifactPlan)); err != nil {
		t.Fatal(err)
	}
	validation, err = ValidateRunDir(outDir, ValidationOptions{})
	if err == nil {
		t.Fatalf("expected missing plan artifact to fail: %+v", validation)
	}
}

func TestParseWrkOutput(t *testing.T) {
	row, err := ParseWrkOutput(wrkFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if row.Requests != 1234 || row.ThroughputRPS != 1234.50 || row.LatencyP99MS != 7.89 {
		t.Fatalf("unexpected row: %+v", row)
	}
	if _, err := ParseWrkOutput("not wrk output"); err == nil {
		t.Fatal("expected malformed output error")
	}
}

func TestPlanSupportsExplicitWorkloadCommandAndReadyURL(t *testing.T) {
	cfg := testConfig()
	cfg.Frontend.Command = "wrk -t1 -c1 -d1s http://example.test"
	cfg.Frontend.ReadyURL = "http://example.test/health"
	plan := cfg.Plan()
	if plan.WorkloadCommand != cfg.Frontend.Command {
		t.Fatalf("unexpected workload command: %q", plan.WorkloadCommand)
	}
	if plan.ReadyURL != cfg.Frontend.ReadyURL {
		t.Fatalf("unexpected ready url: %q", plan.ReadyURL)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}
func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeRepo(t, dir)
	return dir
}

func writeRepo(t *testing.T, dir string) {
	t.Helper()
	composeDir := filepath.Join(dir, "socialNetwork")
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(composeDir, "docker-compose.yml"), []byte("services:\n  nginx-thrift:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testConfig() Config {
	return Config{
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
			"user-service": {AegisName: "user-service", Port: 9090},
		},
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
}

func assertCommandOrder(t *testing.T, commands []Command, want []string) {
	t.Helper()
	got := make([]string, 0, len(commands))
	for _, command := range commands {
		got = append(got, command.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("command count got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command order got %v want %v", got, want)
		}
	}
}
