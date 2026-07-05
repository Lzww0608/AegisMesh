package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/registry"
)

// TestBuildStateMachineConfigPreservesExperimentThresholds locks the build state machine config preserves experiment thresholds contract so future changes do not regress it.
func TestBuildStateMachineConfigPreservesExperimentThresholds(t *testing.T) {
	cfg := buildStateMachineConfig(0.05, 0.09, 2, 5*time.Second, 0.03, 0.90)

	if cfg.DegradedThreshold != 0.05 {
		t.Fatalf("expected degraded threshold 0.05, got %v", cfg.DegradedThreshold)
	}
	if cfg.EjectThreshold != 0.09 {
		t.Fatalf("expected eject threshold 0.09, got %v", cfg.EjectThreshold)
	}
	if cfg.ConsecutiveWindows != 2 {
		t.Fatalf("expected two consecutive windows, got %d", cfg.ConsecutiveWindows)
	}
	if cfg.EjectionDuration != 5*time.Second {
		t.Fatalf("expected 5s ejection duration, got %s", cfg.EjectionDuration)
	}
	if cfg.RecoveryThreshold != 0.03 {
		t.Fatalf("expected recovery threshold 0.03, got %v", cfg.RecoveryThreshold)
	}
	if cfg.ProbeSuccessThreshold != 0.90 {
		t.Fatalf("expected probe threshold 0.90, got %v", cfg.ProbeSuccessThreshold)
	}
}

// TestBuildHealthManagerConfigPreservesLatencySLO locks the build health manager config preserves latency slo contract so future changes do not regress it.
func TestBuildHealthManagerConfigPreservesLatencySLO(t *testing.T) {
	cfg := buildHealthManagerConfig(0.05, 0.09, 2, 5*time.Second, 0.03, 0.90, 250*time.Millisecond)

	if cfg.LatencySLO != 250*time.Millisecond {
		t.Fatalf("expected latency SLO 250ms, got %s", cfg.LatencySLO)
	}
	if cfg.StateMachine.DegradedThreshold != 0.05 || cfg.StateMachine.EjectThreshold != 0.09 {
		t.Fatalf("expected state-machine thresholds to be preserved, got %+v", cfg.StateMachine)
	}
}

// TestCloseRegistryStoreClosesCloseableBackend locks the close registry store closes closeable backend contract so future changes do not regress it.
func TestCloseRegistryStoreClosesCloseableBackend(t *testing.T) {
	store := &closeableRegistryForTest{}
	closeRegistryStore(store)
	if !store.closed {
		t.Fatalf("expected closeable registry to be closed")
	}
}

// closeableRegistryForTest carries closeable registry for test state for controller startup and restore flows.
type closeableRegistryForTest struct {
	registry.Registry
	closed bool
}

// Close closes owned resources and makes repeated calls safe.
func (s *closeableRegistryForTest) Close() error {
	s.closed = true
	return nil
}

