package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/security"
)

func main() {
	controllerAddr := flag.String("controller", "127.0.0.1:9000", "Aegis Controller gRPC address")
	service := flag.String("service", "user-service", "service to record")
	experiment := flag.String("experiment", "recovery_curve", "experiment label")
	variant := flag.String("variant", "adaptive_p2c", "variant label")
	out := flag.String("out", "experiments/results/recovery.csv", "output recovery CSV")
	interval := flag.Duration("interval", time.Second, "poll interval")
	duration := flag.Duration("duration", 30*time.Second, "recording duration; 0 records one sample")
	p99Latency := flag.Float64("p99-latency-ms", math.NaN(), "optional p99 latency value to include in each row")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	conn, err := security.DialController(ctx, *controllerAddr, security.ClientConfigFromEnv("AEGIS_CONTROLLER"))
	if err != nil {
		log.Fatalf("dial controller: %v", err)
	}
	defer conn.Close()

	writer, closeFile, err := openRecoveryWriter(*out)
	if err != nil {
		log.Fatalf("open recovery output: %v", err)
	}
	defer closeFile()

	client := aegisv1.NewTelemetryServiceClient(conn)
	if *duration <= 0 {
		if err := recordOnce(ctx, client, writer, *service, *experiment, *variant, *p99Latency); err != nil {
			log.Fatalf("record recovery sample: %v", err)
		}
		return
	}

	deadline := time.NewTimer(*duration)
	defer deadline.Stop()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	if err := recordOnce(ctx, client, writer, *service, *experiment, *variant, *p99Latency); err != nil {
		log.Printf("record recovery sample: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			if err := recordOnce(ctx, client, writer, *service, *experiment, *variant, *p99Latency); err != nil {
				log.Printf("record recovery sample: %v", err)
			}
		}
	}
}

func openRecoveryWriter(path string) (*csv.Writer, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	_, statErr := os.Stat(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	writer := csv.NewWriter(file)
	if os.IsNotExist(statErr) {
		if err := writer.Write([]string{"experiment", "variant", "timestamp_unix_ms", "endpoint", "slow_score", "p99_latency_ms", "route_weight", "state"}); err != nil {
			_ = file.Close()
			return nil, nil, err
		}
		writer.Flush()
	}
	return writer, func() {
		writer.Flush()
		_ = file.Close()
	}, nil
}

func recordOnce(ctx context.Context, client aegisv1.TelemetryServiceClient, writer *csv.Writer, service, experiment, variant string, p99Latency float64) error {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := client.ListEndpointHealth(reqCtx, &aegisv1.ListEndpointHealthRequest{Service: service})
	if err != nil {
		return err
	}

	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	for _, endpoint := range resp.Endpoints {
		if endpoint == nil {
			continue
		}
		slowScore := endpoint.SlowScore
		p99 := ""
		if !math.IsNaN(p99Latency) {
			p99 = fmt.Sprintf("%.3f", p99Latency)
		}
		row := []string{
			experiment,
			variant,
			now,
			endpoint.EndpointAddress,
			fmt.Sprintf("%.6f", slowScore),
			p99,
			fmt.Sprintf("%.6f", 1/(1+slowScore)),
			endpoint.State,
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}
