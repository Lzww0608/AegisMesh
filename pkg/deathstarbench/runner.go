package deathstarbench

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ArtifactPlan            = "integration_plan.json"
	ArtifactOverlay         = "aegis-compose.override.yml"
	ArtifactManifest        = "run_manifest.json"
	ArtifactWorkloadStdout  = "workload_stdout.txt"
	ArtifactWorkloadStderr  = "workload_stderr.txt"
	ArtifactComposePS       = "compose_ps.json"
	ArtifactComposeLogs     = "compose_logs.txt"
	ArtifactLatencyCSV      = "latency.csv"
	ArtifactComposeUpStdout = "compose_up_stdout.txt"
	ArtifactComposeUpStderr = "compose_up_stderr.txt"
	ArtifactDownStdout      = "compose_down_stdout.txt"
	ArtifactDownStderr      = "compose_down_stderr.txt"

	InjectionModeComposeEnvironmentOverlay = "compose_environment_overlay"
	InjectionModeSidecarProxy              = "sidecar_proxy"
	TrafficGovernanceMetadataOnly          = "metadata_only"
	TrafficGovernanceSidecarProxy          = "sidecar_proxy"
)

// Command describes one shell command in a DeathStarBench run.
type Command struct {
	Name string            `json:"name"`
	Line string            `json:"line"`
	Dir  string            `json:"dir,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// CommandResult captures command output without assuming Docker is available in tests.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Executor runs commands for Runner. Tests use a fake executor; production uses ShellExecutor.
type Executor interface {
	Run(ctx context.Context, command Command) (CommandResult, error)
}

// ShellExecutor executes Command.Line through the host shell.
type ShellExecutor struct{}

// Run executes the configured DeathStarBench stack and records runner artifacts.
func (ShellExecutor) Run(ctx context.Context, command Command) (CommandResult, error) {
	args := shellArgs(command.Line)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = command.Dir
	cmd.Env = os.Environ()
	for key, value := range command.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, err
	}
	result.ExitCode = -1
	return result, err
}

// shellArgs selects the host shell wrapper used by Executor.Run on Windows and POSIX hosts.
func shellArgs(line string) []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell.exe", "-NoProfile", "-Command", line}
	}
	return []string{"/bin/sh", "-c", line}
}

// ReadyFunc waits for the DeathStarBench frontend before the workload starts.
type ReadyFunc func(ctx context.Context, url string, timeout time.Duration) error

// RunOptions configures a real DeathStarBench run.
type RunOptions struct {
	RepoDir        string
	OutputDir      string
	ProjectName    string
	ComposeBin     string
	StartupTimeout time.Duration
	KeepStack      bool
	Executor       Executor
	WaitReady      ReadyFunc
	Now            func() time.Time
}

// RunManifest records runner artifacts and stage status; it is not benchmark evidence by itself.
type RunManifest struct {
	Benchmark   string            `json:"benchmark"`
	RepoDir     string            `json:"repo_dir"`
	ComposeFile string            `json:"compose_file"`
	ProjectName string            `json:"project_name"`
	StartedAt   string            `json:"started_at"`
	EndedAt     string            `json:"ended_at"`
	Status      string            `json:"status"`
	Error       string            `json:"error,omitempty"`
	Plan        IntegrationPlan   `json:"plan"`
	Injection   InjectionManifest `json:"injection"`
	Artifacts   map[string]string `json:"artifacts"`
	Stages      []StageResult     `json:"stages"`
}

// InjectionManifest describes how Aegis metadata or sidecars were attached to a run.
type InjectionManifest struct {
	Mode              string            `json:"mode"`
	TrafficGovernance string            `json:"traffic_governance"`
	AegisController   string            `json:"aegis_controller"`
	ServiceMap        string            `json:"service_map"`
	Services          []InjectedService `json:"services"`
	Notes             []string          `json:"notes,omitempty"`
}

// InjectedService records one DeathStarBench service mapped into AegisMesh injection metadata.
type InjectedService struct {
	DeathStarBenchService string `json:"deathstarbench_service"`
	AegisName             string `json:"aegis_name"`
	Port                  int    `json:"port"`
}

// StageResult records one runner command boundary, including timestamps, exit status, and captured output for post-run audit.
type StageResult struct {
	Name      string `json:"name"`
	Command   string `json:"command,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ValidationOptions controls filesystem validation without changing the runner artifact schema.
type ValidationOptions struct {
	RequireGovernedTraffic bool
}

// RunValidation carries run validation state for the DeathStarBench runner contract.
type RunValidation struct {
	Status string            `json:"status"`
	Checks []ValidationCheck `json:"checks"`
}

// ValidationCheck carries validation check state for the DeathStarBench runner contract.
type ValidationCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// Runner turns an IntegrationPlan into an opt-in Docker Compose run with artifacts.
type Runner struct{}

// Run executes the configured DeathStarBench stack and records runner artifacts.
func (Runner) Run(ctx context.Context, cfg Config, opts RunOptions) (RunManifest, error) {
	opts = normalizeRunOptions(cfg, opts)
	// The manifest documents runner work only; downstream docs must not treat it as measured governance results.
	manifest := RunManifest{
		Benchmark:   cfg.Benchmark,
		RepoDir:     opts.RepoDir,
		ComposeFile: cfg.ComposeFile,
		ProjectName: opts.ProjectName,
		StartedAt:   opts.Now().UTC().Format(time.RFC3339Nano),
		Status:      "running",
		Plan:        cfg.Plan(),
		Artifacts: map[string]string{
			"plan":            ArtifactPlan,
			"compose_overlay": ArtifactOverlay,
			"manifest":        ArtifactManifest,
		},
	}

	if err := normalizeRunPaths(&opts); err != nil {
		manifest.Status = "failed"
		manifest.Error = err.Error()
		manifest.EndedAt = opts.Now().UTC().Format(time.RFC3339Nano)
		return manifest, err
	}
	manifest.RepoDir = opts.RepoDir

	if err := preflight(cfg, opts); err != nil {
		manifest.Status = "failed"
		manifest.Error = err.Error()
		manifest.EndedAt = opts.Now().UTC().Format(time.RFC3339Nano)
		return manifest, err
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		manifest.Status = "failed"
		manifest.Error = err.Error()
		manifest.EndedAt = opts.Now().UTC().Format(time.RFC3339Nano)
		return manifest, err
	}

	manifest.Injection = buildInjectionManifest(cfg)
	if err := writeJSON(filepath.Join(opts.OutputDir, ArtifactPlan), manifest.Plan); err != nil {
		return finishRun(opts, manifest, err)
	}
	if err := os.WriteFile(filepath.Join(opts.OutputDir, ArtifactOverlay), []byte(renderComposeOverlay(cfg)), 0o644); err != nil {
		return finishRun(opts, manifest, err)
	}

	composeFile := filepath.Join(opts.RepoDir, cfg.ComposeFile)
	overlayFile := filepath.Join(opts.OutputDir, ArtifactOverlay)
	composeBase := fmt.Sprintf("%s -p %s -f %s -f %s", opts.ComposeBin, shellQuote(opts.ProjectName), shellQuote(composeFile), shellQuote(overlayFile))

	runErr := runStage(ctx, opts, &manifest, Command{
		Name: "compose_up",
		Line: composeBase + " up -d",
		Dir:  opts.RepoDir,
	}, ArtifactComposeUpStdout, ArtifactComposeUpStderr)

	if runErr == nil {
		runErr = waitReadyStage(ctx, opts, &manifest, readyURL(cfg))
	}
	if runErr == nil {
		manifest.Artifacts["workload_stdout"] = ArtifactWorkloadStdout
		manifest.Artifacts["workload_stderr"] = ArtifactWorkloadStderr
		runErr = runStage(ctx, opts, &manifest, Command{
			Name: "workload",
			Line: manifest.Plan.WorkloadCommand,
			Dir:  opts.RepoDir,
			Env:  manifest.Plan.Environment,
		}, ArtifactWorkloadStdout, ArtifactWorkloadStderr)
	}
	if runErr == nil {
		manifest.Artifacts["latency_csv"] = ArtifactLatencyCSV
		runErr = writeLatencyCSVFromWorkload(opts.OutputDir, cfg, opts.Now)
	}

	manifest.Artifacts["compose_ps"] = ArtifactComposePS
	_ = runStage(ctx, opts, &manifest, Command{
		Name: "collect_compose_ps",
		Line: composeBase + " ps --format json",
		Dir:  opts.RepoDir,
	}, ArtifactComposePS, "compose_ps_stderr.txt")

	manifest.Artifacts["compose_logs"] = ArtifactComposeLogs
	_ = runStage(ctx, opts, &manifest, Command{
		Name: "collect_compose_logs",
		Line: composeBase + " logs --no-color",
		Dir:  opts.RepoDir,
	}, ArtifactComposeLogs, "compose_logs_stderr.txt")

	if !opts.KeepStack {
		downErr := runStage(ctx, opts, &manifest, Command{
			Name: "compose_down",
			Line: composeBase + " down --remove-orphans",
			Dir:  opts.RepoDir,
		}, ArtifactDownStdout, ArtifactDownStderr)
		if runErr == nil {
			runErr = downErr
		}
	}

	return finishRun(opts, manifest, runErr)
}

// normalizeRunOptions fills run-option defaults so downstream logic sees one canonical form.
func normalizeRunOptions(cfg Config, opts RunOptions) RunOptions {
	if opts.ProjectName == "" {
		opts.ProjectName = "aegis-dsb-" + sanitizeName(cfg.Benchmark)
	}
	if opts.ComposeBin == "" {
		opts.ComposeBin = "docker compose"
	}
	if opts.StartupTimeout == 0 {
		opts.StartupTimeout = 90 * time.Second
	}
	if opts.Executor == nil {
		opts.Executor = ShellExecutor{}
	}
	if opts.WaitReady == nil {
		opts.WaitReady = waitHTTPReady
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

// normalizeRunPaths absolutizes run paths so subprocess and artifact code share one canonical form.
func normalizeRunPaths(opts *RunOptions) error {
	if opts.RepoDir != "" {
		abs, err := filepath.Abs(opts.RepoDir)
		if err != nil {
			return fmt.Errorf("abs repo dir: %w", err)
		}
		opts.RepoDir = abs
	}
	if opts.OutputDir != "" {
		abs, err := filepath.Abs(opts.OutputDir)
		if err != nil {
			return fmt.Errorf("abs output dir: %w", err)
		}
		opts.OutputDir = abs
	}
	return nil
}

// preflight rejects incomplete runner config before creating compose artifacts or starting workloads.
func preflight(cfg Config, opts RunOptions) error {
	if cfg.ComposeFile == "" {
		return errors.New("compose_file is required")
	}
	if len(cfg.Services) == 0 {
		return errors.New("at least one DeathStarBench service mapping is required")
	}
	if opts.RepoDir == "" {
		return errors.New("repo dir is required for --run")
	}
	if opts.OutputDir == "" {
		return errors.New("output dir is required for --run")
	}
	info, err := os.Stat(opts.RepoDir)
	if err != nil {
		return fmt.Errorf("stat repo dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo dir %q is not a directory", opts.RepoDir)
	}
	composePath := filepath.Join(opts.RepoDir, cfg.ComposeFile)
	if _, err := os.Stat(composePath); err != nil {
		return fmt.Errorf("stat compose file %q: %w", composePath, err)
	}
	return nil
}

// finishRun tears down the compose stack unless the caller requested artifact-preserving keepalive.
func finishRun(opts RunOptions, manifest RunManifest, runErr error) (RunManifest, error) {
	if runErr != nil {
		manifest.Status = "failed"
		manifest.Error = runErr.Error()
	} else {
		manifest.Status = "completed"
	}
	manifest.EndedAt = opts.Now().UTC().Format(time.RFC3339Nano)
	if opts.OutputDir != "" {
		if err := writeJSON(filepath.Join(opts.OutputDir, ArtifactManifest), manifest); err != nil && runErr == nil {
			return manifest, err
		}
	}
	return manifest, runErr
}

// runStage executes one external command and persists its stdout/stderr artifacts.
func runStage(ctx context.Context, opts RunOptions, manifest *RunManifest, command Command, stdoutName, stderrName string) error {
	stage := StageResult{
		Name:      command.Name,
		Command:   command.Line,
		StartedAt: opts.Now().UTC().Format(time.RFC3339Nano),
	}
	result, err := opts.Executor.Run(ctx, command)
	stage.EndedAt = opts.Now().UTC().Format(time.RFC3339Nano)
	stage.ExitCode = result.ExitCode
	if stdoutName != "" {
		stage.Stdout = stdoutName
		if writeErr := os.WriteFile(filepath.Join(opts.OutputDir, stdoutName), []byte(result.Stdout), 0o644); err == nil && writeErr != nil {
			err = writeErr
		}
	}
	if stderrName != "" {
		stage.Stderr = stderrName
		if writeErr := os.WriteFile(filepath.Join(opts.OutputDir, stderrName), []byte(result.Stderr), 0o644); err == nil && writeErr != nil {
			err = writeErr
		}
	}
	if err != nil {
		stage.Error = err.Error()
	}
	manifest.Stages = append(manifest.Stages, stage)
	if err != nil {
		return fmt.Errorf("%s: %w", command.Name, err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s: exit code %d", command.Name, result.ExitCode)
	}
	return nil
}

// waitReadyStage waits for wait ready stage to reach the expected state or timeout.
func waitReadyStage(ctx context.Context, opts RunOptions, manifest *RunManifest, url string) error {
	stage := StageResult{Name: "wait_frontend", StartedAt: opts.Now().UTC().Format(time.RFC3339Nano)}
	err := opts.WaitReady(ctx, url, opts.StartupTimeout)
	stage.EndedAt = opts.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		stage.ExitCode = 1
		stage.Error = err.Error()
	}
	manifest.Stages = append(manifest.Stages, stage)
	if err != nil {
		return fmt.Errorf("wait_frontend: %w", err)
	}
	return nil
}

// waitHTTPReady waits for wait http ready to reach the expected state or timeout.
func waitHTTPReady(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	client := http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastErr != nil {
				return fmt.Errorf("ready timeout for %s: %w", url, lastErr)
			}
			return fmt.Errorf("ready timeout for %s", url)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		}
	}
}

// writeLatencyCSVFromWorkload writes write latency csv from workload data to the configured output.
func writeLatencyCSVFromWorkload(outDir string, cfg Config, now func() time.Time) error {
	stdoutPath := filepath.Join(outDir, ArtifactWorkloadStdout)
	raw, err := os.ReadFile(stdoutPath)
	if err != nil {
		return err
	}
	row, err := ParseWrkOutput(string(raw))
	if err != nil {
		return err
	}
	if now == nil {
		now = time.Now
	}
	window := strconv.FormatInt(now().UnixMilli(), 10)
	row.Experiment = "deathstarbench_" + sanitizeName(cfg.Benchmark)
	row.Variant = "aegismesh_overlay"
	row.WindowStartUnixMS = window
	row.WindowEndUnixMS = window
	return writeLatencyCSV(filepath.Join(outDir, ArtifactLatencyCSV), row)
}

// LatencyRow carries latency row state for the DeathStarBench runner contract.
type LatencyRow struct {
	Experiment        string
	Variant           string
	WindowStartUnixMS string
	WindowEndUnixMS   string
	Requests          int
	ThroughputRPS     float64
	LatencyP50MS      float64
	LatencyP95MS      float64
	LatencyP99MS      float64
	ErrorRate         float64
}

var (
	percentileLineRE = regexp.MustCompile(`(?m)^\s*(50|95|99)(?:\.\d+)?%\s+([0-9.]+)(us|ms|s)\b`)
	requestsSecRE    = regexp.MustCompile(`(?m)Requests/sec:\s*([0-9.]+)`)
	requestsTotalRE  = regexp.MustCompile(`(?m)([0-9]+)\s+requests\s+in\s+`)
	non2xxRE         = regexp.MustCompile(`(?m)Non-2xx or 3xx responses:\s*([0-9]+)`)
)

// ParseWrkOutput decodes wrk output input into the package's typed representation.
func ParseWrkOutput(output string) (LatencyRow, error) {
	row := LatencyRow{}
	for _, match := range percentileLineRE.FindAllStringSubmatch(output, -1) {
		value, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			return row, err
		}
		value = durationToMillis(value, match[3])
		switch match[1] {
		case "50":
			row.LatencyP50MS = value
		case "95":
			row.LatencyP95MS = value
		case "99":
			row.LatencyP99MS = value
		}
	}
	if match := requestsSecRE.FindStringSubmatch(output); match != nil {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return row, err
		}
		row.ThroughputRPS = value
	}
	if match := requestsTotalRE.FindStringSubmatch(output); match != nil {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			return row, err
		}
		row.Requests = value
	}
	if match := non2xxRE.FindStringSubmatch(output); match != nil && row.Requests > 0 {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			return row, err
		}
		row.ErrorRate = float64(value) / float64(row.Requests)
	}
	if row.LatencyP50MS == 0 || row.LatencyP95MS == 0 || row.LatencyP99MS == 0 || row.ThroughputRPS == 0 {
		return row, errors.New("workload output does not contain wrk/wrk2 p50/p95/p99 and Requests/sec metrics")
	}
	return row, nil
}