// TestBuildRegistryFromFlagsSelectsFileBackend locks the build registry from flags selects file backend contract so future changes do not regress it.
func TestBuildRegistryFromFlagsSelectsFileBackend(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{"--registry-backend", "file", "--registry-file", t.TempDir() + "/registry.json"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	store, err := buildRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if _, ok := store.(interface{ PersistencePath() string }); !ok {
		t.Fatalf("expected persistent registry backend, got %T", store)
	}
}

// TestRegisterPolicyFlags locks the register policy flags contract so future changes do not regress it.
func TestRegisterPolicyFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerPolicyFlags(fs)
	if err := fs.Parse([]string{"--policy-file", "policy.yaml", "--policy-reload-interval", "2s"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if *cfg.file != "policy.yaml" {
		t.Fatalf("expected policy file flag, got %q", *cfg.file)
	}
	if *cfg.reloadInterval != 2*time.Second {
		t.Fatalf("expected 2s reload interval, got %s", *cfg.reloadInterval)
	}
}

// TestStartPolicyHotApplyLoopAppliesInitialFilePolicy locks the start policy hot apply loop applies initial file policy contract so future changes do not regress it.
func TestStartPolicyHotApplyLoopAppliesInitialFilePolicy(t *testing.T) {
	path := t.TempDir() + "/policy.yaml"
	if err := os.WriteFile(path, []byte(`
services:
  user-service:
    outlier_detection:
      degraded_threshold: 0.5
      ejection_duration_seconds: 4
`), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	policyCfg := registerPolicyFlags(fs)
	if err := fs.Parse([]string{"--policy-file", path, "--policy-reload-interval", "1h"}); err != nil {
		t.Fatalf("parse policy flags: %v", err)
	}
	store, err := buildPolicyStore(context.Background(), policyCfg)
	if err != nil {
		t.Fatalf("build policy store: %v", err)
	}
	manager := fault.NewHealthManager(fault.HealthManagerConfig{
		StateMachine: buildStateMachineConfig(5, 6, 7, 8*time.Second, 2, 0.8),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !startPolicyHotApplyLoop(ctx, store, manager, time.Hour) {
		t.Fatalf("expected file policy store to start hot-apply loop")
	}

	cfg := manager.EffectiveStateMachineConfig("user-service")
	if cfg.DegradedThreshold != 0.5 || cfg.EjectThreshold != 6 || cfg.ConsecutiveWindows != 7 || cfg.EjectionDuration != 4*time.Second {
		t.Fatalf("expected initial file policy to hot-apply with base inheritance, got %+v", cfg)
	}
}

// TestRegisterHealthStateFlagsIncludesEtcdOptions locks the register health state flags includes etcd options contract so future changes do not regress it.
func TestRegisterHealthStateFlagsIncludesEtcdOptions(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerHealthStateFlags(fs)
	if err := fs.Parse([]string{
		"--health-state-backend", "etcd",
		"--health-state-max-age", "2m",
		"--health-state-etcd-endpoints", "127.0.0.1:2379,127.0.0.2:2379",
		"--health-state-etcd-prefix", "/aegis/health",
		"--health-state-etcd-request-timeout", "750ms",
		"--health-state-etcd-tls-ca-file", "ca.pem",
		"--health-state-etcd-tls-cert-file", "client.pem",
		"--health-state-etcd-tls-key-file", "client-key.pem",
		"--health-state-etcd-tls-server-name", "etcd.internal",
	}); err != nil {
		t.Fatalf("parse health state flags: %v", err)
	}
	if *cfg.backend != "etcd" || *cfg.maxAge != 2*time.Minute || *cfg.etcdPrefix != "/aegis/health" || *cfg.etcdRequestTimeout != 750*time.Millisecond {
		t.Fatalf("unexpected health state flags: backend=%q maxAge=%s prefix=%q timeout=%s", *cfg.backend, *cfg.maxAge, *cfg.etcdPrefix, *cfg.etcdRequestTimeout)
	}
	if *cfg.etcdTLSCAFile != "ca.pem" || *cfg.etcdTLSCertFile != "client.pem" || *cfg.etcdTLSKeyFile != "client-key.pem" || *cfg.etcdTLSServerName != "etcd.internal" {
		t.Fatalf("unexpected health state etcd TLS flags: ca=%q cert=%q key=%q server=%q", *cfg.etcdTLSCAFile, *cfg.etcdTLSCertFile, *cfg.etcdTLSKeyFile, *cfg.etcdTLSServerName)
	}
}

// TestBuildHealthSnapshotStoreRejectsEtcdWithoutEndpoints locks the build health snapshot store rejects etcd without endpoints contract so future changes do not regress it.
func TestBuildHealthSnapshotStoreRejectsEtcdWithoutEndpoints(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerHealthStateFlags(fs)
	if err := fs.Parse([]string{"--health-state-backend", "etcd"}); err != nil {
		t.Fatalf("parse health state flags: %v", err)
	}
	if _, err := buildHealthSnapshotStore(context.Background(), cfg); err == nil {
		t.Fatalf("expected health state etcd backend without endpoints to be rejected")
	}
}

// TestRestoreHealthSnapshotFiltersStaleHealth locks the restore health snapshot filters stale health contract so future changes do not regress it.
func TestRestoreHealthSnapshotFiltersStaleHealth(t *testing.T) {
	now := time.Now()
	store := &staticHealthSnapshotStore{snapshot: []fault.EndpointHealth{
		{Service: "user-service", InstanceID: "fresh", State: fault.StateEjected, UpdatedAt: now.Add(-time.Minute)},
		{Service: "user-service", InstanceID: "stale", State: fault.StateEjected, UpdatedAt: now.Add(-10 * time.Minute)},
	}}
	manager := fault.NewHealthManager(fault.HealthManagerConfig{Now: func() time.Time { return now }})
	_, restored, err := restoreHealthSnapshot(context.Background(), store, manager, nil, 5*time.Minute)
	if err != nil {
		t.Fatalf("restore health snapshot: %v", err)
	}
	if restored != 1 {
		t.Fatalf("expected only fresh health to restore, got %d", restored)
	}
	if _, ok := manager.Get("user-service", "fresh"); !ok {
		t.Fatalf("expected fresh health entry")
	}
	if _, ok := manager.Get("user-service", "stale"); ok {
		t.Fatalf("expected stale health entry to be filtered")
	}
}

// TestRestoreHealthSnapshotFiltersUnregisteredHealth locks the restore health snapshot filters unregistered health contract so future changes do not regress it.
func TestRestoreHealthSnapshotFiltersUnregisteredHealth(t *testing.T) {
	now := time.Now()
	reg := registry.NewMemoryRegistry(func() time.Time { return now })
	if err := reg.Register(context.Background(), registry.Instance{ID: "active", Service: "user-service", Address: "127.0.0.1:7001"}, time.Minute); err != nil {
		t.Fatalf("register active instance: %v", err)
	}
	store := &staticHealthSnapshotStore{snapshot: []fault.EndpointHealth{
		{Service: "user-service", InstanceID: "active", Address: "127.0.0.1:7001", State: fault.StateEjected, UpdatedAt: now.Add(-time.Minute)},
		{Service: "user-service", InstanceID: "missing", Address: "127.0.0.1:7002", State: fault.StateEjected, UpdatedAt: now.Add(-time.Minute)},
		{Service: "user-service", InstanceID: "active", Address: "127.0.0.1:7101", State: fault.StateDegraded, UpdatedAt: now.Add(-time.Minute)},
	}}
	manager := fault.NewHealthManager(fault.HealthManagerConfig{Now: func() time.Time { return now }})

	_, restored, err := restoreHealthSnapshot(context.Background(), store, manager, reg, 5*time.Minute)
	if err != nil {
		t.Fatalf("restore health snapshot: %v", err)
	}
	if restored != 1 {
		t.Fatalf("expected only registered health to restore, got %d", restored)
	}
	if _, ok := manager.Get("user-service", "active"); !ok {
		t.Fatalf("expected active health entry")
	}
	if _, ok := manager.Get("user-service", "missing"); ok {
		t.Fatalf("expected unregistered health entry to be filtered")
	}
	health, _ := manager.Get("user-service", "active")
	if health.State != fault.StateEjected || health.Address != "127.0.0.1:7001" {
		t.Fatalf("expected address-matched health to win over mismatched snapshot, got %+v", health)
	}
}

// TestPruneHealthMissingFromRegistryRemovesInactiveEndpoint locks the prune health missing from registry removes inactive endpoint contract so future changes do not regress it.
func TestPruneHealthMissingFromRegistryRemovesInactiveEndpoint(t *testing.T) {
	now := time.Now()
	reg := registry.NewMemoryRegistry(func() time.Time { return now })
	if err := reg.Register(context.Background(), registry.Instance{ID: "active", Service: "user-service", Address: "127.0.0.1:7001"}, time.Minute); err != nil {
		t.Fatalf("register active instance: %v", err)
	}
	if err := reg.Register(context.Background(), registry.Instance{ID: "moved", Service: "user-service", Address: "127.0.0.1:7003"}, time.Minute); err != nil {
		t.Fatalf("register moved instance: %v", err)
	}
	manager := fault.NewHealthManager(fault.HealthManagerConfig{Now: func() time.Time { return now }})
	manager.MergeSnapshot([]fault.EndpointHealth{
		{Service: "user-service", InstanceID: "active", Address: "127.0.0.1:7001", State: fault.StateHealthy, UpdatedAt: now},
		{Service: "user-service", InstanceID: "gone", Address: "127.0.0.1:7002", State: fault.StateEjected, UpdatedAt: now},
		{Service: "user-service", InstanceID: "moved", Address: "127.0.0.1:7101", State: fault.StateEjected, UpdatedAt: now},
	})

	pruned, err := pruneHealthMissingFromRegistry(context.Background(), reg, manager)
	if err != nil {
		t.Fatalf("prune health: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("expected inactive and address-mismatched health endpoints to be pruned, got %d", pruned)
	}
	if _, ok := manager.Get("user-service", "active"); !ok {
		t.Fatalf("expected active endpoint health to remain")
	}
	if _, ok := manager.Get("user-service", "gone"); ok {
		t.Fatalf("expected inactive endpoint health to be removed")
	}
	if _, ok := manager.Get("user-service", "moved"); ok {
		t.Fatalf("expected address-mismatched endpoint health to be removed")
	}
}

// TestResolveHealthEtcdPasswordPrefersEnvThenFileThenFlag locks the resolve health etcd password prefers env then file then flag contract so future changes do not regress it.
func TestResolveHealthEtcdPasswordPrefersEnvThenFileThenFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerHealthStateFlags(fs)
	passwordFile := t.TempDir() + "/health-etcd-password.txt"
	if err := os.WriteFile(passwordFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write health etcd password file: %v", err)
	}
	if err := fs.Parse([]string{"--health-state-etcd-password", "from-flag", "--health-state-etcd-password-file", passwordFile, "--health-state-etcd-password-env", "AEGIS_TEST_HEALTH_ETCD_PASSWORD"}); err != nil {
		t.Fatalf("parse health state flags: %v", err)
	}
	if got, err := resolveHealthEtcdPassword(cfg); err != nil || got != "from-file" {
		t.Fatalf("expected file password fallback, got %q err=%v", got, err)
	}
	t.Setenv("AEGIS_TEST_HEALTH_ETCD_PASSWORD", "from-env")
	if got, err := resolveHealthEtcdPassword(cfg); err != nil || got != "from-env" {
		t.Fatalf("expected env password priority, got %q err=%v", got, err)
	}
}

// TestRegisterPolicyFlagsIncludesEtcdOptions locks the register policy flags includes etcd options contract so future changes do not regress it.
func TestRegisterPolicyFlagsIncludesEtcdOptions(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerPolicyFlags(fs)
	if err := fs.Parse([]string{
		"--policy-backend", "etcd",
		"--policy-etcd-endpoints", "127.0.0.1:2379,127.0.0.2:2379",
		"--policy-etcd-prefix", "/aegis/policies",
		"--policy-etcd-request-timeout", "750ms",
		"--policy-etcd-tls-ca-file", "ca.pem",
		"--policy-etcd-tls-cert-file", "client.pem",
		"--policy-etcd-tls-key-file", "client-key.pem",
		"--policy-etcd-tls-server-name", "etcd.internal",
	}); err != nil {
		t.Fatalf("parse policy flags: %v", err)
	}
	if *cfg.backend != "etcd" || *cfg.etcdPrefix != "/aegis/policies" || *cfg.etcdRequestTimeout != 750*time.Millisecond {
		t.Fatalf("unexpected policy etcd flags: backend=%q prefix=%q timeout=%s", *cfg.backend, *cfg.etcdPrefix, *cfg.etcdRequestTimeout)
	}
	if *cfg.etcdTLSCAFile != "ca.pem" || *cfg.etcdTLSCertFile != "client.pem" || *cfg.etcdTLSKeyFile != "client-key.pem" || *cfg.etcdTLSServerName != "etcd.internal" {
		t.Fatalf("unexpected policy etcd TLS flags: ca=%q cert=%q key=%q server=%q", *cfg.etcdTLSCAFile, *cfg.etcdTLSCertFile, *cfg.etcdTLSKeyFile, *cfg.etcdTLSServerName)
	}
}

// TestBuildPolicyStoreRejectsEtcdWithoutEndpoints locks the build policy store rejects etcd without endpoints contract so future changes do not regress it.
func TestBuildPolicyStoreRejectsEtcdWithoutEndpoints(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerPolicyFlags(fs)
	if err := fs.Parse([]string{"--policy-backend", "etcd"}); err != nil {
		t.Fatalf("parse policy flags: %v", err)
	}
	if _, err := buildPolicyStore(context.Background(), cfg); err == nil {
		t.Fatalf("expected policy etcd backend without endpoints to be rejected")
	}
}

// TestResolvePolicyEtcdPasswordPrefersEnvThenFileThenFlag locks the resolve policy etcd password prefers env then file then flag contract so future changes do not regress it.
func TestResolvePolicyEtcdPasswordPrefersEnvThenFileThenFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerPolicyFlags(fs)
	passwordFile := t.TempDir() + "/policy-etcd-password.txt"
	if err := os.WriteFile(passwordFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write policy etcd password file: %v", err)
	}
	if err := fs.Parse([]string{"--policy-etcd-password", "from-flag", "--policy-etcd-password-file", passwordFile, "--policy-etcd-password-env", "AEGIS_TEST_POLICY_ETCD_PASSWORD"}); err != nil {
		t.Fatalf("parse policy flags: %v", err)
	}
	if got, err := resolvePolicyEtcdPassword(cfg); err != nil || got != "from-file" {
		t.Fatalf("expected file password fallback, got %q err=%v", got, err)
	}
	t.Setenv("AEGIS_TEST_POLICY_ETCD_PASSWORD", "from-env")
	if got, err := resolvePolicyEtcdPassword(cfg); err != nil || got != "from-env" {
		t.Fatalf("expected env password priority, got %q err=%v", got, err)
	}
}

// TestRegisterSecurityFlagsAndBuildInsecureAuthForLocalTests locks the register security flags and build insecure auth for local tests contract so future changes do not regress it.
func TestRegisterSecurityFlagsAndBuildInsecureAuthForLocalTests(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--auth-tokens", "admin=root,reader:user-service=read", "--auth-allow-insecure", "--insecure-dev"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if *cfg.authTokens != "admin=root,reader:user-service=read" {
		t.Fatalf("expected auth tokens flag, got %q", *cfg.authTokens)
	}
	if !*cfg.authAllowInsecure {
		t.Fatalf("expected auth allow-insecure flag")
	}
	opts, err := buildControllerServerOptions(cfg)
	if err != nil {
		t.Fatalf("build server options: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected auth interceptors to be installed")
	}
}

// TestBuildControllerServerOptionsRejectsOpenPlaintextByDefault locks the build controller server options rejects open plaintext by default contract so future changes do not regress it.
func TestBuildControllerServerOptionsRejectsOpenPlaintextByDefault(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err == nil {
		t.Fatalf("expected open plaintext controller to be rejected by default")
	}
}

// TestBuildControllerServerOptionsAllowsExplicitInsecureDev locks the build controller server options allows explicit insecure dev contract so future changes do not regress it.
func TestBuildControllerServerOptionsAllowsExplicitInsecureDev(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--insecure-dev"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err != nil {
		t.Fatalf("expected explicit insecure dev mode to be allowed: %v", err)
	}
}

// TestBuildControllerServerOptionsRejectsPlaintextAuthByDefault locks the build controller server options rejects plaintext auth by default contract so future changes do not regress it.
func TestBuildControllerServerOptionsRejectsPlaintextAuthByDefault(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--auth-tokens", "admin=root"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err == nil {
		t.Fatalf("expected auth without TLS to be rejected by default")
	}
}

// TestBuildControllerServerOptionsAcceptsScopedAuthToken locks the build controller server options accepts scoped auth token contract so future changes do not regress it.
func TestBuildControllerServerOptionsAcceptsScopedAuthToken(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--auth-tokens", "sdk:user-service+order-service=sdk", "--auth-allow-insecure", "--insecure-dev"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	opts, err := buildControllerServerOptions(cfg)
	if err != nil {
		t.Fatalf("expected scoped auth token to be accepted: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected auth interceptors to be installed")
	}
}

// TestBuildControllerServerOptionsRejectsInvalidAuthRole locks the build controller server options rejects invalid auth role contract so future changes do not regress it.
func TestBuildControllerServerOptionsRejectsInvalidAuthRole(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--auth-tokens", "unknown=root", "--auth-allow-insecure", "--insecure-dev"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err == nil {
		t.Fatalf("expected invalid auth role to be rejected")
	}
}

