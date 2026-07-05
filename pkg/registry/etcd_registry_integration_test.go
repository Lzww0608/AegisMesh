package registry

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEtcdRegistryIntegrationSharedStateAcrossControllers locks the etcd registry integration shared state across controllers contract so future changes do not regress it.
func TestEtcdRegistryIntegrationSharedStateAcrossControllers(t *testing.T) {
	cfg := testEtcdRegistryConfig(t)
	nowA := time.Unix(1000, 0)
	nowB := nowA.Add(10 * time.Second)
	controllerA, err := NewEtcdRegistry(cfg, func() time.Time { return nowA })
	if err != nil {
		t.Fatalf("create controller A etcd registry: %v", err)
	}
	defer controllerA.Close()
	controllerB, err := NewEtcdRegistry(cfg, func() time.Time { return nowB })
	if err != nil {
		t.Fatalf("create controller B etcd registry: %v", err)
	}
	defer controllerB.Close()

	ctx := context.Background()
	if err := controllerA.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, 5*time.Second); err != nil {
		t.Fatalf("register through controller A: %v", err)
	}
	instances, err := controllerB.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list through controller B: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" {
		t.Fatalf("controller B did not observe controller A registration: %+v", instances)
	}
	if err := controllerB.Heartbeat(ctx, "user-service", "user-a", 5*time.Second); err != nil {
		t.Fatalf("heartbeat through controller B: %v", err)
	}
	instances, err = controllerA.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list through controller A: %v", err)
	}
	if len(instances) != 1 || !instances[0].LastSeen.Equal(nowB) {
		t.Fatalf("controller A did not observe controller B heartbeat time: %+v", instances)
	}
}

// TestEtcdRegistryIntegrationWatchRemoteWriteAndLeaseExpiry locks the etcd registry integration watch remote write and lease expiry contract so future changes do not regress it.
func TestEtcdRegistryIntegrationWatchRemoteWriteAndLeaseExpiry(t *testing.T) {
	cfg := testEtcdRegistryConfig(t)
	controllerA, err := NewEtcdRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("create controller A etcd registry: %v", err)
	}
	defer controllerA.Close()
	controllerB, err := NewEtcdRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("create controller B etcd registry: %v", err)
	}
	defer controllerB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := controllerB.Watch(ctx, "user-service", 0)
	if err != nil {
		t.Fatalf("watch through controller B: %v", err)
	}
	if err := controllerA.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Second); err != nil {
		t.Fatalf("register through controller A: %v", err)
	}
	snapshot := readEtcdIntegrationSnapshot(t, updates, 3*time.Second)
	if len(snapshot.Instances) != 1 {
		t.Fatalf("expected watch registration snapshot, got %+v", snapshot.Instances)
	}
	snapshot = readEtcdIntegrationSnapshot(t, updates, 5*time.Second)
	if len(snapshot.Instances) != 0 {
		t.Fatalf("expected watch lease-expiry snapshot, got %+v", snapshot.Instances)
	}
}

// TestEtcdRegistryIntegrationHeartbeatExtendsRealLease locks the etcd registry integration heartbeat extends real lease contract so future changes do not regress it.
func TestEtcdRegistryIntegrationHeartbeatExtendsRealLease(t *testing.T) {
	cfg := testEtcdRegistryConfig(t)
	reg, err := NewEtcdRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("create etcd registry: %v", err)
	}
	defer reg.Close()

	ctx := context.Background()
	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, time.Second); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	if err := reg.Heartbeat(ctx, "user-service", "user-a", 2*time.Second); err != nil {
		t.Fatalf("heartbeat instance: %v", err)
	}
	time.Sleep(700 * time.Millisecond)
	instances, err := reg.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list before renewed lease expiry: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected renewed lease to keep instance alive, got %+v", instances)
	}
	time.Sleep(2 * time.Second)
	instances, err = reg.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list after renewed lease expiry: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected renewed lease to expire, got %+v", instances)
	}
}

