package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/controller"
	"github.com/aegismesh/aegismesh/pkg/fault"
	"github.com/aegismesh/aegismesh/pkg/policy"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"github.com/aegismesh/aegismesh/pkg/security"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9000", "controller gRPC listen address")
	httpAddr := flag.String("http-addr", "127.0.0.1:9100", "controller HTTP metrics listen address")
	lease := flag.Duration("lease", 30*time.Second, "default service instance lease")
	sweepInterval := flag.Duration("sweep-interval", 5*time.Second, "expired instance sweep interval")
	registryCfg := registerRegistryFlags(flag.CommandLine)
	policyCfg := registerPolicyFlags(flag.CommandLine)
	healthStateCfg := registerHealthStateFlags(flag.CommandLine)
	securityCfg := registerSecurityFlags(flag.CommandLine)
	healthTickInterval := flag.Duration("health-tick-interval", time.Second, "endpoint health state-machine tick interval")
	healthDegradedThreshold := flag.Float64("health-degraded-threshold", 0, "slow_score threshold for DEGRADED; 0 uses default")
	healthEjectThreshold := flag.Float64("health-eject-threshold", 0, "slow_score threshold for EJECTED; 0 uses default")
	healthConsecutiveWindows := flag.Int("health-consecutive-windows", 0, "consecutive windows before state transition; 0 uses default")
	healthEjectionDuration := flag.Duration("health-ejection-duration", 0, "duration before EJECTED moves to PROBING; 0 uses default")
	healthRecoveryThreshold := flag.Float64("health-recovery-threshold", 0, "slow_score threshold for recovery; 0 uses default")
	healthProbeSuccessThreshold := flag.Float64("health-probe-success-threshold", 0, "success rate threshold during PROBING; 0 uses default")
	healthLatencySLO := flag.Duration("health-latency-slo", 0, "absolute p95 latency SLO used by slow_score; 0 disables absolute SLO scoring")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := buildRegistry(registryCfg, time.Now)
	if err != nil {
		log.Fatalf("build registry: %v", err)
	}
	defer closeRegistryStore(store)
	policyStore, err := buildPolicyStore(ctx, policyCfg)
	if err != nil {
		log.Fatalf("build policy store: %v", err)
	}
	defer closePolicyStore(policyStore)
	healthManager := fault.NewHealthManager(buildHealthManagerConfig(
		*healthDegradedThreshold,
		*healthEjectThreshold,
		*healthConsecutiveWindows,
		*healthEjectionDuration,
		*healthRecoveryThreshold,
		*healthProbeSuccessThreshold,
		*healthLatencySLO,
	))
	healthStore, err := buildHealthSnapshotStore(ctx, healthStateCfg)
	if err != nil {
		log.Fatalf("build health state store: %v", err)
	}
	defer closeHealthSnapshotStore(healthStore)
	healthRevision := int64(0)
	if healthStore != nil {
		revision, restored, err := restoreHealthSnapshot(ctx, healthStore, healthManager, store, *healthStateCfg.maxAge)
		if err != nil {
			log.Fatalf("restore health state: %v", err)
		}
		healthRevision = revision
		log.Printf("restored %d health endpoint(s) from %s", restored, describeHealthStateSource(healthStateCfg))
	}
	healthMetrics, err := fault.NewPrometheusHealthMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		log.Fatalf("register health prometheus metrics: %v", err)
	}

	go sweepExpired(ctx, store, healthManager, *sweepInterval)
	go tickHealth(ctx, healthManager, healthMetrics, healthStore, *healthTickInterval)
	if *httpAddr != "" {
		go serveMetrics(ctx, *httpAddr)
	}

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen controller: %v", err)
	}

	serverOptions, err := buildControllerServerOptions(securityCfg)
	if err != nil {
		log.Fatalf("build controller security options: %v", err)
	}
	server := grpc.NewServer(serverOptions...)
	aegisv1.RegisterRegistryServiceServer(server, controller.NewRegistryServiceWithHealth(store, *lease, healthManager))
	aegisv1.RegisterTelemetryServiceServer(server, controller.NewTelemetryServiceWithHealthStore(store, healthManager, healthMetrics, healthStore))
	if policyStore != nil {
		aegisv1.RegisterPolicyServiceServer(server, controller.NewPolicyService(policyStore, *policyCfg.reloadInterval))
		if startPolicyHotApplyLoop(ctx, policyStore, healthManager, *policyCfg.reloadInterval) {
			log.Printf("policy hot-apply enabled from %s", describePolicySource(policyCfg))
		}
	}
	if healthStore != nil {
		go watchHealthSnapshots(ctx, healthStore, healthManager, store, healthRevision, *healthStateCfg.maxAge)
	}

	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	log.Printf("aegis controller listening on %s", *addr)
	if err := server.Serve(lis); err != nil {
		log.Fatalf("serve controller: %v", err)
	}
}