// durationToMillis serializes durations into the millisecond units used by runner manifests.
func durationToMillis(value float64, unit string) float64 {
	switch unit {
	case "us":
		return value / 1000
	case "s":
		return value * 1000
	default:
		return value
	}
}

// writeLatencyCSV writes write latency csv data to the configured output.
func writeLatencyCSV(path string, row LatencyRow) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	if err := writer.Write([]string{"experiment", "variant", "window_start_unix_ms", "window_end_unix_ms", "requests", "throughput_rps", "latency_p50_ms", "latency_p95_ms", "latency_p99_ms", "error_rate"}); err != nil {
		return err
	}
	if err := writer.Write([]string{
		row.Experiment,
		row.Variant,
		row.WindowStartUnixMS,
		row.WindowEndUnixMS,
		strconv.Itoa(row.Requests),
		fmt.Sprintf("%.3f", row.ThroughputRPS),
		fmt.Sprintf("%.3f", row.LatencyP50MS),
		fmt.Sprintf("%.3f", row.LatencyP95MS),
		fmt.Sprintf("%.3f", row.LatencyP99MS),
		fmt.Sprintf("%.6f", row.ErrorRate),
	}); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

// ValidateRunDir validates run dir and returns a typed error for invalid input.
func ValidateRunDir(dir string, opts ValidationOptions) (RunValidation, error) {
	validation := RunValidation{Status: "pass"}
	add := func(name string, pass bool, detail string) {
		validation.Checks = append(validation.Checks, ValidationCheck{Name: name, Pass: pass, Detail: detail})
		if !pass {
			validation.Status = "fail"
		}
	}

	manifestPath := filepath.Join(dir, ArtifactManifest)
	var manifest RunManifest
	if err := readJSON(manifestPath, &manifest); err != nil {
		add("manifest", false, err.Error())
		return validation, fmt.Errorf("validate DeathStarBench run: manifest: %w", err)
	}
	add("manifest", true, ArtifactManifest)
	add("run_completed", manifest.Status == "completed", manifest.Status)
	add("integration_plan", fileNonEmpty(filepath.Join(dir, ArtifactPlan)), ArtifactPlan)
	add("compose_overlay", fileNonEmpty(filepath.Join(dir, ArtifactOverlay)), ArtifactOverlay)
	add("workload_stdout", fileNonEmpty(filepath.Join(dir, ArtifactWorkloadStdout)), ArtifactWorkloadStdout)
	add("workload_stderr", fileExists(filepath.Join(dir, ArtifactWorkloadStderr)), ArtifactWorkloadStderr)
	add("compose_ps", fileNonEmpty(filepath.Join(dir, ArtifactComposePS)), ArtifactComposePS)
	add("compose_logs", fileNonEmpty(filepath.Join(dir, ArtifactComposeLogs)), ArtifactComposeLogs)
	latencyOK, latencyDetail := validateLatencyCSV(filepath.Join(dir, ArtifactLatencyCSV))
	add("latency_csv", latencyOK, latencyDetail)
	add("aegis_service_map", manifest.Injection.ServiceMap != "", manifest.Injection.ServiceMap)
	add("injection_mode", validInjectionMode(manifest.Injection.Mode), manifest.Injection.Mode)
	if opts.RequireGovernedTraffic {
		governed := manifest.Injection.Mode == InjectionModeSidecarProxy && manifest.Injection.TrafficGovernance == TrafficGovernanceSidecarProxy
		add("governed_traffic", governed, fmt.Sprintf("mode=%s traffic_governance=%s", manifest.Injection.Mode, manifest.Injection.TrafficGovernance))
	}
	if validation.Status != "pass" {
		return validation, errors.New("DeathStarBench run validation failed")
	}
	return validation, nil
}

