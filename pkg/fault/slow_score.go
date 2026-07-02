package fault

import (
	"math"
	"sort"
	"time"
)

const scoreEpsilon = 1e-9

type ScoreWeights struct {
	LatencyWeight    float64
	ErrorWeight      float64
	InflightWeight   float64
	RetransmitWeight float64
}

type ScoreCalculatorConfig struct {
	Weights    ScoreWeights
	LatencySLO time.Duration
}

func DefaultScoreWeights() ScoreWeights {
	return ScoreWeights{
		LatencyWeight:    0.45,
		ErrorWeight:      0.25,
		InflightWeight:   0.20,
		RetransmitWeight: 0.10,
	}
}

type EndpointSample struct {
	Service           string
	InstanceID        string
	Address           string
	RegistrationEpoch string
	Method            string
	RequestCount      int64
	ErrorCount        int64
	TimeoutCount      int64
	Inflight          int64
	Capacity          int64
	LatencyEWMA       time.Duration
	LatencyP95        time.Duration
	TCPRetransmit     int64
	ConnectError      int64
}

type EndpointScore struct {
	Service           string
	InstanceID        string
	Address           string
	RegistrationEpoch string
	Method            string
	Score             float64
	LatencyScore      float64
	ErrorScore        float64
	InflightScore     float64
	RetransmitScore   float64
}

type ScoreCalculator struct {
	weights    ScoreWeights
	latencySLO time.Duration
}

func NewScoreCalculator(weights ScoreWeights) *ScoreCalculator {
	return NewScoreCalculatorWithConfig(ScoreCalculatorConfig{Weights: weights})
}

func NewScoreCalculatorWithConfig(cfg ScoreCalculatorConfig) *ScoreCalculator {
	weights := cfg.Weights
	total := weights.LatencyWeight + weights.ErrorWeight + weights.InflightWeight + weights.RetransmitWeight
	if total <= 0 || math.IsNaN(total) {
		weights = DefaultScoreWeights()
	}
	if cfg.LatencySLO < 0 {
		cfg.LatencySLO = 0
	}
	return &ScoreCalculator{weights: weights, latencySLO: cfg.LatencySLO}
}

func (c *ScoreCalculator) Calculate(samples []EndpointSample) map[string]EndpointScore {
	byService := make(map[string]map[string]EndpointSample)
	for _, sample := range samples {
		if sample.Service == "" || sample.InstanceID == "" {
			continue
		}
		serviceSamples := byService[sample.Service]
		if serviceSamples == nil {
			serviceSamples = make(map[string]EndpointSample)
			byService[sample.Service] = serviceSamples
		}
		serviceSamples[sample.InstanceID] = aggregateEndpointSample(serviceSamples[sample.InstanceID], sample)
	}

	out := make(map[string]EndpointScore)
	for service, serviceSamples := range byService {
		aggregated := make([]EndpointSample, 0, len(serviceSamples))
		for _, sample := range serviceSamples {
			aggregated = append(aggregated, sample)
		}
		c.calculateService(service, aggregated, out)
	}
	return out
}

func aggregateEndpointSample(current EndpointSample, sample EndpointSample) EndpointSample {
	if current.Service == "" {
		return sample
	}
	if current.Address == "" {
		current.Address = sample.Address
	}
	if current.RegistrationEpoch == "" {
		current.RegistrationEpoch = sample.RegistrationEpoch
	} else if sample.RegistrationEpoch != "" && current.RegistrationEpoch != sample.RegistrationEpoch {
		current.RegistrationEpoch = ""
	}
	if current.Method != "" && sample.Method != "" && current.Method != sample.Method {
		current.Method = ""
	}
	current.RequestCount += sample.RequestCount
	current.ErrorCount += sample.ErrorCount
	current.TimeoutCount += sample.TimeoutCount
	current.Inflight += sample.Inflight
	current.TCPRetransmit += sample.TCPRetransmit
	current.ConnectError += sample.ConnectError
	if sample.Capacity > current.Capacity {
		current.Capacity = sample.Capacity
	}
	// Recorder samples are method windows, not mergeable histograms. Keep latency
	// conservative so a degraded route on an endpoint is not hidden by faster methods.
	if sample.LatencyEWMA > current.LatencyEWMA {
		current.LatencyEWMA = sample.LatencyEWMA
	}
	if sample.LatencyP95 > current.LatencyP95 {
		current.LatencyP95 = sample.LatencyP95
	}
	return current
}

