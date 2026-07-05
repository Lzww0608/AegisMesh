package telemetry

import (
	"sync/atomic"
	"time"
)

const latencyHistShardCount = 8

var latencyHistShardSeq atomic.Uint32

// latencyHistShardIndex provides the shared latency hist shard index helper for recorder aggregation.
func latencyHistShardIndex() int {
	return int(latencyHistShardSeq.Add(1) & (latencyHistShardCount - 1))
}

// latencyHistShard carries latency hist shard state for recorder aggregation.
type latencyHistShard struct {
	count   atomic.Uint64
	buckets [latencyHistogramBuckets]atomic.Uint64
}

// shardedLatencyHistogram carries sharded latency histogram state for recorder aggregation.
type shardedLatencyHistogram struct {
	shards [latencyHistShardCount]latencyHistShard
}

// Record records record in the current accounting window.
func (h *shardedLatencyHistogram) Record(sample time.Duration) {
	if sample < 0 {
		sample = 0
	}
	idx := latencyBucketIndex(uint64(sample))
	shard := &h.shards[latencyHistShardIndex()]
	shard.buckets[idx].Add(1)
	shard.count.Add(1)
}

// Quantile returns quantile data for shardedLatencyHistogram callers without handing out mutable receiver state.
func (h *shardedLatencyHistogram) Quantile(q float64) time.Duration {
	if q <= 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}

	var merged [latencyHistogramBuckets]uint64
	var total uint64
	for i := range h.shards {
		total += h.shards[i].count.Load()
		for b := range merged {
			merged[b] += h.shards[i].buckets[b].Load()
		}
	}
	if total == 0 {
		return 0
	}

	target := uint64(q*float64(total) + 0.999999999)
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i, count := range merged {
		seen += count
		if seen >= target {
			return time.Duration(latencyBucketUpperBounds[i])
		}
	}
	return time.Duration(latencyBucketUpperBounds[len(latencyBucketUpperBounds)-1])
}

// Reset clears all shard counters so the next aggregation window starts empty.
func (h *shardedLatencyHistogram) Reset() {
	for i := range h.shards {
		h.shards[i].count.Store(0)
		for b := range h.shards[i].buckets {
			h.shards[i].buckets[b].Store(0)
		}
	}
}

// latencyHistogram is the single-shard merge view used in tests.
type latencyHistogram struct {
	count   uint64
	buckets [latencyHistogramBuckets]uint64
}

// Record records record in the current accounting window.
func (h *latencyHistogram) Record(sample time.Duration) {
	if sample < 0 {
		sample = 0
	}
	idx := latencyBucketIndex(uint64(sample))
	h.buckets[idx]++
	h.count++
}

// Quantile returns quantile data for latencyHistogram callers without handing out mutable receiver state.
func (h *latencyHistogram) Quantile(q float64) time.Duration {
	if h.count == 0 {
		return 0
	}
	if q <= 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	target := uint64(q*float64(h.count) + 0.999999999)
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i, count := range h.buckets {
		seen += count
		if seen >= target {
			return time.Duration(latencyBucketUpperBounds[i])
		}
	}
	return time.Duration(latencyBucketUpperBounds[len(latencyBucketUpperBounds)-1])
}

// Reset clears the single-shard histogram used by tests and merge paths.
func (h *latencyHistogram) Reset() {
	for i := range h.buckets {
		h.buckets[i] = 0
	}
	h.count = 0
}