type registryFlags struct {
	backend             *string
	file                *string
	fileV2Sync          *string
	fileV2FlushInterval *time.Duration
	fileV2FlushRecords  *int
	fileV2FlushBytes    *int
	fileV2CompactBytes  *int64
	etcdEndpoints       *string
	etcdPrefix          *string
	etcdDialTimeout     *time.Duration
	etcdRequestTimeout  *time.Duration
	etcdUsername        *string
	etcdPassword        *string
	etcdPasswordEnv     *string
	etcdPasswordFile    *string
	etcdTLSCAFile       *string
	etcdTLSCertFile     *string
	etcdTLSKeyFile      *string
	etcdTLSServerName   *string
}

func registerRegistryFlags(fs *flag.FlagSet) registryFlags {
	return registryFlags{
		backend:             fs.String("registry-backend", "memory", "registry backend: memory, file, file-v2, or etcd"),
		file:                fs.String("registry-file", "data/aegis-registry.json", "file registry snapshot path when --registry-backend=file or file-v2"),
		fileV2Sync:          fs.String("registry-file-v2-sync", string(registry.FileRegistrySyncBatch), "file-v2 WAL sync mode: batch or always"),
		fileV2FlushInterval: fs.Duration("registry-file-v2-flush-interval", 2*time.Millisecond, "file-v2 group commit flush/fsync interval"),
		fileV2FlushRecords:  fs.Int("registry-file-v2-flush-records", 64, "file-v2 group commit flush/fsync record threshold"),
		fileV2FlushBytes:    fs.Int("registry-file-v2-flush-bytes", 64*1024, "file-v2 group commit flush/fsync byte threshold"),
		fileV2CompactBytes:  fs.Int64("registry-file-v2-compact-bytes", 16*1024*1024, "file-v2 WAL size threshold that triggers snapshot compaction; <=0 disables automatic compaction"),
		etcdEndpoints:       fs.String("registry-etcd-endpoints", "", "comma-separated etcd endpoints when --registry-backend=etcd"),
		etcdPrefix:          fs.String("registry-etcd-prefix", "/aegismesh/registry", "etcd key prefix for registry leases"),
		etcdDialTimeout:     fs.Duration("registry-etcd-dial-timeout", 3*time.Second, "etcd client dial timeout"),
		etcdRequestTimeout:  fs.Duration("registry-etcd-request-timeout", 3*time.Second, "etcd per-request timeout for registry operations"),
		etcdUsername:        fs.String("registry-etcd-username", "", "optional etcd username"),
		etcdPassword:        fs.String("registry-etcd-password", "", "optional etcd password; prefer --registry-etcd-password-env or --registry-etcd-password-file"),
		etcdPasswordEnv:     fs.String("registry-etcd-password-env", "AEGIS_REGISTRY_ETCD_PASSWORD", "environment variable containing etcd password"),
		etcdPasswordFile:    fs.String("registry-etcd-password-file", "", "file containing etcd password"),
		etcdTLSCAFile:       fs.String("registry-etcd-tls-ca-file", "", "CA certificate file for etcd TLS"),
		etcdTLSCertFile:     fs.String("registry-etcd-tls-cert-file", "", "client certificate file for etcd mTLS"),
		etcdTLSKeyFile:      fs.String("registry-etcd-tls-key-file", "", "client key file for etcd mTLS"),
		etcdTLSServerName:   fs.String("registry-etcd-tls-server-name", "", "server name override for etcd TLS"),
	}
}

