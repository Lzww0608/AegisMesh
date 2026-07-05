package aegisgrpc

import (
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/telemetry"
)

const defaultProtoSamplesCap = 64

// acquireProtoSamples returns acquire proto samples data for telemetryReporter callers without handing out mutable receiver state.
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

// releaseProtoSamples releases previously acquired capacity back to the limiter.
func (r *telemetryReporter) releaseProtoSamples(samples []*aegisv1.EndpointStatsSample) {
	if len(samples) == 0 {
		return
	}
	for i := range samples {
		resetProtoSample(samples[i])
	}
	r.protoSamplesPool.Put(samples[:0])
}

// resetProtoSample provides the shared reset proto sample helper for resolver, picker, and reporter state.
func resetProtoSample(sample *aegisv1.EndpointStatsSample) {
	if sample == nil {
		return
	}
	*sample = aegisv1.EndpointStatsSample{}
}

// fillProtoSample provides the shared fill proto sample helper for resolver, picker, and reporter state.
func fillProtoSample(dst *aegisv1.EndpointStatsSample, stat telemetry.EndpointStats) {
	dst.Source = stat.Source
	dst.Service = stat.Destination
	dst.InstanceId = stat.EndpointID
	dst.RegistrationEpoch = stat.RegistrationEpoch
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

// protoSamplesPoolHolder carries proto samples pool holder state for resolver, picker, and reporter state.
type protoSamplesPoolHolder struct {
	protoSamplesPool sync.Pool
}
