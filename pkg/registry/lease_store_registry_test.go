package registry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLeaseStoreRegistrySharesStateAcrossControllers(t *testing.T) {
	now := time.Unix(100, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	controllerA, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller A registry: %v", err)
	}
	controllerB, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller B registry: %v", err)
	}

	ctx := context.Background()
	if err := controllerA.Register(ctx, Instance{
		ID:      "user-a",
		Service: "user-service",
		Address: "127.0.0.1:7001",
		Labels:  map[string]string{"version": "v1"},
	}, time.Minute); err != nil {
		t.Fatalf("register through controller A: %v", err)
	}

	instances, err := controllerB.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list through controller B: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user-a" || instances[0].Labels["version"] != "v1" {
		t.Fatalf("controller B did not observe shared registration: %+v", instances)
	}

	now = now.Add(10 * time.Second)
	if err := controllerB.Heartbeat(ctx, "user-service", "user-a", time.Minute); err != nil {
		t.Fatalf("heartbeat through controller B: %v", err)
	}
	instances, err = controllerA.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list through controller A: %v", err)
	}
	if got := instances[0].LastSeen; !got.Equal(now) {
		t.Fatalf("expected heartbeat LastSeen %s, got %s", now, got)
	}
}

func TestLeaseStoreRegistryWatchReceivesRemoteWriteAndExpiry(t *testing.T) {
	now := time.Unix(200, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	controllerA, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller A registry: %v", err)
	}
	controllerB, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller B registry: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates, err := controllerB.Watch(ctx, "user-service", 0)
	if err != nil {
		t.Fatalf("watch through controller B: %v", err)
	}

	if err := controllerA.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "127.0.0.1:7001"}, time.Second); err != nil {
		t.Fatalf("register through controller A: %v", err)
	}
	snapshot := readLeaseStoreSnapshot(t, updates)
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].ID != "user-a" {
		t.Fatalf("expected remote registration update, got %+v", snapshot.Instances)
	}

	now = now.Add(2 * time.Second)
	if expired := controllerA.SweepExpired(context.Background()); expired != 1 {
		t.Fatalf("expected one expired record, got %d", expired)
	}
	snapshot = readLeaseStoreSnapshot(t, updates)
	if len(snapshot.Instances) != 0 {
		t.Fatalf("expected expiry update to remove instance, got %+v", snapshot.Instances)
	}
}

func TestLeaseStoreRegistryWatchReceivesPartialExpiryWithRemainingInstance(t *testing.T) {
	now := time.Unix(225, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	controllerA, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller A registry: %v", err)
	}
	controllerB, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller B registry: %v", err)
	}

	ctx := context.Background()
	if err := controllerA.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "127.0.0.1:7001"}, time.Second); err != nil {
		t.Fatalf("register expiring instance: %v", err)
	}
	if err := controllerA.Register(ctx, Instance{ID: "user-b", Service: "user-service", Address: "127.0.0.1:7002"}, time.Minute); err != nil {
		t.Fatalf("register remaining instance: %v", err)
	}
	initial, err := controllerB.Snapshot(ctx, "user-service")
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if len(initial.Instances) != 2 {
		t.Fatalf("expected two initial instances, got %+v", initial.Instances)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates, err := controllerB.Watch(watchCtx, "user-service", initial.Version)
	if err != nil {
		t.Fatalf("watch through controller B: %v", err)
	}
	now = now.Add(2 * time.Second)
	if expired := controllerA.SweepExpired(ctx); expired != 1 {
		t.Fatalf("expected one expired record, got %d", expired)
	}
	snapshot := readLeaseStoreSnapshot(t, updates)
	if snapshot.Version <= initial.Version {
		t.Fatalf("expected partial expiry version to advance: initial=%d snapshot=%d", initial.Version, snapshot.Version)
	}
	if len(snapshot.Instances) != 1 || snapshot.Instances[0].ID != "user-b" {
		t.Fatalf("expected watch to remove only expired instance, got %+v", snapshot.Instances)
	}
}
func TestLeaseStoreRegistryHeartbeatMissingInstance(t *testing.T) {
	store := newFakeLeaseStore(time.Now)
	reg, err := newLeaseStoreRegistry(store, "/aegis/test", time.Now)
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	if err := reg.Heartbeat(context.Background(), "user-service", "missing", time.Second); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("expected ErrInstanceNotFound, got %v", err)
	}
}

