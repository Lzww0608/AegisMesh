package verifier

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec carries spec state for this package call path.
type Spec struct {
	Test   TestSpec   `yaml:"test" json:"test"`
	Expect ExpectSpec `yaml:"expect" json:"expect"`
}

// TestSpec carries test spec state for this package call path.
type TestSpec struct {
	Name     string `yaml:"name" json:"name"`
	Service  string `yaml:"service" json:"service"`
	Method   string `yaml:"method" json:"method"`
	Requests int    `yaml:"requests" json:"requests"`
}

// ExpectSpec carries expect spec state for this package call path.
type ExpectSpec struct {
	Routes           map[string]float64 `yaml:"routes" json:"routes"`
	Tolerance        float64            `yaml:"tolerance" json:"tolerance"`
	MaxRetryAttempts int                `yaml:"max_retry_attempts" json:"max_retry_attempts"`
	ForbiddenEdges   []string           `yaml:"forbidden_edges" json:"forbidden_edges"`
}

// TraceRecord carries trace record state for this package call path.
type TraceRecord struct {
	TraceID       string   `json:"trace_id" yaml:"trace_id"`
	Route         string   `json:"route" yaml:"route"`
	Path          []string `json:"path" yaml:"path"`
	RetryAttempts int      `json:"retry_attempts" yaml:"retry_attempts"`
	Status        string   `json:"status" yaml:"status"`
}

// Report carries report state for this package call path.
type Report struct {
	Passed            bool               `json:"passed"`
	Checks            []CheckResult      `json:"checks"`
	RouteDistribution map[string]float64 `json:"route_distribution"`
	TraceCount        int                `json:"trace_count"`
}

// CheckResult reports one verifier assertion so reports can preserve both pass/fail state and diagnostic text.
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// ParseSpec decodes spec input into the package's typed representation.
func ParseSpec(raw []byte) (Spec, error) {
	var spec Spec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return Spec{}, err
	}
	if spec.Expect.Routes == nil {
		spec.Expect.Routes = map[string]float64{}
	}
	if spec.Expect.Tolerance == 0 {
		spec.Expect.Tolerance = 0.03
	}
	return spec, nil
}

// Verify provides the shared verify helper for this package call path.
func Verify(spec Spec, traces []TraceRecord) Report {
	report := Report{
		Passed:            true,
		RouteDistribution: routeDistribution(traces),
		TraceCount:        len(traces),
	}

	for route, expected := range spec.Expect.Routes {
		actual := report.RouteDistribution[route]
		diff := math.Abs(actual - expected)
		name := "route_distribution:" + route
		if diff <= spec.Expect.Tolerance {
			report.add(name, true, fmt.Sprintf("actual %.4f within %.4f of expected %.4f", actual, spec.Expect.Tolerance, expected))
		} else {
			report.add(name, false, fmt.Sprintf("actual %.4f differs from expected %.4f by %.4f", actual, expected, diff))
		}
	}

	maxRetry := spec.Expect.MaxRetryAttempts
	retryOK := true
	for _, trace := range traces {
		if trace.RetryAttempts > maxRetry {
			retryOK = false
			report.add("retry_budget", false, fmt.Sprintf("trace %s used %d retry attempts above max %d", trace.TraceID, trace.RetryAttempts, maxRetry))
			break
		}
	}
	if retryOK {
		report.add("retry_budget", true, fmt.Sprintf("all traces used at most %d retry attempts", maxRetry))
	}

	forbidden := make(map[string]struct{}, len(spec.Expect.ForbiddenEdges))
	for _, edge := range spec.Expect.ForbiddenEdges {
		forbidden[strings.TrimSpace(edge)] = struct{}{}
	}
	forbiddenOK := true
	for _, trace := range traces {
		for _, edge := range traceEdges(trace.Path) {
			if _, blocked := forbidden[edge]; blocked {
				forbiddenOK = false
				report.add("forbidden_edges", false, fmt.Sprintf("trace %s used forbidden edge %s", trace.TraceID, edge))
				break
			}
		}
		if !forbiddenOK {
			break
		}
	}
	if forbiddenOK {
		report.add("forbidden_edges", true, "no forbidden edges observed")
	}

	return report
}

// add appends a verifier check result and flips the aggregate pass flag on the first failure.
func (r *Report) add(name string, passed bool, message string) {
	r.Checks = append(r.Checks, CheckResult{Name: name, Passed: passed, Message: message})
	if !passed {
		r.Passed = false
	}
}

// HasFailedCheck returns has failed check data for Report callers without handing out mutable receiver state.
func (r Report) HasFailedCheck(name string) bool {
	for _, check := range r.Checks {
		if check.Name == name && !check.Passed {
			return true
		}
	}
	return false
}

// routeDistribution provides the shared route distribution helper for this package call path.
func routeDistribution(traces []TraceRecord) map[string]float64 {
	out := make(map[string]float64)
	if len(traces) == 0 {
		return out
	}
	counts := make(map[string]int)
	for _, trace := range traces {
		if trace.Route == "" {
			continue
		}
		counts[trace.Route]++
	}
	for route, count := range counts {
		out[route] = float64(count) / float64(len(traces))
	}
	return out
}

// traceEdges provides the shared trace edges helper for this package call path.
func traceEdges(path []string) []string {
	if len(path) < 2 {
		return nil
	}
	edges := make([]string, 0, len(path)-1)
	for i := 0; i < len(path)-1; i++ {
		edges = append(edges, path[i]+"->"+path[i+1])
	}
	return edges
}

// LoadTraceJSONL reads trace jsonl state from the configured backing source and returns a caller-owned view.
func LoadTraceJSONL(r io.Reader) ([]TraceRecord, error) {
	scanner := bufio.NewScanner(r)
	traces := make([]TraceRecord, 0)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var trace TraceRecord
		if err := json.Unmarshal([]byte(line), &trace); err != nil {
			return nil, fmt.Errorf("parse trace jsonl line %d: %w", lineNo, err)
		}
		traces = append(traces, trace)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return traces, nil
}
