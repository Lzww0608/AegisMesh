package ebpf

import (
	"context"
	"sync"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
)

type TelemetryClient interface {
	ReportEndpointStats(ctx context.Context, in *aegisv1.ReportEndpointStatsRequest, opts ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error)
}

type ReporterConfig struct {
	Collector  Collector
	Client     TelemetryClient
	Aggregator *Aggregator
	Interval   time.Duration
}

type Reporter struct {
	collector  Collector
	client     TelemetryClient
	aggregator *Aggregator
	interval   time.Duration
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewReporter(cfg ReporterConfig) *Reporter {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultReportInterval
	}
	return &Reporter{
		collector:  cfg.Collector,
		client:     cfg.Client,
		aggregator: cfg.Aggregator,
		interval:   cfg.Interval,
	}
}

func (r *Reporter) Start(ctx context.Context) error {
	if r.collector == nil || r.client == nil || r.aggregator == nil {
		return nil
	}
	if err := r.collector.Start(); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.wg.Add(1)
	go r.run(runCtx)
	return nil
}

func (r *Reporter) Stop() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	if r.collector == nil {
		return nil
	}
	return r.collector.Stop()
}

func (r *Reporter) ReportOnce(ctx context.Context) error {
	if r.client == nil || r.aggregator == nil {
		return nil
	}
	r.drainEvents()
	samples := NetworkSamplesToTelemetrySamples(r.aggregator.SnapshotAndReset())
	if len(samples) == 0 {
		return nil
	}
	_, err := r.client.ReportEndpointStats(ctx, &aegisv1.ReportEndpointStatsRequest{Samples: samples})
	return err
}

func (r *Reporter) run(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-r.collector.Events():
			if !ok {
				return
			}
			r.aggregator.Observe(event)
		case <-ticker.C:
			_ = r.ReportOnce(ctx)
		}
	}
}

func (r *Reporter) drainEvents() {
	if r.collector == nil {
		return
	}
	for {
		select {
		case event, ok := <-r.collector.Events():
			if !ok {
				return
			}
			r.aggregator.Observe(event)
		default:
			return
		}
	}
}