func TestLeaseStoreRegistryHeartbeatDoesNotOverwriteConflictingRegister(t *testing.T) {
	now := time.Unix(250, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	controllerA, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller A registry: %v", err)
	}
	controllerB, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller B registry: %v", err)
	}

	ctx := context.Background()
	if err := controllerA.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "old:7001"}, time.Second); err != nil {
		t.Fatalf("register original instance: %v", err)
	}
	store.afterGet = func(string) {
		if err := controllerB.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "new:7001"}, time.Minute); err != nil {
			t.Errorf("conflicting re-register: %v", err)
		}
	}
	if err := controllerA.Heartbeat(ctx, "user-service", "user-a", time.Minute); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("expected fenced heartbeat to return ErrInstanceNotFound, got %v", err)
	}

	instances, err := controllerB.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list after conflicting heartbeat: %v", err)
	}
	if len(instances) != 1 || instances[0].Address != "new:7001" {
		t.Fatalf("stale heartbeat overwrote new registration: %+v", instances)
	}
}

func TestLeaseStoreRegistrySnapshotVersionIsServiceScopedForExistingService(t *testing.T) {
	now := time.Unix(260, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	reg, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	if err := reg.Register(context.Background(), Instance{ID: "user-a", Service: "user-service", Address: "user:7001"}, time.Minute); err != nil {
		t.Fatalf("register user service: %v", err)
	}
	before, err := reg.Snapshot(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("snapshot user service before unrelated write: %v", err)
	}
	if err := reg.Register(context.Background(), Instance{ID: "order-a", Service: "order-service", Address: "order:7001"}, time.Minute); err != nil {
		t.Fatalf("register unrelated service: %v", err)
	}
	after, err := reg.Snapshot(context.Background(), "user-service")
	if err != nil {
		t.Fatalf("snapshot user service after unrelated write: %v", err)
	}
	if after.Version != before.Version {
		t.Fatalf("expected unrelated service write not to change user-service version: before=%d after=%d", before.Version, after.Version)
	}
}
func TestLeaseStoreRegistryEscapesServiceAndInstancePathSegments(t *testing.T) {
	now := time.Unix(300, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	reg, err := newLeaseStoreRegistry(store, "/aegis/test/", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	if err := reg.Register(context.Background(), Instance{ID: "user/a", Service: "user/service", Address: "127.0.0.1:7001"}, time.Minute); err != nil {
		t.Fatalf("register escaped instance: %v", err)
	}
	instances, err := reg.List(context.Background(), "user/service")
	if err != nil {
		t.Fatalf("list escaped service: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "user/a" {
		t.Fatalf("expected escaped instance to round-trip, got %+v", instances)
	}
	for key := range store.entries {
		if strings.Contains(strings.TrimPrefix(key, "/aegis/test/"), "/a/") {
			t.Fatalf("key was not path-escaped: %s", key)
		}
	}
}

func TestLeaseTTLSecondsRoundsUp(t *testing.T) {
	tests := map[time.Duration]int64{
		0:                       1,
		time.Millisecond:        1,
		time.Second:             1,
		1500 * time.Millisecond: 2,
	}
	for ttl, want := range tests {
		if got := leaseTTLSeconds(ttl); got != want {
			t.Fatalf("leaseTTLSeconds(%s) = %d, want %d", ttl, got, want)
		}
	}
}

func readLeaseStoreSnapshot(t *testing.T, updates <-chan InstanceSnapshot) InstanceSnapshot {
	t.Helper()
	select {
	case snapshot, ok := <-updates:
		if !ok {
			t.Fatalf("updates channel closed")
		}
		return snapshot
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for registry update")
		return InstanceSnapshot{}
	}
}

type fakeLeaseStore struct {
	mu       sync.Mutex
	now      func() time.Time
	revision int64
	entries  map[string]fakeLeaseEntry
	watchers map[int]fakeLeaseWatcher
	nextID   int
	closed   bool
	afterGet func(key string)
}

type fakeLeaseEntry struct {
	value       []byte
	expiresAt   time.Time
	modRevision int64
}

type fakeLeaseWatcher struct {
	prefix       string
	afterVersion int64
	ch           chan leaseStoreRevision
}

func newFakeLeaseStore(now func() time.Time) *fakeLeaseStore {
	if now == nil {
		now = time.Now
	}
	return &fakeLeaseStore{
		now:      now,
		entries:  make(map[string]fakeLeaseEntry),
		watchers: make(map[int]fakeLeaseWatcher),
	}
}

func (s *fakeLeaseStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, ErrInvalidInstance
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, context.Canceled
	}
	s.revision++
	s.entries[key] = fakeLeaseEntry{value: append([]byte(nil), value...), expiresAt: s.now().Add(ttl), modRevision: s.revision}
	s.notifyLocked(key)
	return s.revision, nil
}

func (s *fakeLeaseStore) Update(ctx context.Context, key string, value []byte, ttl time.Duration, expectedModRevision int64) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if ttl <= 0 {
		return 0, false, ErrInvalidInstance
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, false, context.Canceled
	}
	entry, ok := s.entries[key]
	if !ok || entry.modRevision != expectedModRevision {
		return s.revision, false, nil
	}
	s.revision++
	s.entries[key] = fakeLeaseEntry{value: append([]byte(nil), value...), expiresAt: s.now().Add(ttl), modRevision: s.revision}
	s.notifyLocked(key)
	return s.revision, true, nil
}

func (s *fakeLeaseStore) Get(ctx context.Context, key string) ([]byte, int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	s.mu.Lock()
	entry, ok := s.entries[key]
	if !ok {
		revision := s.revision
		s.mu.Unlock()
		return nil, revision, false, nil
	}
	afterGet := s.afterGet
	s.afterGet = nil
	value := append([]byte(nil), entry.value...)
	modRevision := entry.modRevision
	s.mu.Unlock()
	if afterGet != nil {
		afterGet(key)
	}
	return value, modRevision, true, nil
}

func (s *fakeLeaseStore) List(ctx context.Context, prefix string) ([]leaseStoreKV, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]leaseStoreKV, 0)
	for key, entry := range s.entries {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, leaseStoreKV{Key: key, Value: append([]byte(nil), entry.value...), ModRevision: entry.modRevision})
	}
	return out, s.revision, nil
}

