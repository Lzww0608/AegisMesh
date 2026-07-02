package aegisgrpc

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
)

const (
	defaultTelemetryReportInterval = 5 * time.Second
	defaultTelemetryReportTimeout  = 2 * time.Second
)

type telemetryClient interface {
	ReportEndpointStats(ctx context.Context, in *aegisv1.ReportEndpointStatsRequest, opts ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error)
}

type telemetryReporter struct {
	client         telemetryClient
	recorder       *telemetry.Recorder
	interval       time.Duration
	requestTimeout time.Duration
	protoSamplesPoolHolder
}

func newTelemetryReporter(client telemetryClient, recorder *telemetry.Recorder, interval time.Duration) *telemetryReporter {
	if interval <= 0 {
		interval = defaultTelemetryReportInterval
	}
	return &telemetryReporter{
		client:         client,
		recorder:       recorder,
		interval:       interval,
		requestTimeout: defaultTelemetryReportTimeout,
	}
}

func (r *telemetryReporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.ReportOnce(ctx)
		}
	}
}

func (r *telemetryReporter) ReportOnce(ctx context.Context) error {
	if r == nil || r.client == nil || r.recorder == nil {
		return nil
	}
	stats := r.recorder.SnapshotAndReset()
	defer telemetry.ReleaseEndpointStatsSlice(stats)
	if len(stats) == 0 {
		return nil
	}

	samples := r.acquireProtoSamples(len(stats))
	defer r.releaseProtoSamples(samples)

	for i, stat := range stats {
		fillProtoSample(samples[i], stat)
	}
	samples = samples[:len(stats)]

	reportCtx := ctx
	cancel := func() {}
	if r.requestTimeout > 0 {
		reportCtx, cancel = context.WithTimeout(ctx, r.requestTimeout)
	}
	defer cancel()

	_, err := r.client.ReportEndpointStats(reportCtx, &aegisv1.ReportEndpointStatsRequest{Samples: samples})
	return err
}

func statsToProto(stat telemetry.EndpointStats) *aegisv1.EndpointStatsSample {
	sample := &aegisv1.EndpointStatsSample{}
	fillProtoSample(sample, stat)
	return sample
}
