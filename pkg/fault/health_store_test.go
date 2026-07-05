package fault

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestHealthManagerMergeSnapshotRestoresEjectedStateMachine locks the health manager merge snapshot restores ejected state machine contract so future changes do not regress it.
func TestHealthManagerMergeSnapshotRestoresEjectedStateMachine(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{
		Now: func() time.Time { return now },
		StateMachine: StateMachineConfig{
			DegradedThreshold:  1,
			EjectThreshold:     2,
			ConsecutiveWindows: 1,
			EjectionDuration:   30 * time.Second,
			RecoveryThreshold:  0.5,
		},
	})

	merged := manager.MergeSnapshot([]EndpointHealth{{
		Service:          "user-service",
		InstanceID:       "user-a",
		Address:          "127.0.0.1:7001",
		State:            StateEjected,
		SlowScore:        3,
		EjectedAt:        now.Add(-31 * time.Second),
		LastTransitionAt: now.Add(-31 * time.Second),
		UpdatedAt:        now.Add(-time.Second),
	}})
	if merged != 1 {
		t.Fatalf("expected one merged service, got %d", merged)
	}
	if got := manager.HealthVersion("user-service"); got == 0 {
		t.Fatalf("expected merge to bump service health version")
	}

	manager.Tick()
	health, ok := manager.Get("user-service", "user-a")
	if !ok {
		t.Fatalf("expected restored health entry")
	}
	if health.State != StateProbing {
		t.Fatalf("expected restored ejected endpoint to continue to PROBING on tick, got %+v", health)
	}
}

// TestHealthManagerMergeSnapshotSkipsOlderEndpointHealth locks the health manager merge snapshot skips older endpoint health contract so future changes do not regress it.
func TestHealthManagerMergeSnapshotSkipsOlderEndpointHealth(t *testing.T) {
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	manager := NewHealthManager(HealthManagerConfig{Now: func() time.Time { return now }})
	manager.MergeSnapshot([]EndpointHealth{{
		Service:    "user-service",
		InstanceID: "user-a",
		Address:    "127.0.0.1:7001",
		State:      StateHealthy,
		UpdatedAt:  now,
	}})
	merged := manager.MergeSnapshot([]EndpointHealth{{
		Service:    "user-service",
		InstanceID: "user-a",
		Address:    "127.0.0.1:7001",
		State:      StateEjected,
		UpdatedAt:  now.Add(-time.Second),
	}})
	if merged != 0 {
		t.Fatalf("expected older snapshot to be skipped, got merged=%d", merged)
	}
	health, _ := manager.Get("user-service", "user-a")
	if health.State != StateHealthy {
		t.Fatalf("expected newer healthy state to win, got %+v", health)
	}
}

// TestEtcdHealthStoreRoundTripAndNewestWins locks the etcd health store round trip and newest wins contract so future changes do not regress it.
func TestEtcdHealthStoreRoundTripAndNewestWins(t *testing.T) {
	kv := newFakeHealthKVStore()
	store := newTestEtcdHealthStore(t, kv, EtcdHealthStoreConfig{Prefix: "/aegis/health"})
	defer store.Close()

	t1 := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	if _, err := store.Save(context.Background(), []EndpointHealth{{
		Service:                 "user-service",
		InstanceID:              "user-a",
		Address:                 "127.0.0.1:7001",
		State:                   StateDegraded,
		SlowScore:               1.75,
		ConsecutiveSlowWindows:  2,
		ConsecutiveEjectWindows: 1,
		LastTransitionAt:        t1.Add(-time.Second),
		UpdatedAt:               t1,
	}}); err != nil {
		t.Fatalf("save health: %v", err)
	}

	loaded, revision, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load health: %v", err)
	}
	if revision == 0 || len(loaded) != 1 {
		t.Fatalf("expected one loaded health row with revision, rev=%d rows=%d", revision, len(loaded))
	}
	if got := loaded[0]; got.Service != "user-service" || got.InstanceID != "user-a" || got.State != StateDegraded || got.SlowScore != 1.75 || got.ConsecutiveSlowWindows != 2 {
		t.Fatalf("unexpected loaded health: %+v", got)
	}

	if _, err := store.Save(context.Background(), []EndpointHealth{{
		Service:    "user-service",
		InstanceID: "user-a",
		State:      StateHealthy,
		UpdatedAt:  t1.Add(-time.Second),
	}}); err != nil {
		t.Fatalf("save older health: %v", err)
	}
	loaded, _, err = store.Load(context.Background())
	if err != nil {
		t.Fatalf("load after older health: %v", err)
	}
	if loaded[0].State != StateDegraded {
		t.Fatalf("expected older health write to be ignored, got %+v", loaded[0])
	}

	if _, err := store.Save(context.Background(), []EndpointHealth{{
		Service:    "user-service",
		InstanceID: "user-a",
		State:      StateEjected,
		UpdatedAt:  t1.Add(time.Second),
	}}); err != nil {
		t.Fatalf("save newer health: %v", err)
	}
	loaded, _, err = store.Load(context.Background())
	if err != nil {
		t.Fatalf("load after newer health: %v", err)
	}
	if loaded[0].State != StateEjected {
		t.Fatalf("expected newer health to win, got %+v", loaded[0])
	}
}