func (c *ScoreCalculator) calculateService(service string, samples []EndpointSample, out map[string]EndpointScore) {
	latencies := make([]float64, 0, len(samples))
	errorRates := make([]float64, 0, len(samples))
	retransmitRates := make([]float64, 0, len(samples))
	for _, sample := range samples {
		latencies = append(latencies, sample.LatencyP95.Seconds())
		errorRates = append(errorRates, rate(sample.ErrorCount, sample.RequestCount))
		retransmitRates = append(retransmitRates, networkRate(sample.TCPRetransmit+sample.ConnectError, sample.RequestCount))
	}

	medianLatency := median(latencies)
	madLatency := medianAbsoluteDeviation(latencies, medianLatency)
	avgErrorRate := average(errorRates)
	avgRetransmitRate := average(retransmitRates)

	for _, sample := range samples {
		relativeLatencyScore := math.Max(0, sample.LatencyP95.Seconds()-medianLatency) / math.Max(madLatency, 0.001)
		absoluteLatencyScore := c.absoluteLatencyScore(sample.LatencyP95)
		latencyScore := math.Max(relativeLatencyScore, absoluteLatencyScore)
		errorScore := 0.0
		if sample.ErrorCount > 0 {
			errorScore = rate(sample.ErrorCount, sample.RequestCount) / math.Max(avgErrorRate, scoreEpsilon)
		}
		inflightScore := float64(sample.Inflight) / float64(capacity(sample.Capacity))
		retransmitScore := 0.0
		networkEvents := sample.TCPRetransmit + sample.ConnectError
		if networkEvents > 0 {
			retransmitScore = networkRate(networkEvents, sample.RequestCount) / math.Max(avgRetransmitRate, scoreEpsilon)
		}

		score := c.weights.LatencyWeight*latencyScore +
			c.weights.ErrorWeight*errorScore +
			c.weights.InflightWeight*inflightScore +
			c.weights.RetransmitWeight*retransmitScore

		out[ScoreKey(service, sample.InstanceID)] = EndpointScore{
			Service:           service,
			InstanceID:        sample.InstanceID,
			Address:           sample.Address,
			RegistrationEpoch: sample.RegistrationEpoch,
			Method:            sample.Method,
			Score:             score,
			LatencyScore:      latencyScore,
			ErrorScore:        errorScore,
			InflightScore:     inflightScore,
			RetransmitScore:   retransmitScore,
		}
	}
}

func (c *ScoreCalculator) absoluteLatencyScore(latency time.Duration) float64 {
	if c.latencySLO <= 0 || latency <= 0 {
		return 0
	}
	return latency.Seconds() / c.latencySLO.Seconds()
}

func ScoreKey(service, instanceID string) string {
	return service + "/" + instanceID
}

func rate(count, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(count) / float64(total)
}

func networkRate(count, total int64) float64 {
	if count <= 0 {
		return 0
	}
	if total <= 0 {
		return float64(count)
	}
	return float64(count) / float64(total)
}

func capacity(value int64) int64 {
	if value <= 0 {
		return 100
	}
	return value
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func medianAbsoluteDeviation(values []float64, med float64) float64 {
	if len(values) == 0 {
		return 0
	}
	deviations := make([]float64, 0, len(values))
	for _, value := range values {
		deviations = append(deviations, math.Abs(value-med))
	}
	return median(deviations)
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
