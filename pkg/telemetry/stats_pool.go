package telemetry

import "sync"

const defaultEndpointStatsSliceCap = 64

var endpointStatsSlicePool sync.Pool

func acquireEndpointStatsSlice(capHint int) []EndpointStats {
	if capHint < defaultEndpointStatsSliceCap {
		capHint = defaultEndpointStatsSliceCap
	}
	if cached, ok := endpointStatsSlicePool.Get().([]EndpointStats); ok && cap(cached) >= capHint {
		return cached[:0]
	}
	return make([]EndpointStats, 0, capHint)
}

// ReleaseEndpointStatsSlice returns a SnapshotAndReset slice to the recorder pool.
// Callers must not use the slice after release.
func ReleaseEndpointStatsSlice(stats []EndpointStats) {
	if stats == nil {
		return
	}
	for i := range stats {
		stats[i] = EndpointStats{}
	}
	endpointStatsSlicePool.Put(stats[:0])
}

func cloneEndpointStatsSlice(stats []EndpointStats) []EndpointStats {
	if len(stats) == 0 {
		return nil
	}
	out := make([]EndpointStats, len(stats))
	copy(out, stats)
	return out
}