func closeRegistryStore(store registry.Registry) {
	closer, ok := store.(interface{ Close() error })
	if !ok || closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		log.Printf("close registry backend: %v", err)
	}
}
func buildRegistry(cfg registryFlags, now func() time.Time) (registry.Registry, error) {
	switch *cfg.backend {
	case "", "memory":
		return registry.NewMemoryRegistry(now), nil
	case "file":
		return registry.NewFileRegistry(*cfg.file, now)
	case "file-v2":
		return registry.NewFileRegistryV2(
			*cfg.file,
			now,
			registry.WithFileRegistryV2SyncMode(registry.FileRegistrySyncMode(*cfg.fileV2Sync)),
			registry.WithFileRegistryV2GroupCommit(*cfg.fileV2FlushRecords, *cfg.fileV2FlushBytes, *cfg.fileV2FlushInterval),
			registry.WithFileRegistryV2CompactBytes(*cfg.fileV2CompactBytes),
		)
	case "etcd":
		password, err := resolveEtcdPassword(cfg)
		if err != nil {
			return nil, err
		}
		return registry.NewEtcdRegistry(registry.EtcdRegistryConfig{
			Endpoints:      splitCommaList(*cfg.etcdEndpoints),
			Prefix:         *cfg.etcdPrefix,
			DialTimeout:    *cfg.etcdDialTimeout,
			RequestTimeout: *cfg.etcdRequestTimeout,
			Username:       *cfg.etcdUsername,
			Password:       password,
			TLS: security.TLSConfig{
				CAFile:     *cfg.etcdTLSCAFile,
				CertFile:   *cfg.etcdTLSCertFile,
				KeyFile:    *cfg.etcdTLSKeyFile,
				ServerName: *cfg.etcdTLSServerName,
			},
		}, now)
	default:
		return nil, fmt.Errorf("unsupported registry backend %q", *cfg.backend)
	}
}
func splitCommaList(raw string) []string {
	items := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
func resolveEtcdPassword(cfg registryFlags) (string, error) {
	if cfg.etcdPasswordEnv != nil && *cfg.etcdPasswordEnv != "" {
		if value := os.Getenv(*cfg.etcdPasswordEnv); value != "" {
			return value, nil
		}
	}
	if cfg.etcdPasswordFile != nil && *cfg.etcdPasswordFile != "" {
		raw, err := os.ReadFile(*cfg.etcdPasswordFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if cfg.etcdPassword != nil {
		return *cfg.etcdPassword, nil
	}
	return "", nil
}

type policyFlags struct {
	backend            *string
	file               *string
	reloadInterval     *time.Duration
	etcdEndpoints      *string
	etcdPrefix         *string
	etcdDialTimeout    *time.Duration
	etcdRequestTimeout *time.Duration
	etcdUsername       *string
	etcdPassword       *string
	etcdPasswordEnv    *string
	etcdPasswordFile   *string
	etcdTLSCAFile      *string
	etcdTLSCertFile    *string
	etcdTLSKeyFile     *string
	etcdTLSServerName  *string
}

func registerPolicyFlags(fs *flag.FlagSet) policyFlags {
	return policyFlags{
		backend:            fs.String("policy-backend", "file", "policy backend: file or etcd"),
		file:               fs.String("policy-file", "", "YAML policy file when --policy-backend=file; empty disables file PolicyService"),
		reloadInterval:     fs.Duration("policy-reload-interval", 3*time.Second, "PolicyService WatchPolicy polling interval over the local policy cache"),
		etcdEndpoints:      fs.String("policy-etcd-endpoints", "", "comma-separated etcd endpoints when --policy-backend=etcd"),
		etcdPrefix:         fs.String("policy-etcd-prefix", "/aegismesh/policy/v1", "etcd key prefix for policy snapshots"),
		etcdDialTimeout:    fs.Duration("policy-etcd-dial-timeout", 3*time.Second, "etcd client dial timeout for policy backend"),
		etcdRequestTimeout: fs.Duration("policy-etcd-request-timeout", 3*time.Second, "etcd per-request timeout for policy backend"),
		etcdUsername:       fs.String("policy-etcd-username", "", "optional etcd username for policy backend"),
		etcdPassword:       fs.String("policy-etcd-password", "", "optional etcd password for policy backend; prefer --policy-etcd-password-env or --policy-etcd-password-file"),
		etcdPasswordEnv:    fs.String("policy-etcd-password-env", "AEGIS_POLICY_ETCD_PASSWORD", "environment variable containing policy etcd password"),
		etcdPasswordFile:   fs.String("policy-etcd-password-file", "", "file containing policy etcd password"),
		etcdTLSCAFile:      fs.String("policy-etcd-tls-ca-file", "", "CA certificate file for policy etcd TLS"),
		etcdTLSCertFile:    fs.String("policy-etcd-tls-cert-file", "", "client certificate file for policy etcd mTLS"),
		etcdTLSKeyFile:     fs.String("policy-etcd-tls-key-file", "", "client key file for policy etcd mTLS"),
		etcdTLSServerName:  fs.String("policy-etcd-tls-server-name", "", "server name override for policy etcd TLS"),
	}
}

func buildPolicyStore(ctx context.Context, cfg policyFlags) (controller.PolicyStore, error) {
	backend := "file"
	if cfg.backend != nil && *cfg.backend != "" {
		backend = *cfg.backend
	}
	switch backend {
	case "file":
		if cfg.file == nil || *cfg.file == "" {
			return nil, nil
		}
		return policy.NewFileStore(*cfg.file)
	case "etcd":
		password, err := resolvePolicyEtcdPassword(cfg)
		if err != nil {
			return nil, err
		}
		return policy.NewEtcdStore(ctx, policy.EtcdStoreConfig{
			Endpoints:      splitCommaList(*cfg.etcdEndpoints),
			Prefix:         *cfg.etcdPrefix,
			DialTimeout:    *cfg.etcdDialTimeout,
			RequestTimeout: *cfg.etcdRequestTimeout,
			Username:       *cfg.etcdUsername,
			Password:       password,
			TLS: security.TLSConfig{
				CAFile:     *cfg.etcdTLSCAFile,
				CertFile:   *cfg.etcdTLSCertFile,
				KeyFile:    *cfg.etcdTLSKeyFile,
				ServerName: *cfg.etcdTLSServerName,
			},
		})
	default:
		return nil, fmt.Errorf("unsupported policy backend %q", backend)
	}
}

func closePolicyStore(store controller.PolicyStore) {
	closer, ok := store.(interface{ Close() error })
	if !ok || closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		log.Printf("close policy backend: %v", err)
	}
}

func resolvePolicyEtcdPassword(cfg policyFlags) (string, error) {
	if cfg.etcdPasswordEnv != nil && *cfg.etcdPasswordEnv != "" {
		if value := os.Getenv(*cfg.etcdPasswordEnv); value != "" {
			return value, nil
		}
	}
	if cfg.etcdPasswordFile != nil && *cfg.etcdPasswordFile != "" {
		raw, err := os.ReadFile(*cfg.etcdPasswordFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if cfg.etcdPassword != nil {
		return *cfg.etcdPassword, nil
	}
	return "", nil
}

func describePolicySource(cfg policyFlags) string {
	backend := "file"
	if cfg.backend != nil && *cfg.backend != "" {
		backend = *cfg.backend
	}
	if backend == "etcd" {
		return "etcd:" + *cfg.etcdPrefix
	}
	if cfg.file == nil {
		return "file"
	}
	return *cfg.file
}

type healthStateFlags struct {
	backend            *string
	maxAge             *time.Duration
	etcdEndpoints      *string
	etcdPrefix         *string
	etcdDialTimeout    *time.Duration
	etcdRequestTimeout *time.Duration
	etcdUsername       *string
	etcdPassword       *string
	etcdPasswordEnv    *string
	etcdPasswordFile   *string
	etcdTLSCAFile      *string
	etcdTLSCertFile    *string
	etcdTLSKeyFile     *string
	etcdTLSServerName  *string
}

func registerHealthStateFlags(fs *flag.FlagSet) healthStateFlags {
	return healthStateFlags{
		backend:            fs.String("health-state-backend", "none", "health state backend: none or etcd"),
		maxAge:             fs.Duration("health-state-max-age", 5*time.Minute, "maximum age for restored/shared health snapshots; <=0 disables staleness filtering"),
		etcdEndpoints:      fs.String("health-state-etcd-endpoints", "", "comma-separated etcd endpoints when --health-state-backend=etcd"),
		etcdPrefix:         fs.String("health-state-etcd-prefix", "/aegismesh/health/v1", "etcd key prefix for health snapshots"),
		etcdDialTimeout:    fs.Duration("health-state-etcd-dial-timeout", 3*time.Second, "etcd client dial timeout for health state backend"),
		etcdRequestTimeout: fs.Duration("health-state-etcd-request-timeout", 3*time.Second, "etcd per-request timeout for health state backend"),
		etcdUsername:       fs.String("health-state-etcd-username", "", "optional etcd username for health state backend"),
		etcdPassword:       fs.String("health-state-etcd-password", "", "optional etcd password for health state backend; prefer --health-state-etcd-password-env or --health-state-etcd-password-file"),
		etcdPasswordEnv:    fs.String("health-state-etcd-password-env", "AEGIS_HEALTH_STATE_ETCD_PASSWORD", "environment variable containing health state etcd password"),
		etcdPasswordFile:   fs.String("health-state-etcd-password-file", "", "file containing health state etcd password"),
		etcdTLSCAFile:      fs.String("health-state-etcd-tls-ca-file", "", "CA certificate file for health state etcd TLS"),
		etcdTLSCertFile:    fs.String("health-state-etcd-tls-cert-file", "", "client certificate file for health state etcd mTLS"),
		etcdTLSKeyFile:     fs.String("health-state-etcd-tls-key-file", "", "client key file for health state etcd mTLS"),
		etcdTLSServerName:  fs.String("health-state-etcd-tls-server-name", "", "server name override for health state etcd TLS"),
	}
}

func buildHealthSnapshotStore(ctx context.Context, cfg healthStateFlags) (fault.HealthSnapshotStore, error) {
	backend := "none"
	if cfg.backend != nil && *cfg.backend != "" {
		backend = *cfg.backend
	}
	switch backend {
	case "", "none":
		return nil, nil
	case "etcd":
		password, err := resolveHealthEtcdPassword(cfg)
		if err != nil {
			return nil, err
		}
		return fault.NewEtcdHealthStore(ctx, fault.EtcdHealthStoreConfig{
			Endpoints:      splitCommaList(*cfg.etcdEndpoints),
			Prefix:         *cfg.etcdPrefix,
			DialTimeout:    *cfg.etcdDialTimeout,
			RequestTimeout: *cfg.etcdRequestTimeout,
			Username:       *cfg.etcdUsername,
			Password:       password,
			TLS: security.TLSConfig{
				CAFile:     *cfg.etcdTLSCAFile,
				CertFile:   *cfg.etcdTLSCertFile,
				KeyFile:    *cfg.etcdTLSKeyFile,
				ServerName: *cfg.etcdTLSServerName,
			},
		})
	default:
		return nil, fmt.Errorf("unsupported health state backend %q", backend)
	}
}

func closeHealthSnapshotStore(store fault.HealthSnapshotStore) {
	if store == nil {
		return
	}
	if err := store.Close(); err != nil {
		log.Printf("close health state backend: %v", err)
	}
}

func resolveHealthEtcdPassword(cfg healthStateFlags) (string, error) {
	if cfg.etcdPasswordEnv != nil && *cfg.etcdPasswordEnv != "" {
		if value := os.Getenv(*cfg.etcdPasswordEnv); value != "" {
			return value, nil
		}
	}
	if cfg.etcdPasswordFile != nil && *cfg.etcdPasswordFile != "" {
		raw, err := os.ReadFile(*cfg.etcdPasswordFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if cfg.etcdPassword != nil {
		return *cfg.etcdPassword, nil
	}
	return "", nil
}

func restoreHealthSnapshot(ctx context.Context, store fault.HealthSnapshotStore, manager *fault.HealthManager, registryStore registry.Registry, maxAge time.Duration) (int64, int, error) {
	if store == nil || manager == nil {
		return 0, 0, nil
	}
	snapshot, revision, err := store.Load(ctx)
	if err != nil {
		return 0, 0, err
	}
	snapshot = filterFreshHealth(snapshot, time.Now(), maxAge)
	snapshot, err = filterActiveRegistryHealth(ctx, registryStore, snapshot)
	if err != nil {
		return 0, 0, err
	}
	merged := manager.MergeSnapshot(snapshot)
	return revision, merged, nil
}

func watchHealthSnapshots(ctx context.Context, store fault.HealthSnapshotStore, manager *fault.HealthManager, registryStore registry.Registry, afterRevision int64, maxAge time.Duration) {
	backoff := 500 * time.Millisecond
	for ctx.Err() == nil {
		updates, err := store.Watch(ctx, afterRevision)
		if err != nil {
			log.Printf("watch health state snapshots: %v", err)
			if !sleepOrDone(ctx, backoff) {
				return
			}
			continue
		}
		for event := range updates {
			if ctx.Err() != nil {
				return
			}
			if event.Revision > afterRevision {
				afterRevision = event.Revision
			}
			if event.Err != nil {
				log.Printf("watch health state snapshots: %v", event.Err)
				break
			}
			revision, merged, err := restoreHealthSnapshot(ctx, store, manager, registryStore, maxAge)
			if err != nil {
				log.Printf("reload health state snapshots: %v", err)
				continue
			}
			if revision > afterRevision {
				afterRevision = revision
			}
			if merged > 0 {
				log.Printf("merged %d health endpoint(s) from shared health state", merged)
			}
		}
		if !sleepOrDone(ctx, backoff) {
			return
		}
	}
}

func filterFreshHealth(snapshot []fault.EndpointHealth, now time.Time, maxAge time.Duration) []fault.EndpointHealth {
	if maxAge <= 0 {
		return snapshot
	}
	out := snapshot[:0]
	for _, health := range snapshot {
		if health.UpdatedAt.IsZero() {
			continue
		}
		if now.Sub(health.UpdatedAt) > maxAge {
			continue
		}
		out = append(out, health)
	}
	return out
}

func filterActiveRegistryHealth(ctx context.Context, store registry.Registry, snapshot []fault.EndpointHealth) ([]fault.EndpointHealth, error) {
	if store == nil || len(snapshot) == 0 {
		return snapshot, nil
	}
	active, err := activeRegistryEndpoints(ctx, store, snapshot)
	if err != nil {
		return nil, err
	}
	out := snapshot[:0]
	for _, health := range snapshot {
		address, ok := activeRegistryHealthAddress(active, health)
		if ok && healthMatchesRegistryAddress(health.Address, address) {
			out = append(out, health)
		}
	}
	return out, nil
}

func activeRegistryEndpoints(ctx context.Context, store registry.Registry, health []fault.EndpointHealth) (map[fault.EndpointIdentity]string, error) {
	services := make(map[string]struct{})
	for _, endpoint := range health {
		if endpoint.Service != "" {
			services[endpoint.Service] = struct{}{}
		}
	}
	active := make(map[fault.EndpointIdentity]string)
	for service := range services {
		instances, err := store.List(ctx, service)
		if err != nil {
			return nil, err
		}
		for _, inst := range instances {
			if inst.Service == "" || inst.ID == "" {
				continue
			}
			active[fault.EndpointIdentity{Service: inst.Service, InstanceID: inst.ID, RegistrationEpoch: inst.RegistrationEpoch}] = inst.Address
		}
	}
	return active, nil
}

func activeRegistryHealthAddress(active map[fault.EndpointIdentity]string, health fault.EndpointHealth) (string, bool) {
	identity := fault.EndpointIdentity{Service: health.Service, InstanceID: health.InstanceID, RegistrationEpoch: health.RegistrationEpoch}
	if address, ok := active[identity]; ok {
		return address, true
	}
	legacy := fault.EndpointIdentity{Service: health.Service, InstanceID: health.InstanceID}
	if address, ok := active[legacy]; ok {
		return address, true
	}
	if health.RegistrationEpoch == "" {
		for candidate, address := range active {
			if candidate.Service == health.Service && candidate.InstanceID == health.InstanceID {
				return address, true
			}
		}
	}
	return "", false
}
func healthMatchesRegistryAddress(healthAddress, registryAddress string) bool {
	return healthAddress == "" || registryAddress == "" || healthAddress == registryAddress
}

func pruneHealthMissingFromRegistry(ctx context.Context, store registry.Registry, manager *fault.HealthManager) (int, error) {
	if store == nil || manager == nil {
		return 0, nil
	}
	health := manager.Snapshot()
	if len(health) == 0 {
		return 0, nil
	}
	retain, err := activeRegistryEndpoints(ctx, store, health)
	if err != nil {
		return 0, err
	}
	return manager.PruneMissing(retain), nil
}

func sleepOrDone(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func describeHealthStateSource(cfg healthStateFlags) string {
	backend := "none"
	if cfg.backend != nil && *cfg.backend != "" {
		backend = *cfg.backend
	}
	if backend == "etcd" {
		return "etcd:" + *cfg.etcdPrefix
	}
	return backend
}

type securityFlags struct {
	tlsCertFile            *string
	tlsKeyFile             *string
	tlsCAFile              *string
	tlsRequireClientCert   *bool
	authTokens             *string
	authTokensEnv          *string
	authTokensFile         *string
	authCertPrincipals     *string
	authCertPrincipalsEnv  *string
	authCertPrincipalsFile *string
	insecureDev            *bool
	authAllowInsecure      *bool
}

func registerSecurityFlags(fs *flag.FlagSet) securityFlags {
	return securityFlags{
		tlsCertFile:            fs.String("tls-cert-file", "", "controller server TLS certificate file; empty disables server TLS"),
		tlsKeyFile:             fs.String("tls-key-file", "", "controller server TLS private key file"),
		tlsCAFile:              fs.String("tls-ca-file", "", "CA certificate file for client certificate verification and mTLS"),
		tlsRequireClientCert:   fs.Bool("tls-require-client-cert", false, "require and verify client certificates with --tls-ca-file"),
		authTokens:             fs.String("auth-tokens", "", "comma-separated static controller auth tokens as role[:service+service]=token; roles: admin,registry,telemetry,policy,reader,sdk; prefer --auth-tokens-env or --auth-tokens-file"),
		authTokensEnv:          fs.String("auth-tokens-env", "AEGIS_CONTROLLER_AUTH_TOKENS", "environment variable containing static controller auth tokens"),
		authTokensFile:         fs.String("auth-tokens-file", "", "file containing static controller auth tokens"),
		authCertPrincipals:     fs.String("auth-cert-principals", "", "comma-separated static mTLS principal mappings as role[:service+service]=uri:<URI-SAN>|dns:<DNS-SAN>|cn:<CommonName>; prefer --auth-cert-principals-env or --auth-cert-principals-file"),
		authCertPrincipalsEnv:  fs.String("auth-cert-principals-env", "AEGIS_CONTROLLER_AUTH_CERT_PRINCIPALS", "environment variable containing static mTLS principal mappings"),
		authCertPrincipalsFile: fs.String("auth-cert-principals-file", "", "file containing static mTLS principal mappings"),
		insecureDev:            fs.Bool("insecure-dev", false, "allow controller to start without TLS and auth; intended only for local demos/tests"),
		authAllowInsecure:      fs.Bool("auth-allow-insecure", false, "allow bearer token auth without TLS; intended only for local tests"),
	}
}

func resolveControllerAuthTokens(cfg securityFlags) (string, error) {
	if cfg.authTokensEnv != nil && *cfg.authTokensEnv != "" {
		if value := os.Getenv(*cfg.authTokensEnv); value != "" {
			return value, nil
		}
	}
	if cfg.authTokensFile != nil && *cfg.authTokensFile != "" {
		raw, err := os.ReadFile(*cfg.authTokensFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if cfg.authTokens != nil {
		return *cfg.authTokens, nil
	}
	return "", nil
}
func resolveControllerAuthCertPrincipals(cfg securityFlags) (string, error) {
	if cfg.authCertPrincipalsEnv != nil && *cfg.authCertPrincipalsEnv != "" {
		if value := os.Getenv(*cfg.authCertPrincipalsEnv); value != "" {
			return value, nil
		}
	}
	if cfg.authCertPrincipalsFile != nil && *cfg.authCertPrincipalsFile != "" {
		raw, err := os.ReadFile(*cfg.authCertPrincipalsFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	if cfg.authCertPrincipals != nil {
		return *cfg.authCertPrincipals, nil
	}
	return "", nil
}
func buildControllerServerOptions(cfg securityFlags) ([]grpc.ServerOption, error) {
	tlsCfg := security.TLSConfig{
		CertFile:          *cfg.tlsCertFile,
		KeyFile:           *cfg.tlsKeyFile,
		CAFile:            *cfg.tlsCAFile,
		RequireClientCert: *cfg.tlsRequireClientCert,
	}
	creds, err := security.ServerTransportCredentials(tlsCfg)
	if err != nil {
		return nil, err
	}
	options := make([]grpc.ServerOption, 0, 3)
	if creds != nil {
		options = append(options, grpc.Creds(creds))
	}

	authTokens, err := resolveControllerAuthTokens(cfg)
	if err != nil {
		return nil, err
	}
	tokens, err := security.ParseStaticTokenPrincipals(authTokens)
	if err != nil {
		return nil, err
	}
	authCertPrincipals, err := resolveControllerAuthCertPrincipals(cfg)
	if err != nil {
		return nil, err
	}
	certPrincipals, err := security.ParseStaticMTLSPrincipals(authCertPrincipals)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 && len(certPrincipals) == 0 && !*cfg.insecureDev {
		return nil, fmt.Errorf("controller requires auth tokens or mTLS cert principals unless --insecure-dev is set")
	}
	if !tlsCfg.Enabled() && len(tokens) > 0 && !*cfg.authAllowInsecure {
		return nil, fmt.Errorf("--auth-tokens requires TLS unless --auth-allow-insecure is set")
	}
	if !tlsCfg.Enabled() && !*cfg.insecureDev && len(tokens) > 0 {
		return nil, fmt.Errorf("controller requires TLS unless --insecure-dev is set")
	}
	if len(certPrincipals) > 0 {
		if !tlsCfg.Enabled() {
			return nil, fmt.Errorf("--auth-cert-principals requires controller TLS")
		}
		if *cfg.tlsCAFile == "" {
			return nil, fmt.Errorf("--auth-cert-principals requires --tls-ca-file for verified client certificates")
		}
		if len(tokens) == 0 && !*cfg.tlsRequireClientCert {
			return nil, fmt.Errorf("certificate-only auth requires --tls-require-client-cert")
		}
	}
	if len(tokens) > 0 || len(certPrincipals) > 0 {
		auth := security.NewControllerPrincipalTokenAuthenticatorWithMTLS(tokens, certPrincipals)
		options = append(options,
			grpc.ChainUnaryInterceptor(auth.UnaryServerInterceptor()),
			grpc.ChainStreamInterceptor(auth.StreamServerInterceptor()),
		)
	}
	return options, nil
}

func startPolicyHotApplyLoop(ctx context.Context, store controller.PolicyStore, manager *fault.HealthManager, interval time.Duration) bool {
	lister, ok := store.(controller.PolicySnapshotLister)
	if !ok || manager == nil {
		return false
	}
	if changed := controller.ApplyOutlierDetectionPolicies(lister, manager); changed > 0 {
		log.Printf("applied outlier policy to %d service(s)", changed)
	}
	go controller.RunPolicyHotApplyLoop(ctx, lister, manager, interval, log.Printf)
	return true
}

func buildStateMachineConfig(degradedThreshold float64, ejectThreshold float64, consecutiveWindows int, ejectionDuration time.Duration, recoveryThreshold float64, probeSuccessThreshold float64) fault.StateMachineConfig {
	return fault.StateMachineConfig{
		DegradedThreshold:     degradedThreshold,
		EjectThreshold:        ejectThreshold,
		ConsecutiveWindows:    consecutiveWindows,
		EjectionDuration:      ejectionDuration,
		RecoveryThreshold:     recoveryThreshold,
		ProbeSuccessThreshold: probeSuccessThreshold,
	}
}

func buildHealthManagerConfig(degradedThreshold float64, ejectThreshold float64, consecutiveWindows int, ejectionDuration time.Duration, recoveryThreshold float64, probeSuccessThreshold float64, latencySLO time.Duration) fault.HealthManagerConfig {
	return fault.HealthManagerConfig{
		StateMachine: buildStateMachineConfig(
			degradedThreshold,
			ejectThreshold,
			consecutiveWindows,
			ejectionDuration,
			recoveryThreshold,
			probeSuccessThreshold,
		),
		LatencySLO: latencySLO,
	}
}

func sweepExpired(ctx context.Context, store registry.Registry, manager *fault.HealthManager, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired := store.SweepExpired(ctx)
			if expired > 0 {
				log.Printf("expired %d service instance(s)", expired)
			}
			pruned, err := pruneHealthMissingFromRegistry(ctx, store, manager)
			if err != nil {
				log.Printf("prune inactive health endpoint(s): %v", err)
			} else if pruned > 0 {
				log.Printf("pruned %d inactive health endpoint(s)", pruned)
			}
		}
	}
}

func tickHealth(ctx context.Context, manager *fault.HealthManager, metrics *fault.PrometheusHealthMetrics, healthStore fault.HealthSnapshotStore, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			health := manager.Tick()
			for _, endpoint := range health {
				metrics.RecordHealth(endpoint)
			}
			if healthStore != nil {
				if _, err := healthStore.Save(ctx, health); err != nil {
					log.Printf("persist health tick snapshot: %v", err)
				}
			}
		}
	}
}

func serveMetrics(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("controller metrics listening on http://%s/metrics", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("serve controller metrics: %v", err)
	}
}