// TestResolveControllerAuthTokensPrefersEnvThenFileThenFlag locks the resolve controller auth tokens prefers env then file then flag contract so future changes do not regress it.
func TestResolveControllerAuthTokensPrefersEnvThenFileThenFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	tokenFile := t.TempDir() + "/auth-tokens.txt"
	if err := os.WriteFile(tokenFile, []byte("registry=from-file\n"), 0o600); err != nil {
		t.Fatalf("write auth token file: %v", err)
	}
	if err := fs.Parse([]string{"--auth-tokens", "reader=from-flag", "--auth-tokens-file", tokenFile, "--auth-tokens-env", "AEGIS_TEST_CONTROLLER_AUTH_TOKENS"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if got, err := resolveControllerAuthTokens(cfg); err != nil || got != "registry=from-file" {
		t.Fatalf("expected file token fallback, got %q err=%v", got, err)
	}
	t.Setenv("AEGIS_TEST_CONTROLLER_AUTH_TOKENS", "admin=from-env")
	if got, err := resolveControllerAuthTokens(cfg); err != nil || got != "admin=from-env" {
		t.Fatalf("expected env token priority, got %q err=%v", got, err)
	}
}

// TestResolveControllerAuthCertPrincipalsPrefersEnvThenFileThenFlag locks the resolve controller auth cert principals prefers env then file then flag contract so future changes do not regress it.
func TestResolveControllerAuthCertPrincipalsPrefersEnvThenFileThenFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	principalFile := t.TempDir() + "/auth-cert-principals.txt"
	if err := os.WriteFile(principalFile, []byte("reader=dns:from-file.example\n"), 0o600); err != nil {
		t.Fatalf("write auth cert principal file: %v", err)
	}
	if err := fs.Parse([]string{"--auth-cert-principals", "reader=dns:from-flag.example", "--auth-cert-principals-file", principalFile, "--auth-cert-principals-env", "AEGIS_TEST_CONTROLLER_AUTH_CERT_PRINCIPALS"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if got, err := resolveControllerAuthCertPrincipals(cfg); err != nil || got != "reader=dns:from-file.example" {
		t.Fatalf("expected file cert principal fallback, got %q err=%v", got, err)
	}
	t.Setenv("AEGIS_TEST_CONTROLLER_AUTH_CERT_PRINCIPALS", "sdk:user-service=uri:spiffe://aegis/ns/default/sa/user")
	if got, err := resolveControllerAuthCertPrincipals(cfg); err != nil || got != "sdk:user-service=uri:spiffe://aegis/ns/default/sa/user" {
		t.Fatalf("expected env cert principal priority, got %q err=%v", got, err)
	}
}

// TestBuildControllerServerOptionsRejectsCertificatePrincipalsWithoutTLS locks the build controller server options rejects certificate principals without tls contract so future changes do not regress it.
func TestBuildControllerServerOptionsRejectsCertificatePrincipalsWithoutTLS(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--auth-cert-principals", "sdk:user-service=cn:sdk-client", "--insecure-dev"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err == nil {
		t.Fatalf("expected certificate principals without TLS to be rejected")
	}
}

// TestBuildControllerServerOptionsRejectsCertificatePrincipalsWithoutCA locks the build controller server options rejects certificate principals without ca contract so future changes do not regress it.
func TestBuildControllerServerOptionsRejectsCertificatePrincipalsWithoutCA(t *testing.T) {
	certFile, keyFile, _ := writeControllerServerTLSMaterial(t)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--tls-cert-file", certFile, "--tls-key-file", keyFile, "--auth-cert-principals", "sdk:user-service=cn:sdk-client", "--auth-tokens", "admin=root"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err == nil {
		t.Fatalf("expected certificate principals without CA to be rejected")
	}
}