func (s *fakeLeaseStore) Watch(ctx context.Context, prefix string, afterVersion int64) (<-chan leaseStoreRevision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	id := s.nextID
	s.nextID++
	ch := make(chan leaseStoreRevision, 1)
	s.watchers[id] = fakeLeaseWatcher{prefix: prefix, afterVersion: afterVersion, ch: ch}
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if watcher, ok := s.watchers[id]; ok {
			delete(s.watchers, id)
			close(watcher.ch)
		}
		s.mu.Unlock()
	}()
	return ch, nil
}

func (s *fakeLeaseStore) SweepExpired(ctx context.Context) int {
	if err := ctx.Err(); err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := 0
	now := s.now()
	for key, entry := range s.entries {
		if entry.expiresAt.After(now) {
			continue
		}
		delete(s.entries, key)
		s.revision++
		expired++
		s.notifyLocked(key)
	}
	return expired
}

func (s *fakeLeaseStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, watcher := range s.watchers {
		delete(s.watchers, id)
		close(watcher.ch)
	}
	return nil
}

func (s *fakeLeaseStore) notifyLocked(key string) {
	for id, watcher := range s.watchers {
		if s.revision <= watcher.afterVersion || !strings.HasPrefix(key, watcher.prefix) {
			continue
		}
		watcher.afterVersion = s.revision
		s.watchers[id] = watcher
		select {
		case watcher.ch <- leaseStoreRevision{Revision: s.revision}:
		default:
			select {
			case <-watcher.ch:
			default:
			}
			select {
			case watcher.ch <- leaseStoreRevision{Revision: s.revision}:
			default:
			}
		}
	}
}

func TestLeaseStoreRegistryOwnerHeartbeatRejectsStaleReplacement(t *testing.T) {
	now := time.Unix(251, 0)
	store := newFakeLeaseStore(func() time.Time { return now })
	controllerA, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller A registry: %v", err)
	}
	controllerB, err := newLeaseStoreRegistry(store, "/aegis/test", func() time.Time { return now })
	if err != nil {
		t.Fatalf("create controller B registry: %v", err)
	}

	ctx := context.Background()
	if err := controllerA.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "old:7001"}, time.Minute); err != nil {
		t.Fatalf("register original instance: %v", err)
	}
	instances, err := controllerA.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list original: %v", err)
	}
	original := instances[0]
	if original.RegistrationEpoch == "" || original.OwnerToken == "" {
		t.Fatalf("expected owner credentials, got %+v", original)
	}
	if err := controllerB.Register(ctx, Instance{ID: "user-a", Service: "user-service", Address: "new:7001"}, time.Minute); err != nil {
		t.Fatalf("register replacement: %v", err)
	}
	if err := controllerA.HeartbeatWithOwner(ctx, "user-service", "user-a", original.RegistrationEpoch, original.OwnerToken, time.Minute); !errors.Is(err, ErrRegistrationEpochMismatch) {
		t.Fatalf("expected stale owner mismatch, got %v", err)
	}
	instances, err = controllerB.List(ctx, "user-service")
	if err != nil {
		t.Fatalf("list replacement: %v", err)
	}
	if len(instances) != 1 || instances[0].Address != "new:7001" {
		t.Fatalf("stale owner heartbeat changed replacement: %+v", instances)
	}
}
