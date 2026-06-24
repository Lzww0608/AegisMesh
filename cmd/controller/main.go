package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/controller"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/policy"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9000", "controller gRPC listen address")
	httpAddr := flag.String("http-addr", "127.0.0.1:9100", "controller HTTP metrics listen address")
	lease := flag.Duration("lease", 30*time.Second, "default service instance lease")
	sweepInterval := flag.Duration("sweep-interval", 5*time.Second, "expired instance sweep interval")
	registryCfg := registerRegistryFlags(flag.CommandLine)
	policyCfg := registerPolicyFlags(flag.CommandLine)
	healthTickInterval := flag.Duration("health-tick-interval", time.Second, "endpoint health state-machine tick interval")
	healthDegradedThreshold := flag.Float64("health-degraded-threshold", 0, "slow_score threshold for DEGRADED; 0 uses default")
	healthEjectThreshold := flag.Float64("health-eject-threshold", 0, "slow_score threshold for EJECTED; 0 uses default")
	healthConsecutiveWindows := flag.Int("health-consecutive-windows", 0, "consecutive windows before state transition; 0 uses default")
	healthEjectionDuration := flag.Duration("health-ejection-duration", 0, "duration before EJECTED moves to PROBING; 0 uses default")
	healthRecoveryThreshold := flag.Float64("health-recovery-threshold", 0, "slow_score threshold for recovery; 0 uses default")
	healthProbeSuccessThreshold := flag.Float64("health-probe-success-threshold", 0, "success rate threshold during PROBING; 0 uses default")
	healthLatencySLO := flag.Duration("health-latency-slo", 0, "absolute p95 latency SLO used by slow_score; 0 disables absolute SLO scoring")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := buildRegistry(registryCfg, time.Now)
	if err != nil {
		log.Fatalf("build registry: %v", err)
	}
	policyStore, err := buildPolicyStore(policyCfg)
	if err != nil {
		log.Fatalf("build policy store: %v", err)
	}
	healthManager := fault.NewHealthManager(buildHealthManagerConfig(
		*healthDegradedThreshold,
		*healthEjectThreshold,
		*healthConsecutiveWindows,
		*healthEjectionDuration,
		*healthRecoveryThreshold,
		*healthProbeSuccessThreshold,
		*healthLatencySLO,
	))
	healthMetrics, err := fault.NewPrometheusHealthMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		log.Fatalf("register health prometheus metrics: %v", err)
	}

	go sweepExpired(ctx, store, *sweepInterval)
	go tickHealth(ctx, healthManager, healthMetrics, *healthTickInterval)
	if *httpAddr != "" {
		go serveMetrics(ctx, *httpAddr)
	}

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen controller: %v", err)
	}

	server := grpc.NewServer()
	aegisv1.RegisterRegistryServiceServer(server, controller.NewRegistryServiceWithHealth(store, *lease, healthManager))
	aegisv1.RegisterTelemetryServiceServer(server, controller.NewTelemetryService(store, healthManager, healthMetrics))
	if policyStore != nil {
		aegisv1.RegisterPolicyServiceServer(server, controller.NewPolicyService(policyStore, *policyCfg.reloadInterval))
	}

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	log.Printf("aegis controller listening on %s", *addr)
	if err := server.Serve(lis); err != nil {
		log.Fatalf("serve controller: %v", err)
	}
}

type registryFlags struct {
	backend             *string
	file                *string
	fileV2Sync          *string
	fileV2FlushInterval *time.Duration
	fileV2FlushRecords  *int
	fileV2FlushBytes    *int
	fileV2CompactBytes  *int64
}