// TestBuildControllerServerOptionsRequiresClientCertForCertificateOnlyAuth locks the build controller server options requires client cert for certificate only auth contract so future changes do not regress it.
func TestBuildControllerServerOptionsRequiresClientCertForCertificateOnlyAuth(t *testing.T) {
	certFile, keyFile, caFile := writeControllerServerTLSMaterial(t)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--tls-cert-file", certFile, "--tls-key-file", keyFile, "--tls-ca-file", caFile, "--auth-cert-principals", "sdk:user-service=cn:sdk-client"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	if _, err := buildControllerServerOptions(cfg); err == nil {
		t.Fatalf("expected certificate-only auth without --tls-require-client-cert to be rejected")
	}
}

// TestBuildControllerServerOptionsAcceptsCertificateOnlyMTLSAuth locks the build controller server options accepts certificate only mtls auth contract so future changes do not regress it.
func TestBuildControllerServerOptionsAcceptsCertificateOnlyMTLSAuth(t *testing.T) {
	certFile, keyFile, caFile := writeControllerServerTLSMaterial(t)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--tls-cert-file", certFile, "--tls-key-file", keyFile, "--tls-ca-file", caFile, "--tls-require-client-cert", "--auth-cert-principals", "sdk:user-service=cn:sdk-client"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	opts, err := buildControllerServerOptions(cfg)
	if err != nil {
		t.Fatalf("expected certificate-only mTLS auth to be accepted: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected TLS credentials and auth interceptors")
	}
}

