package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/aegismesh/aegismesh/agent/ebpf"
	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	controllerAddr := flag.String("controller", "127.0.0.1:9000", "Aegis Controller gRPC address")
	objectPath := flag.String("object", ebpf.DefaultObjectPath(), "compiled eBPF object path")
	endpointMapRaw := flag.String("endpoint-map", "", "comma-separated endpoint mapping: ip:port=service/instance")
	interval := flag.Duration("interval", 5*time.Second, "telemetry report interval")
	flag.Parse()

	endpointMap, err := ebpf.ParseEndpointMap(*endpointMapRaw)
	if err != nil {
		log.Fatalf("parse endpoint map: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := grpc.NewClient(*controllerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("connect controller %s: %v", *controllerAddr, err)
	}
	defer conn.Close()

	collector, err := ebpf.NewCollector(ebpf.Config{
		ObjectPath:     *objectPath,
		ControllerAddr: *controllerAddr,
		EndpointMap:    endpointMap,
	})
	if err != nil && !errors.Is(err, ebpf.ErrUnsupportedPlatform) {
		log.Fatalf("create eBPF collector: %v", err)
	}

	reporter := ebpf.NewReporter(ebpf.ReporterConfig{
		Collector:  collector,
		Client:     aegisv1.NewTelemetryServiceClient(conn),
		Aggregator: ebpf.NewAggregator(endpointMap),
		Interval:   *interval,
	})
	if err := reporter.Start(ctx); err != nil {
		if errors.Is(err, ebpf.ErrUnsupportedPlatform) {
			log.Fatalf("eBPF collector is only supported on Linux")
		}
		log.Fatalf("start eBPF reporter: %v", err)
	}
	log.Printf("aegis eBPF agent reporting to %s every %s", *controllerAddr, interval.String())

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reporter.ReportOnce(shutdownCtx); err != nil {
		log.Printf("final eBPF report failed: %v", err)
	}
	if err := reporter.Stop(); err != nil {
		log.Printf("stop eBPF reporter: %v", err)
	}
}