// newTestEtcdHealthStore initializes test etcd health store with package defaults for this package's call path.
func newTestEtcdHealthStore(t *testing.T, kv healthKVStore, cfg EtcdHealthStoreConfig) *EtcdHealthStore {
	t.Helper()
	store, err := newEtcdHealthStoreWithKV(kv, cfg)
	if err != nil {
		t.Fatalf("new health store: %v", err)
	}
	return store
}

// fakeHealthKVStore defines persistence operations for fake health kv store state.
type fakeHealthKVStore struct {
	mu        sync.Mutex
	kvs       map[string]healthKV
	revision  int64
	watchers  map[int]chan HealthStoreEvent
	nextWatch int
	closed    bool
}

// newFakeHealthKVStore initializes fake health kv store with package defaults for this package's call path.
func newFakeHealthKVStore() *fakeHealthKVStore {
	return &fakeHealthKVStore{kvs: make(map[string]healthKV), watchers: make(map[int]chan HealthStoreEvent)}
}

// List returns a point-in-time list of list visible to the caller.
func (s *fakeHealthKVStore) List(_ context.Context, prefix string) ([]healthKV, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]healthKV, 0, len(s.kvs))
	for _, kv := range s.kvs {
		if len(prefix) == 0 || len(kv.Key) >= len(prefix) && kv.Key[:len(prefix)] == prefix {
			out = append(out, cloneHealthKV(kv))
		}
	}
	return out, s.revision, nil
}

// Get returns get state for the requested key.
func (s *fakeHealthKVStore) Get(_ context.Context, key string) (healthKV, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kv, ok := s.kvs[key]
	if !ok {
		return healthKV{}, false, nil
	}
	return cloneHealthKV(kv), true, nil
}

// PutIfModRevision emulates etcd compare-and-swap writes for health-store concurrency tests.
func (s *fakeHealthKVStore) PutIfModRevision(_ context.Context, key string, value []byte, expectedModRevision int64) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.kvs[key]
	if ok && current.ModRevision != expectedModRevision {
		return s.revision, false, nil
	}
	if !ok && expectedModRevision != 0 {
		return s.revision, false, nil
	}
	s.revision++
	s.kvs[key] = healthKV{Key: key, Value: append([]byte(nil), value...), ModRevision: s.revision}
	for _, watcher := range s.watchers {
		select {
		case watcher <- HealthStoreEvent{Revision: s.revision}:
		default:
		}
	}
	return s.revision, true, nil
}

// Watch streams backing-source changes to callers until the source or context closes.
func (s *fakeHealthKVStore) Watch(ctx context.Context, _ string, _ int64) (<-chan HealthStoreEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextWatch
	s.nextWatch++
	updates := make(chan HealthStoreEvent, 16)
	s.watchers[id] = updates
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		ch, ok := s.watchers[id]
		if ok {
			delete(s.watchers, id)
			close(ch)
		}
		s.mu.Unlock()
	}()
	return updates, nil
}

// Close closes owned resources and makes repeated calls safe.
func (s *fakeHealthKVStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, ch := range s.watchers {
		delete(s.watchers, id)
		close(ch)
	}
	return nil
}

// cloneHealthKV returns an isolated copy of clone health kv input so callers cannot mutate shared state.
func cloneHealthKV(kv healthKV) healthKV {
	return healthKV{Key: kv.Key, Value: append([]byte(nil), kv.Value...), ModRevision: kv.ModRevision}
}