// validInjectionMode limits config acceptance to runner modes that this adapter can materialize.
func validInjectionMode(mode string) bool {
	return mode == InjectionModeComposeEnvironmentOverlay || mode == InjectionModeSidecarProxy
}

// fileExists treats any successful stat as enough for optional artifact presence checks.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fileNonEmpty requires an artifact to exist and contain bytes before validation accepts it.
func fileNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// validateLatencyCSV enforces the synthetic latency.csv contract written by the runner.
func validateLatencyCSV(path string) (bool, string) {
	file, err := os.Open(path)
	if err != nil {
		return false, err.Error()
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return false, err.Error()
	}
	if len(rows) < 2 {
		return false, "missing data rows"
	}
	header := []string{"experiment", "variant", "window_start_unix_ms", "window_end_unix_ms", "requests", "throughput_rps", "latency_p50_ms", "latency_p95_ms", "latency_p99_ms", "error_rate"}
	if !sameStrings(rows[0], header) {
		return false, "header mismatch"
	}
	for i, row := range rows[1:] {
		if len(row) != len(header) {
			return false, fmt.Sprintf("row %d has %d columns", i+2, len(row))
		}
		requests, err := strconv.Atoi(row[4])
		if err != nil || requests <= 0 {
			return false, fmt.Sprintf("row %d invalid requests", i+2)
		}
		throughput, err := strconv.ParseFloat(row[5], 64)
		if err != nil || throughput <= 0 {
			return false, fmt.Sprintf("row %d invalid throughput_rps", i+2)
		}
		p50, err50 := strconv.ParseFloat(row[6], 64)
		p95, err95 := strconv.ParseFloat(row[7], 64)
		p99, err99 := strconv.ParseFloat(row[8], 64)
		if err50 != nil || err95 != nil || err99 != nil || p50 <= 0 || p95 < p50 || p99 < p95 {
			return false, fmt.Sprintf("row %d invalid latency percentiles", i+2)
		}
		errorRate, err := strconv.ParseFloat(row[9], 64)
		if err != nil || errorRate < 0 || errorRate > 1 {
			return false, fmt.Sprintf("row %d invalid error_rate", i+2)
		}
	}
	return true, fmt.Sprintf("%s rows=%d", ArtifactLatencyCSV, len(rows)-1)
}