// TestEtcdLeaseStoreIntegrationFencedUpdate locks the etcd lease store integration fenced update contract so future changes do not regress it.
func TestEtcdLeaseStoreIntegrationFencedUpdate(t *testing.T) {
	cfg := testEtcdRegistryConfig(t)
	reg, err := NewEtcdRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("create etcd registry: %v", err)
	}
	defer reg.Close()
	store := reg.store.(*etcdLeaseStore)
	key := reg.instanceKey("user-service", "user-a")
	ctx := context.Background()

	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "old:7001"}, 5*time.Second); err != nil {
		t.Fatalf("register original instance: %v", err)
	}
	_, oldRevision, ok, err := store.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("get original instance: ok=%v err=%v", ok, err)
	}
	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "new:7001"}, 5*time.Second); err != nil {
		t.Fatalf("register replacement instance: %v", err)
	}
	value, err := encodeLeaseStoreRecord(Instance{ID: "user-a", Service: "user-service", Address: "stale:7001", Status: InstanceHealthy, LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("encode stale instance: %v", err)
	}
	if _, updated, err := store.Update(ctx, key, value, 5*time.Second, oldRevision); err != nil || updated {
		t.Fatalf("expected stale update to be fenced, updated=%v err=%v", updated, err)
	}
	instances, err := reg.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list after stale update: %v", err)
	}
	if len(instances) != 1 || instances[0].Address != "new:7001" {
		t.Fatalf("stale update overwrote replacement: %+v", instances)
	}
}

// TestEtcdRegistryIntegrationServiceVersionIsolation locks the etcd registry integration service version isolation contract so future changes do not regress it.
func TestEtcdRegistryIntegrationServiceVersionIsolation(t *testing.T) {
	cfg := testEtcdRegistryConfig(t)
	reg, err := NewEtcdRegistry(cfg, time.Now)
	if err != nil {
		t.Fatalf("create etcd registry: %v", err)
	}
	defer reg.Close()

	ctx := context.Background()
	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "user-a:7001"}, 5*time.Second); err != nil {
		t.Fatalf("register user instance: %v", err)
	}
	before, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot before unrelated write: %v", err)
	}
	if err := reg.Register(ctx, Instance{ID: "order-a", Service: "order-service", Address: "order-a:7001"}, 5*time.Second); err != nil {
		t.Fatalf("register order instance: %v", err)
	}
	after, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot after unrelated write: %v", err)
	}
	if after.Version != before.Version {
		t.Fatalf("expected user-service version to stay service-scoped, before=%d after=%d", before.Version, after.Version)
	}
}

// testEtcdRegistryConfig locks the etcd registry config contract so future changes do not regress it.
func testEtcdRegistryConfig(t *testing.T) EtcdRegistryConfig {
	t.Helper()
	raw := os.Getenv("AEGIS_TEST_ETCD_ENDPOINTS")
	if raw == "" {
		t.Skip("set AEGIS_TEST_ETCD_ENDPOINTS to run live etcd registry integration tests")
	}
	return EtcdRegistryConfig{
		Endpoints:      splitIntegrationList(raw),
		Prefix:         "/aegismesh/test/" + strings.ToLower(t.Name()) + "-" + time.Now().Format("20060102150405.000000000"),
		DialTimeout:    3 * time.Second,
		RequestTimeout: 3 * time.Second,
		Username:       os.Getenv("AEGIS_TEST_ETCD_USERNAME"),
		Password:       os.Getenv("AEGIS_TEST_ETCD_PASSWORD"),
	}
}

// splitIntegrationList keeps split integration list rules consistent for registry persistence and watch paths.
func splitIntegrationList(raw string) []string {
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

// readEtcdIntegrationSnapshot reads read etcd integration snapshot data from the supplied input.
func readEtcdIntegrationSnapshot(t *testing.T, updates <-chan InstanceSnapshot, timeout time.Duration) InstanceSnapshot {
	t.Helper()
	select {
	case snapshot, ok := <-updates:
		if !ok {
			t.Fatalf("watch channel closed")
		}
		return snapshot
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for etcd watch update")
		return InstanceSnapshot{}
	}
}
