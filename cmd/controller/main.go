package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/controller"
	"github.com/aegismesh/aegismesh/pkg/fault"
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
	healthTickInterval := flag.Duration("health-tick-interval", time.Second, "endpoint health state-machine tick interval")
	healthDegradedThreshold := flag.Float64("health-degraded-threshold", 0, "slow_score threshold for DEGRADED; 0 uses default")
	healthEjectThreshold := flag.Float64("health-eject-threshold", 0, "slow_score threshold for EJECTED; 0 uses default")
	healthConsecutiveWindows := flag.Int("health-consecutive-windows", 0, "consecutive windows before state transition; 0 uses default")
	healthEjectionDuration := flag.Duration("health-ejection-duration", 0, "duration before EJECTED moves to PROBING; 0 uses default")
	healthRecoveryThreshold := flag.Float64("health-recovery-threshold", 0, "slow_score threshold for recovery; 0 uses default")
	healthProbeSuccessThreshold := flag.Float64("health-probe-success-threshold", 0, "success rate threshold during PROBING; 0 uses default")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store := registry.NewMemoryRegistry(time.Now)
	healthManager := fault.NewHealthManager(fault.HealthManagerConfig{
		StateMachine: buildStateMachineConfig(
			*healthDegradedThreshold,
			*healthEjectThreshold,
			*healthConsecutiveWindows,
			*healthEjectionDuration,
			*healthRecoveryThreshold,
			*healthProbeSuccessThreshold,
		),
	})
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

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	log.Printf("aegis controller listening on %s", *addr)
	if err := server.Serve(lis); err != nil {
		log.Fatalf("serve controller: %v", err)
	}
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
