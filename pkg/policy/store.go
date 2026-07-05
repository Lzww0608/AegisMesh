package policy

import (
	"os"
	"sort"
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// Store is the read-only policy snapshot source consumed by controller policy serving and SDK hot-apply.
type Store interface {
	Get(service string) (*aegisv1.PolicySnapshot, bool)
}

// FileStore loads YAML policy snapshots and caches file metadata so unchanged reads avoid reparsing.
type FileStore struct {
	mu       sync.RWMutex
	path     string
	modTime  int64
	policies map[string]*aegisv1.PolicySnapshot
}

// fileConfig is the YAML root keyed by service name for local policy files.
type fileConfig struct {
	Services map[string]serviceConfig `yaml:"services"`
}

// serviceConfig groups service-level routing, retry, outlier, breaker, and method override settings.
type serviceConfig struct {
	RoutingPolicy    string                      `yaml:"routing_policy"`
	Retry            retryConfig                 `yaml:"retry"`
	OutlierDetection outlierDetectionConfig      `yaml:"outlier_detection"`
	CircuitBreaker   circuitBreakerConfig        `yaml:"circuit_breaker"`
	Methods          map[string]methodPolicyConf `yaml:"methods"`
}

// retryConfig mirrors YAML retry fields before they are normalized into policy snapshots.
type retryConfig struct {
	Enabled             bool    `yaml:"enabled"`
	MaxAttempts         int32   `yaml:"max_attempts"`
	BudgetRatio         float64 `yaml:"budget_ratio"`
	MinBudget           int32   `yaml:"min_budget"`
	WindowSeconds       int64   `yaml:"window_seconds"`
	PerTryTimeoutMillis int64   `yaml:"per_try_timeout_millis"`
}

// outlierDetectionConfig carries YAML thresholds used to build fault-state transition policy.
type outlierDetectionConfig struct {
	DegradedThreshold       float64 `yaml:"degraded_threshold"`
	EjectThreshold          float64 `yaml:"eject_threshold"`
	ConsecutiveWindows      int32   `yaml:"consecutive_windows"`
	EjectionDurationSeconds int64   `yaml:"ejection_duration_seconds"`
	RecoveryThreshold       float64 `yaml:"recovery_threshold"`
	ProbeSuccessThreshold   float64 `yaml:"probe_success_threshold"`
}

// circuitBreakerConfig captures the per-endpoint inflight cap exported into policy snapshots.
type circuitBreakerConfig struct {
	MaxInflightPerEndpoint int64 `yaml:"max_inflight_per_endpoint"`
}

// methodPolicyConf lets a method override idempotency, timeout, and retry settings from the service default.
type methodPolicyConf struct {
	Idempotent    bool        `yaml:"idempotent"`
	TimeoutMillis int64       `yaml:"timeout_millis"`
	Retry         retryConfig `yaml:"retry"`
}

// NewFileStore initializes file store with package defaults for this package's call path.
func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{
		path:     path,
		policies: make(map[string]*aegisv1.PolicySnapshot),
	}
	if err := store.Reload(); err != nil {
		return nil, err
	}
	return store, nil
}

// Get returns get state for the requested key.
func (s *FileStore) Get(service string) (*aegisv1.PolicySnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := s.policies[service]
	if snapshot == nil {
		return nil, false
	}
	return proto.Clone(snapshot).(*aegisv1.PolicySnapshot), true
}

// List returns a point-in-time list of list visible to the caller.
func (s *FileStore) List() []*aegisv1.PolicySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*aegisv1.PolicySnapshot, 0, len(s.policies))
	for _, snapshot := range s.policies {
		if snapshot == nil {
			continue
		}
		out = append(out, proto.Clone(snapshot).(*aegisv1.PolicySnapshot))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Service < out[j].Service
	})
	return out
}

// ReloadIfChanged stats the policy file and reloads cached snapshots only when the mod time changes.
func (s *FileStore) ReloadIfChanged() error {
	info, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	if info.ModTime().UnixNano() == s.currentModTime() {
		return nil
	}
	return s.Reload()
}

// Reload reparses the policy file and atomically replaces the cached service snapshots.
func (s *FileStore) Reload() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return err
	}

	var cfg fileConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}

	policies := make(map[string]*aegisv1.PolicySnapshot, len(cfg.Services))
	revision := info.ModTime().UnixNano()
	for service, serviceCfg := range cfg.Services {
		methods := make(map[string]*aegisv1.MethodPolicy, len(serviceCfg.Methods))
		for method, methodCfg := range serviceCfg.Methods {
			methods[method] = &aegisv1.MethodPolicy{
				Method:        method,
				Idempotent:    methodCfg.Idempotent,
				TimeoutMillis: methodCfg.TimeoutMillis,
				Retry:         retryToProto(methodCfg.Retry),
			}
		}
		policies[service] = &aegisv1.PolicySnapshot{
			Service:          service,
			Revision:         revision,
			RoutingPolicy:    serviceCfg.RoutingPolicy,
			Retry:            retryToProto(serviceCfg.Retry),
			OutlierDetection: outlierToProto(serviceCfg.OutlierDetection),
			CircuitBreaker:   circuitBreakerToProto(serviceCfg.CircuitBreaker),
			Methods:          methods,
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.modTime = revision
	s.policies = policies
	return nil
}

// currentModTime returns current mod time data for FileStore callers without handing out mutable receiver state.
func (s *FileStore) currentModTime() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modTime
}

// retryToProto provides the shared retry to proto helper for policy storage and hot-apply.
func retryToProto(cfg retryConfig) *aegisv1.RetryPolicy {
	return &aegisv1.RetryPolicy{
		Enabled:             cfg.Enabled,
		MaxAttempts:         cfg.MaxAttempts,
		BudgetRatio:         cfg.BudgetRatio,
		MinBudget:           cfg.MinBudget,
		WindowSeconds:       cfg.WindowSeconds,
		PerTryTimeoutMillis: cfg.PerTryTimeoutMillis,
	}
}

// outlierToProto provides the shared outlier to proto helper for policy storage and hot-apply.
func outlierToProto(cfg outlierDetectionConfig) *aegisv1.OutlierDetectionPolicy {
	return &aegisv1.OutlierDetectionPolicy{
		DegradedThreshold:       cfg.DegradedThreshold,
		EjectThreshold:          cfg.EjectThreshold,
		ConsecutiveWindows:      cfg.ConsecutiveWindows,
		EjectionDurationSeconds: cfg.EjectionDurationSeconds,
		RecoveryThreshold:       cfg.RecoveryThreshold,
		ProbeSuccessThreshold:   cfg.ProbeSuccessThreshold,
	}
}

// circuitBreakerToProto provides the shared circuit breaker to proto helper for policy storage and hot-apply.
func circuitBreakerToProto(cfg circuitBreakerConfig) *aegisv1.CircuitBreakerPolicy {
	return &aegisv1.CircuitBreakerPolicy{MaxInflightPerEndpoint: cfg.MaxInflightPerEndpoint}
}
