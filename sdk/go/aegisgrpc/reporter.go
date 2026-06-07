package aegisgrpc

import (
	"context"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
	"google.golang.org/grpc"
)

const defaultTelemetryReportInterval = 5 * time.Second

type telemetryClient interface {
	ReportEndpointStats(ctx context.Context, in *aegisv1.ReportEndpointStatsRequest, opts ...grpc.CallOption) (*aegisv1.ReportEndpointStatsResponse, error)
}

type telemetryReporter struct {
	client   telemetryClient
	recorder *telemetry.Recorder
	interval time.Duration
}

func newTelemetryReporter(client telemetryClient, recorder *telemetry.Recorder, interval time.Duration) *telemetryReporter {
	if interval <= 0 {
		interval = defaultTelemetryReportInterval
	}
	return &telemetryReporter{
		client:   client,
		recorder: recorder,
		interval: interval,
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
	if len(stats) == 0 {
		return nil
	}

	req := &aegisv1.ReportEndpointStatsRequest{
		Samples: make([]*aegisv1.EndpointStatsSample, 0, len(stats)),
	}
	for _, stat := range stats {
		req.Samples = append(req.Samples, statsToProto(stat))
	}
	_, err := r.client.ReportEndpointStats(ctx, req)
	return err
}

func statsToProto(stat telemetry.EndpointStats) *aegisv1.EndpointStatsSample {
	return &aegisv1.EndpointStatsSample{
		Source:                stat.Source,
		Service:               stat.Destination,
		EndpointAddress:       stat.Upstream,
		Method:                stat.Method,
		RequestCount:          stat.RequestCount,
		ErrorCount:            stat.ErrorCount,
		TimeoutCount:          stat.TimeoutCount,
		Inflight:              stat.Inflight,
		LatencyEwmaSeconds:    stat.LatencyEWMA.Seconds(),
		LatencyP95Seconds:     stat.LatencyP95.Seconds(),
		WindowStartUnixMillis: stat.WindowStart.UnixMilli(),
		WindowEndUnixMillis:   stat.WindowEnd.UnixMilli(),
	}
}
