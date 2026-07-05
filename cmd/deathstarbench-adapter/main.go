package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aegismesh/aegismesh/pkg/deathstarbench"
)

// main wires the command-line entry point and reports fatal setup or runtime errors.
func main() {
	configPath := flag.String("config", "experiments/deathstarbench/social-network.yaml", "DeathStarBench integration config")
	run := flag.Bool("run", false, "execute the integration plan against a local DeathStarBench checkout")
	repoDir := flag.String("repo-dir", "", "local DeathStarBench checkout used with --run")
	outDir := flag.String("out", "", "run artifact directory used with --run")
	project := flag.String("project", "", "docker compose project name used with --run")
	composeBin := flag.String("compose-bin", "docker compose", "compose command used with --run")
	startupTimeout := flag.Duration("startup-timeout", 90*time.Second, "frontend readiness timeout used with --run")
	keepStack := flag.Bool("keep-stack", false, "leave the DeathStarBench compose stack running after --run")
	validateRun := flag.String("validate-run", "", "validate an existing DeathStarBench run artifact directory and exit")
	requireGovernedTraffic := flag.Bool("require-governed-traffic", false, "validation fails unless the run manifest records sidecar/proxy governed traffic")
	flag.Parse()

	if *validateRun != "" {
		validation, err := deathstarbench.ValidateRunDir(*validateRun, deathstarbench.ValidationOptions{RequireGovernedTraffic: *requireGovernedTraffic})
		printJSON(validation)
		if err != nil {
			log.Fatalf("validate run: %v", err)
		}
		return
	}

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	cfg, err := deathstarbench.ParseConfig(raw)
	if err != nil {
		log.Fatalf("parse config: %v", err)
	}

	if !*run {
		printJSON(cfg.Plan())
		return
	}

	if *outDir == "" {
		*outDir = defaultRunDir(cfg.Benchmark, time.Now().UTC())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	manifest, err := deathstarbench.Runner{}.Run(ctx, cfg, deathstarbench.RunOptions{
		RepoDir:        *repoDir,
		OutputDir:      *outDir,
		ProjectName:    *project,
		ComposeBin:     *composeBin,
		StartupTimeout: *startupTimeout,
		KeepStack:      *keepStack,
	})
	printJSON(manifest)
	if err != nil {
		log.Fatalf("run DeathStarBench: %v", err)
	}
}

// printJSON provides the shared print json helper for the DeathStarBench runner contract.
func printJSON(value any) {
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Fatalf("encode json: %v", err)
	}
	fmt.Println(string(out))
}

// defaultRunDir keeps default run dir rules consistent for the DeathStarBench runner contract.
func defaultRunDir(benchmark string, now time.Time) string {
	return "experiments/results/runs/deathstarbench-" + sanitize(benchmark) + "-" + now.Format("20060102-150405")
}

// sanitize keeps sanitize rules consistent for the DeathStarBench runner contract.
func sanitize(value string) string {
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