// TestBuildControllerServerOptionsAcceptsMixedTokenAndOptionalCertificateAuth locks the build controller server options accepts mixed token and optional certificate auth contract so future changes do not regress it.
func TestBuildControllerServerOptionsAcceptsMixedTokenAndOptionalCertificateAuth(t *testing.T) {
	certFile, keyFile, caFile := writeControllerServerTLSMaterial(t)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerSecurityFlags(fs)
	if err := fs.Parse([]string{"--tls-cert-file", certFile, "--tls-key-file", keyFile, "--tls-ca-file", caFile, "--auth-tokens", "admin=root", "--auth-cert-principals", "sdk:user-service=cn:sdk-client"}); err != nil {
		t.Fatalf("parse security flags: %v", err)
	}
	opts, err := buildControllerServerOptions(cfg)
	if err != nil {
		t.Fatalf("expected mixed token and optional certificate auth to be accepted: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected TLS credentials and auth interceptors")
	}
}

// TestSplitCommaListTrimsEmptyItems locks the split comma list trims empty items contract so future changes do not regress it.
func TestSplitCommaListTrimsEmptyItems(t *testing.T) {
	got := splitCommaList(" http://127.0.0.1:2379,;http://127.0.0.2:2379 ,, ")
	if len(got) != 2 || got[0] != "http://127.0.0.1:2379" || got[1] != "http://127.0.0.2:2379" {
		t.Fatalf("unexpected split endpoints: %+v", got)
	}
}

