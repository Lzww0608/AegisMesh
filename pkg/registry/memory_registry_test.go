package registry

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryRegistryRegistersAndListsLiveInstances(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })

	err := reg.Register(context.Background(), Instance{
		ID:      "user-1",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Status:  InstanceHealthy,
		Labels:  map[string]string{"variant": "primary"},
	}, 10*time.Second)
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	got, err := reg.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 live instance, got %d", len(got))
	}
	if got[0].ID != "user-1" || got[0].Address != "127.0.0.1:7001" {
		t.Fatalf("unexpected instance: %+v", got[0])
	}
	if got[0].Labels["variant"] != "primary" {
		t.Fatalf("expected labels to round-trip, got %+v", got[0].Labels)
	}
}

func TestMemoryRegistryExpiresInstancesAfterLease(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })

	err := reg.Register(context.Background(), Instance{
		ID:      "user-1",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Status:  InstanceHealthy,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	now = now.Add(6 * time.Second)
	expired := reg.SweepExpired(context.Background())
	if expired != 1 {
		t.Fatalf("expected 1 expired instance, got %d", expired)
	}

	got, err := reg.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no live instances after lease expiry, got %+v", got)
	}
}

func TestMemoryRegistryHeartbeatExtendsLease(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })

	err := reg.Register(context.Background(), Instance{
		ID:      "user-1",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Status:  InstanceHealthy,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("register instance: %v", err)
	}

	now = now.Add(4 * time.Second)
	if err := reg.Heartbeat(context.Background(), "user-service", "user-1", 5*time.Second); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	now = now.Add(3 * time.Second)
	expired := reg.SweepExpired(context.Background())
	if expired != 0 {
		t.Fatalf("expected heartbeat to keep instance alive, expired %d", expired)
	}

	got, err := reg.List(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected renewed instance to remain live, got %+v", got)
	}
}

func TestMemoryRegistrySnapshotVersionAndImmutableList(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })
	ctx := context.Background()

	if err := reg.Register(ctx, Instance{ID: "user-b", Service: "user-service", Address: "127.0.0.1:7002", Labels: map[string]string{"variant": "secondary"}}, time.Minute); err != nil {
		t.Fatalf("register user-b: %v", err)
	}
	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "127.0.0.1:7001", Labels: map[string]string{"variant": "primary"}}, time.Minute); err != nil {
		t.Fatalf("register user-a: %v", err)
	}

	snapshot, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Version == 0 {
		t.Fatalf("expected non-zero snapshot version")
	}
	if len(snapshot.Instances) != 2 || snapshot.Instances[0].ID != "user-a" || snapshot.Instances[1].ID != "user-b" {
		t.Fatalf("expected sorted snapshot by id, got %+v", snapshot.Instances)
	}

	listed, err := reg.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listed[0].Labels["variant"] = "mutated"

	listedAgain, err := reg.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list again: %v", err)
	}
	if listedAgain[0].Labels["variant"] != "primary" {
		t.Fatalf("list leaked mutable labels into registry snapshot: %+v", listedAgain[0].Labels)
	}

	snapshotAgain, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot again: %v", err)
	}
	if snapshotAgain.Version != snapshot.Version {
		t.Fatalf("read-only list/snapshot changed version: before %d after %d", snapshot.Version, snapshotAgain.Version)
	}

	now = now.Add(time.Second)
	if err := reg.Heartbeat(ctx, "user-service", "user-a", time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	afterHeartbeat, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot after heartbeat: %v", err)
	}
	if afterHeartbeat.Version <= snapshot.Version {
		t.Fatalf("heartbeat did not advance version: before %d after %d", snapshot.Version, afterHeartbeat.Version)
	}
}

func TestMemoryRegistrySnapshotDoesNotLeakMutableState(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })
	ctx := context.Background()

	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "127.0.0.1:7001", Labels: map[string]string{"variant": "primary"}}, time.Minute); err != nil {
		t.Fatalf("register: %v", err)
	}

	snapshot, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshot.Instances[0].ID = "mutated"
	snapshot.Instances[0].Labels["variant"] = "mutated"

	snapshotAgain, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot again: %v", err)
	}
	if snapshotAgain.Instances[0].ID != "user-a" || snapshotAgain.Instances[0].Labels["variant"] != "primary" {
		t.Fatalf("snapshot exposed mutable registry state: %+v", snapshotAgain.Instances[0])
	}
}

