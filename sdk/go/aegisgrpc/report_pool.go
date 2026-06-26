package aegisgrpc

import (
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
)

const defaultProtoSamplesCap = 64

func (r *telemetryReporter) acquireProtoSamples(n int) []*aegisv1.EndpointStatsSample {
	if n < defaultProtoSamplesCap {
		n = defaultProtoSamplesCap
	}
	var samples []*aegisv1.EndpointStatsSample
	if cached, ok := r.protoSamplesPool.Get().([]*aegisv1.EndpointStatsSample); ok {
		samples = cached
	}
	if cap(samples) < n {
		samples = make([]*aegisv1.EndpointStatsSample, 0, n)
	}
	for len(samples) < n {
		samples = append(samples, &aegisv1.EndpointStatsSample{})
	}
	return samples
}

func (r *telemetryReporter) releaseProtoSamples(samples []*aegisv1.EndpointStatsSample) {
	if len(samples) == 0 {
		return
	}
	for i := range samples {
		resetProtoSample(samples[i])
	}
	r.protoSamplesPool.Put(samples[:0])
}

func resetProtoSample(sample *aegisv1.EndpointStatsSample) {
	if sample == nil {
		return
	}
	*sample = aegisv1.EndpointStatsSample{}
}

func fillProtoSample(dst *aegisv1.EndpointStatsSample, stat telemetry.EndpointStats) {
	dst.Source = stat.Source
	dst.Service = stat.Destination
	dst.InstanceId = stat.EndpointID
	dst.EndpointAddress = stat.Upstream
	dst.Method = stat.Method
	dst.RequestCount = stat.RequestCount
	dst.ErrorCount = stat.ErrorCount
	dst.TimeoutCount = stat.TimeoutCount
	dst.Inflight = stat.Inflight
	dst.LatencyEwmaSeconds = stat.LatencyEWMA.Seconds()
	dst.LatencyP95Seconds = stat.LatencyP95.Seconds()
	dst.WindowStartUnixMillis = stat.WindowStart.UnixMilli()
	dst.WindowEndUnixMillis = stat.WindowEnd.UnixMilli()
}

type protoSamplesPoolHolder struct {
	protoSamplesPool sync.Pool
}