// sameStrings compares CSV headers exactly so validation catches reordered result columns.
func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// buildInjectionManifest builds build injection manifest dependencies from validated configuration.
func buildInjectionManifest(cfg Config) InjectionManifest {
	plan := cfg.Plan()
	services := make([]InjectedService, 0, len(cfg.Services))
	for name, mapping := range cfg.Services {
		services = append(services, InjectedService{DeathStarBenchService: name, AegisName: mapping.AegisName, Port: mapping.Port})
	}
	sort.Slice(services, func(i, j int) bool {
		return services[i].DeathStarBenchService < services[j].DeathStarBenchService
	})
	return InjectionManifest{
		Mode:              InjectionModeComposeEnvironmentOverlay,
		TrafficGovernance: TrafficGovernanceMetadataOnly,
		AegisController:   cfg.Controller,
		ServiceMap:        plan.Environment["AEGIS_SERVICE_MAP"],
		Services:          services,
		Notes: []string{
			"The overlay injects AegisMesh metadata into DeathStarBench containers.",
			"Non-Go/non-gRPC DeathStarBench traffic is not governed until a sidecar proxy or service rewrite consumes this metadata.",
		},
	}
}

// renderComposeOverlay renders render compose overlay into the external representation expected by callers.
func renderComposeOverlay(cfg Config) string {
	plan := cfg.Plan()
	var b strings.Builder
	b.WriteString("# Generated by cmd/deathstarbench-adapter --run.\n")
	b.WriteString("# This injects AegisMesh metadata; it does not by itself rewrite DeathStarBench RPC clients.\n")
	b.WriteString("services:\n")
	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mapping := cfg.Services[name]
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString(":\n")
		b.WriteString("    environment:\n")
		writeYAMLScalar(&b, "      AEGIS_CONTROLLER", cfg.Controller)
		writeYAMLScalar(&b, "      AEGIS_SERVICE_MAP", plan.Environment["AEGIS_SERVICE_MAP"])
		writeYAMLScalar(&b, "      AEGIS_SERVICE", mapping.AegisName)
		writeYAMLScalar(&b, "      AEGIS_INSTANCE_ID", name)
		writeYAMLScalar(&b, "      AEGIS_INSTANCE_PORT", strconv.Itoa(mapping.Port))
		b.WriteString("    labels:\n")
		writeYAMLScalar(&b, "      aegismesh.service", mapping.AegisName)
		writeYAMLScalar(&b, "      aegismesh.instance", name)
		writeYAMLScalar(&b, "      aegismesh.port", strconv.Itoa(mapping.Port))
	}
	return b.String()
}

// writeYAMLScalar writes write yaml scalar data to the configured output.
func writeYAMLScalar(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(strconv.Quote(value))
	b.WriteByte('\n')
}

// frontendURL prefers the configured readiness URL and falls back to the local DeathStarBench frontend.
func frontendURL(cfg Config) string {
	if cfg.Frontend.URL != "" {
		return cfg.Frontend.URL
	}
	return "http://localhost:8080"
}

// sanitizeName converts benchmark names into stable filesystem and compose-project slugs.
func sanitizeName(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "run"
	}
	return out
}

// shellQuote wraps compose arguments only when shell metacharacters require quoting.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\r\n\"'`$&;()[]{}") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// writeJSON writes write json data to the configured output.
func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

// readJSON reads read json data from the supplied input.
func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