// TestResolveEtcdPasswordPrefersEnvThenFileThenFlag locks the resolve etcd password prefers env then file then flag contract so future changes do not regress it.
func TestResolveEtcdPasswordPrefersEnvThenFileThenFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	passwordFile := t.TempDir() + "/etcd-password.txt"
	if err := os.WriteFile(passwordFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	if err := fs.Parse([]string{"--registry-etcd-password", "from-flag", "--registry-etcd-password-file", passwordFile, "--registry-etcd-password-env", "AEGIS_TEST_ETCD_PASSWORD"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if got, err := resolveEtcdPassword(cfg); err != nil || got != "from-file" {
		t.Fatalf("expected file password fallback, got %q err=%v", got, err)
	}
	t.Setenv("AEGIS_TEST_ETCD_PASSWORD", "from-env")
	if got, err := resolveEtcdPassword(cfg); err != nil || got != "from-env" {
		t.Fatalf("expected env password priority, got %q err=%v", got, err)
	}
}

// TestRegisterRegistryFlagsIncludesEtcdSecurityOptions locks the register registry flags includes etcd security options contract so future changes do not regress it.
func TestRegisterRegistryFlagsIncludesEtcdSecurityOptions(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{
		"--registry-backend", "etcd",
		"--registry-etcd-endpoints", "127.0.0.1:2379",
		"--registry-etcd-request-timeout", "750ms",
		"--registry-etcd-tls-ca-file", "ca.pem",
		"--registry-etcd-tls-cert-file", "client.pem",
		"--registry-etcd-tls-key-file", "client-key.pem",
		"--registry-etcd-tls-server-name", "etcd.internal",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if *cfg.etcdRequestTimeout != 750*time.Millisecond {
		t.Fatalf("expected request timeout flag, got %s", *cfg.etcdRequestTimeout)
	}
	if *cfg.etcdTLSCAFile != "ca.pem" || *cfg.etcdTLSCertFile != "client.pem" || *cfg.etcdTLSKeyFile != "client-key.pem" || *cfg.etcdTLSServerName != "etcd.internal" {
		t.Fatalf("unexpected etcd TLS flags: ca=%q cert=%q key=%q server=%q", *cfg.etcdTLSCAFile, *cfg.etcdTLSCertFile, *cfg.etcdTLSKeyFile, *cfg.etcdTLSServerName)
	}
}

// TestBuildRegistryFromFlagsRejectsEtcdWithoutEndpoints locks the build registry from flags rejects etcd without endpoints contract so future changes do not regress it.
func TestBuildRegistryFromFlagsRejectsEtcdWithoutEndpoints(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{"--registry-backend", "etcd"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if _, err := buildRegistry(cfg, time.Now); err == nil {
		t.Fatalf("expected etcd backend without endpoints to be rejected")
	}
}

// staticHealthSnapshotStore defines persistence operations for static health snapshot store state.
type staticHealthSnapshotStore struct {
	snapshot []fault.EndpointHealth
}

// Load reads the current state from the configured backing source.
func (s *staticHealthSnapshotStore) Load(context.Context) ([]fault.EndpointHealth, int64, error) {
	return append([]fault.EndpointHealth(nil), s.snapshot...), 7, nil
}

// Save persists save state to the backing store.
func (s *staticHealthSnapshotStore) Save(context.Context, []fault.EndpointHealth) (int64, error) {
	return 0, nil
}

// Watch streams backing-source changes to callers until the source or context closes.
func (s *staticHealthSnapshotStore) Watch(ctx context.Context, _ int64) (<-chan fault.HealthStoreEvent, error) {
	ch := make(chan fault.HealthStoreEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// Close closes owned resources and makes repeated calls safe.
func (s *staticHealthSnapshotStore) Close() error { return nil }

// writeControllerServerTLSMaterial writes write controller server tls material data to the configured output.
func writeControllerServerTLSMaterial(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	caKey := newControllerTestKey(t)
	caCert := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "AegisMesh Controller Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caCert, caCert, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caFile := filepath.Join(dir, "ca.pem")
	writeControllerTestPEM(t, caFile, "CERTIFICATE", caDER)

	serverKey := newControllerTestKey(t)
	serverCert := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "controller.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"controller.test"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverCert, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	certFile := filepath.Join(dir, "server.pem")
	keyFile := filepath.Join(dir, "server-key.pem")
	writeControllerTestPEM(t, certFile, "CERTIFICATE", serverDER)
	writeControllerTestPEM(t, keyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey))
	return certFile, keyFile, caFile
}

// newControllerTestKey initializes controller test key with package defaults for this package's call path.
func newControllerTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	return key
}

// writeControllerTestPEM writes write controller test pem data to the configured output.
func writeControllerTestPEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := pem.Encode(file, &pem.Block{Type: typ, Bytes: der}); err != nil {
		_ = file.Close()
		t.Fatalf("write PEM %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// TestBuildRegistryFromFlagsSelectsEtcdBackend locks the build registry from flags selects etcd backend contract so future changes do not regress it.
func TestBuildRegistryFromFlagsSelectsEtcdBackend(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{"--registry-backend", "etcd", "--registry-etcd-endpoints", "127.0.0.1:2379,127.0.0.2:2379", "--registry-etcd-prefix", "/aegis/test", "--registry-etcd-dial-timeout", "250ms"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	store, err := buildRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("build etcd registry: %v", err)
	}
	if _, ok := store.(interface {
		Snapshot(context.Context, string) (registry.InstanceSnapshot, error)
	}); !ok {
		t.Fatalf("expected etcd registry to expose snapshots, got %T", store)
	}
	if closer, ok := store.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("close etcd registry: %v", err)
		}
	}
}

// TestBuildRegistryFromFlagsSelectsFileV2Backend locks the build registry from flags selects file v2 backend contract so future changes do not regress it.
func TestBuildRegistryFromFlagsSelectsFileV2Backend(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := registerRegistryFlags(fs)
	if err := fs.Parse([]string{"--registry-backend", "file-v2", "--registry-file", t.TempDir() + "/registry.json", "--registry-file-v2-sync", "always", "--registry-file-v2-flush-records", "1", "--registry-file-v2-flush-bytes", "256", "--registry-file-v2-flush-interval", "1ms", "--registry-file-v2-compact-bytes", "1024"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	store, err := buildRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	persistent, ok := store.(interface {
		PersistencePath() string
		WALPath() string
		Close() error
	})
	if !ok {
		t.Fatalf("expected file-v2 registry backend, got %T", store)
	}
	if persistent.WALPath() == "" {
		t.Fatalf("expected file-v2 registry to expose WAL path")
	}
	if err := persistent.Close(); err != nil {
		t.Fatalf("close file-v2 registry: %v", err)
	}
}
