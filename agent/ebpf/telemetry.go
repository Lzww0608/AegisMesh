package ebpf

import aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"

// NetworkSamplesToTelemetrySamples provides the shared network samples to telemetry samples helper for the eBPF telemetry path.
func NetworkSamplesToTelemetrySamples(samples []NetworkSample) []*aegisv1.EndpointStatsSample {
	out := make([]*aegisv1.EndpointStatsSample, 0, len(samples))
	for _, sample := range samples {
		out = append(out, &aegisv1.EndpointStatsSample{
			Source:                "ebpf",
			Service:               sample.Service,
			InstanceId:            sample.InstanceID,
			EndpointAddress:       sample.Address,
			TcpRetransmit:         sample.TCPRetransmit,
			ConnectError:          sample.ConnectError,
			WindowStartUnixMillis: sample.WindowStart.UnixMilli(),
			WindowEndUnixMillis:   sample.WindowEnd.UnixMilli(),
		})
	}
	return out
}