func TestMemoryRegistryWatchPublishesExpiryWithoutExternalSweep(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	var currentNanos atomic.Int64
	currentNanos.Store(base.UnixNano())
	reg := NewMemoryRegistry(func() time.Time { return time.Unix(0, currentNanos.Load()) })
	ctx := context.Background()

	if err := reg.Register(ctx, Instance{ID: "user-1", Service: "user-service", Address: "127.0.0.1:7001"}, 100*time.Millisecond); err != nil {
		t.Fatalf("register: %v", err)
	}
	beforeExpiry, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot before expiry: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates, err := reg.Watch(watchCtx, "user-service", beforeExpiry.Version)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	currentNanos.Store(base.Add(150 * time.Millisecond).UnixNano())

	expired := receiveSnapshot(t, updates)
	if len(expired.Instances) != 0 {
		t.Fatalf("expected watch to publish expired empty snapshot, got %+v", expired.Instances)
	}
	if expired.Version <= beforeExpiry.Version {
		t.Fatalf("expected expiry snapshot version to advance: before %d after %d", beforeExpiry.Version, expired.Version)
	}
}
func TestMemoryRegistryListSweepsExpiredSnapshotForService(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })
	ctx := context.Background()

	if err := reg.Register(ctx, Instance{ID: "user-1", Service: "user-service", Address: "127.0.0.1:7001"}, time.Second); err != nil {
		t.Fatalf("register: %v", err)
	}
	beforeExpiry, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot before expiry: %v", err)
	}

	now = now.Add(2 * time.Second)
	got, err := reg.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list after expiry: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected service-local snapshot sweep to hide expired instance, got %+v", got)
	}
	afterExpiry, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot after expiry: %v", err)
	}
	if afterExpiry.Version <= beforeExpiry.Version {
		t.Fatalf("expiry sweep did not advance version: before %d after %d", beforeExpiry.Version, afterExpiry.Version)
	}
}

func TestMemoryRegistrySweepExpiredBatchesSnapshotRebuildByService(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })
	ctx := context.Background()

	if err := reg.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "127.0.0.1:7001"}, time.Second); err != nil {
		t.Fatalf("register user-a: %v", err)
	}
	if err := reg.Register(ctx, Instance{ID: "user-b", Service: "user-service", Address: "127.0.0.1:7002"}, time.Second); err != nil {
		t.Fatalf("register user-b: %v", err)
	}
	beforeExpiry, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot before expiry: %v", err)
	}

	now = now.Add(2 * time.Second)
	if expired := reg.SweepExpired(ctx); expired != 2 {
		t.Fatalf("expected two expired instances, got %d", expired)
	}
	afterExpiry, err := reg.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("snapshot after expiry: %v", err)
	}
	if len(afterExpiry.Instances) != 0 {
		t.Fatalf("expected empty snapshot after batch expiry, got %+v", afterExpiry.Instances)
	}
	if afterExpiry.Version != beforeExpiry.Version+1 {
		t.Fatalf("expected one snapshot rebuild for batch expiry: before %d after %d", beforeExpiry.Version, afterExpiry.Version)
	}
}
func TestMemoryRegistryWatchCoalescesUpdatesForSlowConsumer(t *testing.T) {
	var (
		clockMu sync.Mutex
		now     = time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	)
	getNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(d)
	}
	reg := NewMemoryRegistry(getNow)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := reg.Register(context.Background(), Instance{ID: "user-1", Service: "user-service", Address: "127.0.0.1:7001"}, time.Minute); err != nil {
		t.Fatalf("register: %v", err)
	}
	updates, err := reg.Watch(ctx, "user-service", 0)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	first := receiveSnapshot(t, updates)
	if first.Version == 0 || len(first.Instances) != 1 {
		t.Fatalf("unexpected initial watch snapshot: %+v", first)
	}

	for i := 0; i < 8; i++ {
		advance(time.Millisecond)
		if err := reg.Heartbeat(context.Background(), "user-service", "user-1", time.Minute); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}

	latest := receiveSnapshot(t, updates)
	if latest.Version <= first.Version {
		t.Fatalf("expected coalesced watch to deliver latest version: first %d latest %d", first.Version, latest.Version)
	}
	select {
	case extra := <-updates:
		t.Fatalf("expected only one coalesced update, got extra %+v", extra)
	default:
	}
}

func TestMemoryRegistryWatchPublishesChangedSnapshots(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	reg := NewMemoryRegistry(func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates, err := reg.Watch(ctx, "user-service", 0)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}

	if err := reg.Register(context.Background(), Instance{ID: "user-1", Service: "user-service", Address: "127.0.0.1:7001"}, time.Minute); err != nil {
		t.Fatalf("register: %v", err)
	}
	first := receiveSnapshot(t, updates)
	if first.Version == 0 || len(first.Instances) != 1 || first.Instances[0].ID != "user-1" {
		t.Fatalf("unexpected first watch snapshot: %+v", first)
	}

	if err := reg.Heartbeat(context.Background(), "user-service", "user-1", time.Minute); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	second := receiveSnapshot(t, updates)
	if second.Version <= first.Version {
		t.Fatalf("expected heartbeat watch version to advance: first %d second %d", first.Version, second.Version)
	}
}

func receiveSnapshot(t *testing.T, updates <-chan InstanceSnapshot) InstanceSnapshot {
	t.Helper()
	select {
	case snapshot := <-updates:
		return snapshot
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for registry snapshot")
		return InstanceSnapshot{}
	}
}