func registerRegistryFlags(fs *flag.FlagSet) registryFlags {
	return registryFlags{
		backend:             fs.String("registry-backend", "memory", "registry backend: memory, file, or file-v2"),
		file:                fs.String("registry-file", "data/aegis-registry.json", "file registry snapshot path when --registry-backend=file or file-v2"),
		fileV2Sync:          fs.String("registry-file-v2-sync", string(registry.FileRegistrySyncBatch), "file-v2 WAL sync mode: batch or always"),
		fileV2FlushInterval: fs.Duration("registry-file-v2-flush-interval", 2*time.Millisecond, "file-v2 group commit flush/fsync interval"),
		fileV2FlushRecords:  fs.Int("registry-file-v2-flush-records", 64, "file-v2 group commit flush/fsync record threshold"),
		fileV2FlushBytes:    fs.Int("registry-file-v2-flush-bytes", 64*1024, "file-v2 group commit flush/fsync byte threshold"),
		fileV2CompactBytes:  fs.Int64("registry-file-v2-compact-bytes", 16*1024*1024, "file-v2 WAL size threshold that triggers snapshot compaction; <=0 disables automatic compaction"),
	}
}

func buildRegistry(cfg registryFlags, now func() time.Time) (registry.Registry, error) {
	switch *cfg.backend {
	case "", "memory":
		return registry.NewMemoryRegistry(now), nil
	case "file":
		return registry.NewFileRegistry(*cfg.file, now)
	case "file-v2":
		return registry.NewFileRegistryV2(
			*cfg.file,
			now,
			registry.WithFileRegistryV2SyncMode(registry.FileRegistrySyncMode(*cfg.fileV2Sync)),
			registry.WithFileRegistryV2GroupCommit(*cfg.fileV2FlushRecords, *cfg.fileV2FlushBytes, *cfg.fileV2FlushInterval),
			registry.WithFileRegistryV2CompactBytes(*cfg.fileV2CompactBytes),
		)
	default:
		return nil, fmt.Errorf("unsupported registry backend %q", *cfg.backend)
	}
}

type policyFlags struct {
	file           *string
	reloadInterval *time.Duration
}

func registerPolicyFlags(fs *flag.FlagSet) policyFlags {
	return policyFlags{
		file:           fs.String("policy-file", "", "YAML policy file for PolicyService; empty disables PolicyService"),
		reloadInterval: fs.Duration("policy-reload-interval", 3*time.Second, "PolicyService WatchPolicy reload interval"),
	}
}

func buildPolicyStore(cfg policyFlags) (controller.PolicyStore, error) {
	if cfg.file == nil || *cfg.file == "" {
		return nil, nil
	}
	return policy.NewFileStore(*cfg.file)
}

func buildStateMachineConfig(degradedThreshold float64, ejectThreshold float64, consecutiveWindows int, ejectionDuration time.Duration, recoveryThreshold float64, probeSuccessThreshold float64) fault.StateMachineConfig {
	return fault.StateMachineConfig{
		DegradedThreshold:     degradedThreshold,
		EjectThreshold:        ejectThreshold,
		ConsecutiveWindows:    consecutiveWindows,
		EjectionDuration:      ejectionDuration,
		RecoveryThreshold:     recoveryThreshold,
		ProbeSuccessThreshold: probeSuccessThreshold,
	}
}

func buildHealthManagerConfig(degradedThreshold float64, ejectThreshold float64, consecutiveWindows int, ejectionDuration time.Duration, recoveryThreshold float64, probeSuccessThreshold float64, latencySLO time.Duration) fault.HealthManagerConfig {
	return fault.HealthManagerConfig{
		StateMachine: buildStateMachineConfig(
			degradedThreshold,
			ejectThreshold,
			consecutiveWindows,
			ejectionDuration,
			recoveryThreshold,
			probeSuccessThreshold,
		),
		LatencySLO: latencySLO,
	}
}

func sweepExpired(ctx context.Context, store registry.Registry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired := store.SweepExpired(ctx)
			if expired > 0 {
				log.Printf("expired %d service instance(s)", expired)
			}
		}
	}
}

func tickHealth(ctx context.Context, manager *fault.HealthManager, metrics *fault.PrometheusHealthMetrics, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, health := range manager.Tick() {
				metrics.RecordHealth(health)
			}
		}
	}
}

func serveMetrics(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("controller metrics listening on http://%s/metrics", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("serve controller metrics: %v", err)
	}
}
