package policy

import (
	"os"
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

type Store interface {
	Get(service string) (*aegisv1.PolicySnapshot, bool)
}

type FileStore struct {
	mu       sync.RWMutex
	path     string
	modTime  int64
	policies map[string]*aegisv1.PolicySnapshot
}

type fileConfig struct {
	Services map[string]serviceConfig `yaml:"services"`
}

type serviceConfig struct {
	RoutingPolicy    string                      `yaml:"routing_policy"`
	Retry            retryConfig                 `yaml:"retry"`
	OutlierDetection outlierDetectionConfig      `yaml:"outlier_detection"`
	CircuitBreaker   circuitBreakerConfig        `yaml:"circuit_breaker"`
	Methods          map[string]methodPolicyConf `yaml:"methods"`
}

type retryConfig struct {
	Enabled             bool    `yaml:"enabled"`
	MaxAttempts         int32   `yaml:"max_attempts"`
	BudgetRatio         float64 `yaml:"budget_ratio"`
	MinBudget           int32   `yaml:"min_budget"`
	WindowSeconds       int64   `yaml:"window_seconds"`
	PerTryTimeoutMillis int64   `yaml:"per_try_timeout_millis"`
}

type outlierDetectionConfig struct {
	DegradedThreshold       float64 `yaml:"degraded_threshold"`
	EjectThreshold          float64 `yaml:"eject_threshold"`
	ConsecutiveWindows      int32   `yaml:"consecutive_windows"`
	EjectionDurationSeconds int64   `yaml:"ejection_duration_seconds"`
	RecoveryThreshold       float64 `yaml:"recovery_threshold"`
	ProbeSuccessThreshold   float64 `yaml:"probe_success_threshold"`
}

type circuitBreakerConfig struct {
	MaxInflightPerEndpoint int64 `yaml:"max_inflight_per_endpoint"`
}

type methodPolicyConf struct {
	Idempotent    bool        `yaml:"idempotent"`
	TimeoutMillis int64       `yaml:"timeout_millis"`
	Retry         retryConfig `yaml:"retry"`
}

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

func (s *FileStore) Get(service string) (*aegisv1.PolicySnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := s.policies[service]
	if snapshot == nil {
		return nil, false
	}
	return proto.Clone(snapshot).(*aegisv1.PolicySnapshot), true
}

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
	version := info.ModTime().UnixNano()
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
			Version:          version,
			RoutingPolicy:    serviceCfg.RoutingPolicy,
			Retry:            retryToProto(serviceCfg.Retry),
			OutlierDetection: outlierToProto(serviceCfg.OutlierDetection),
			CircuitBreaker:   circuitBreakerToProto(serviceCfg.CircuitBreaker),
			Methods:          methods,
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.modTime = version
	s.policies = policies
	return nil
}

func (s *FileStore) currentModTime() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modTime
}

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

func circuitBreakerToProto(cfg circuitBreakerConfig) *aegisv1.CircuitBreakerPolicy {
	return &aegisv1.CircuitBreakerPolicy{MaxInflightPerEndpoint: cfg.MaxInflightPerEndpoint}
}
