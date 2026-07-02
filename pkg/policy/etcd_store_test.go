package policy

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"
)

func TestEtcdStoreLoadsPolicySnapshotsFromPrefix(t *testing.T) {
	kv := newFakePolicyKVStore()
	kv.putSnapshotLocked(PolicyServiceKey("/aegis/test", "z-service"), &aegisv1.PolicySnapshot{Retry: &aegisv1.RetryPolicy{MaxAttempts: 3}})
	kv.putSnapshotLocked(PolicyServiceKey("/aegis/test", "a-service"), &aegisv1.PolicySnapshot{
		Service:       "ignored-from-value",
		Revision:      99,
		RoutingPolicy: "adaptive_p2c",
		OutlierDetection: &aegisv1.OutlierDetectionPolicy{
			EjectThreshold: 2.5,
		},
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
			MaxInflightPerEndpoint: 7,
		},
		Methods: map[string]*aegisv1.MethodPolicy{
			"/demo.A/Get": {Method: "/demo.A/Get", Idempotent: true, TimeoutMillis: 15},
		},
	})
	store := newTestEtcdPolicyStore(t, kv, EtcdStoreConfig{Prefix: "/aegis/test", WatchBackoff: time.Millisecond})
	defer store.Close()

	listed := store.List()
	if len(listed) != 2 {
		t.Fatalf("expected two listed policies, got %d", len(listed))
	}
	if listed[0].Service != "a-service" || listed[1].Service != "z-service" {
		t.Fatalf("expected sorted policies, got %q then %q", listed[0].Service, listed[1].Service)
	}
	if listed[0].Revision == 99 || listed[0].RoutingPolicy != "adaptive_p2c" {
		t.Fatalf("expected service/revision to come from etcd key/modrev, got %+v", listed[0])
	}
	if listed[0].OutlierDetection.GetEjectThreshold() != 2.5 || listed[0].CircuitBreaker.GetMaxInflightPerEndpoint() != 7 {
		t.Fatalf("expected outlier/circuit policy, got %+v", listed[0])
	}
	method := listed[0].Methods["/demo.A/Get"]
	if method == nil || !method.Idempotent || method.TimeoutMillis != 15 {
		t.Fatalf("unexpected method policy: %+v", method)
	}

	listed[0].Service = "mutated"
	listed[0].CircuitBreaker.MaxInflightPerEndpoint = 99
	snapshot, ok := store.Get("a-service")
	if !ok {
		t.Fatalf("expected a-service policy")
	}
	if snapshot.Service != "a-service" || snapshot.CircuitBreaker.GetMaxInflightPerEndpoint() != 7 {
		t.Fatalf("expected Get to return immutable clone, got %+v", snapshot)
	}
}

func TestEtcdStoreWatchesSharedUpdatesAndDeletes(t *testing.T) {
	kv := newFakePolicyKVStore()
	storeA := newTestEtcdPolicyStore(t, kv, EtcdStoreConfig{Prefix: "/aegis/test", WatchBackoff: time.Millisecond})
	defer storeA.Close()
	storeB := newTestEtcdPolicyStore(t, kv, EtcdStoreConfig{Prefix: "/aegis/test", WatchBackoff: time.Millisecond})
	defer storeB.Close()
	waitForBackendCounts(t, kv, 2, 2)

	kv.PutSnapshot(PolicyServiceKey("/aegis/test", "user-service"), &aegisv1.PolicySnapshot{
		RoutingPolicy: "adaptive_p2c",
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
			MaxInflightPerEndpoint: 9,
		},
	})
	waitForPolicySnapshot(t, storeB, "user-service", func(snapshot *aegisv1.PolicySnapshot) bool {
		return snapshot.RoutingPolicy == "adaptive_p2c" && snapshot.CircuitBreaker.GetMaxInflightPerEndpoint() == 9
	})

	kv.PutSnapshot(PolicyServiceKey("/aegis/test", "user-service"), &aegisv1.PolicySnapshot{
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{MaxInflightPerEndpoint: 11},
	})
	waitForPolicySnapshot(t, storeA, "user-service", func(snapshot *aegisv1.PolicySnapshot) bool {
		return snapshot.CircuitBreaker.GetMaxInflightPerEndpoint() == 11
	})

	kv.Delete(PolicyServiceKey("/aegis/test", "user-service"))
	waitForPolicyMissing(t, storeB, "user-service")
}

func TestEtcdStoreUsesOneWatchAndCacheReadsDoNotHitBackend(t *testing.T) {
	kv := newFakePolicyKVStore()
	kv.PutSnapshot(PolicyServiceKey("/aegis/test", "user-service"), &aegisv1.PolicySnapshot{Retry: &aegisv1.RetryPolicy{MaxAttempts: 2}})
	store := newTestEtcdPolicyStore(t, kv, EtcdStoreConfig{Prefix: "/aegis/test", WatchBackoff: time.Millisecond})
	defer store.Close()
	waitForBackendCounts(t, kv, 1, 1)

	listCalls, watchCalls := kv.Counts()
	if listCalls != 1 || watchCalls != 1 {
		t.Fatalf("expected startup to perform one list and one watch, got list=%d watch=%d", listCalls, watchCalls)
	}
	for i := 0; i < 10; i++ {
		if _, ok := store.Get("user-service"); !ok {
			t.Fatalf("expected user-service policy")
		}
		_ = store.List()
	}
	listCalls, watchCalls = kv.Counts()
	if listCalls != 1 || watchCalls != 1 {
		t.Fatalf("expected cache reads not to hit backend, got list=%d watch=%d", listCalls, watchCalls)
	}
	if _, ok := any(store).(interface{ ReloadIfChanged() error }); ok {
		t.Fatalf("etcd policy store must not implement ReloadIfChanged; PolicyService calls it per stream")
	}
}

func TestEtcdStoreResyncsAfterWatchClose(t *testing.T) {
	kv := newFakePolicyKVStore()
	store := newTestEtcdPolicyStore(t, kv, EtcdStoreConfig{Prefix: "/aegis/test", WatchBackoff: time.Millisecond})
	defer store.Close()
	waitForBackendCounts(t, kv, 1, 1)

	kv.PutSnapshot(PolicyServiceKey("/aegis/test", "user-service"), &aegisv1.PolicySnapshot{Retry: &aegisv1.RetryPolicy{MaxAttempts: 2}})
	waitForPolicySnapshot(t, store, "user-service", func(snapshot *aegisv1.PolicySnapshot) bool {
		return snapshot.Retry.GetMaxAttempts() == 2
	})
	kv.CloseWatchers()
	waitForBackendCounts(t, kv, 2, 2)

	kv.PutSnapshot(PolicyServiceKey("/aegis/test", "order-service"), &aegisv1.PolicySnapshot{Retry: &aegisv1.RetryPolicy{MaxAttempts: 4}})
	waitForPolicySnapshot(t, store, "order-service", func(snapshot *aegisv1.PolicySnapshot) bool {
		return snapshot.Retry.GetMaxAttempts() == 4
	})
}

func TestEtcdPolicyIntegrationSharedStateAcrossStores(t *testing.T) {
	cfg := testEtcdPolicyConfig(t)
	client := testEtcdPolicyClient(t, cfg)
	defer client.Close()

	ctx := context.Background()
	initial := marshalPolicySnapshot(t, &aegisv1.PolicySnapshot{
		RoutingPolicy: "adaptive_p2c",
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
			MaxInflightPerEndpoint: 9,
		},
	})
	if _, err := client.Put(ctx, PolicyServiceKey(cfg.Prefix, "user-service"), string(initial)); err != nil {
		t.Fatalf("put policy: %v", err)
	}

	storeA, err := NewEtcdStore(ctx, cfg)
	if err != nil {
		t.Fatalf("create policy store A: %v", err)
	}
	defer storeA.Close()
	storeB, err := NewEtcdStore(ctx, cfg)
	if err != nil {
		t.Fatalf("create policy store B: %v", err)
	}
	defer storeB.Close()

	if snapshot, ok := storeA.Get("user-service"); !ok || snapshot.RoutingPolicy != "adaptive_p2c" {
		t.Fatalf("store A did not load policy: ok=%v snapshot=%+v", ok, snapshot)
	}
	if snapshot, ok := storeB.Get("user-service"); !ok || snapshot.CircuitBreaker.GetMaxInflightPerEndpoint() != 9 {
		t.Fatalf("store B did not load shared policy: ok=%v snapshot=%+v", ok, snapshot)
	}

	updated := marshalPolicySnapshot(t, &aegisv1.PolicySnapshot{CircuitBreaker: &aegisv1.CircuitBreakerPolicy{MaxInflightPerEndpoint: 11}})
	if _, err := client.Put(ctx, PolicyServiceKey(cfg.Prefix, "user-service"), string(updated)); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	waitForPolicySnapshot(t, storeA, "user-service", func(snapshot *aegisv1.PolicySnapshot) bool {
		return snapshot.CircuitBreaker.GetMaxInflightPerEndpoint() == 11
	})

	if _, err := client.Delete(ctx, PolicyServiceKey(cfg.Prefix, "user-service")); err != nil {
		t.Fatalf("delete policy: %v", err)
	}
	waitForPolicyMissing(t, storeB, "user-service")
}

func newTestEtcdPolicyStore(t *testing.T, kv policyKVStore, cfg EtcdStoreConfig) *EtcdStore {
	t.Helper()
	store, err := newEtcdStoreWithKV(context.Background(), kv, cfg)
	if err != nil {
		t.Fatalf("new etcd policy store: %v", err)
	}
	return store
}

type fakePolicyKVStore struct {
	mu         sync.Mutex
	kvs        map[string]policyKV
	revision   int64
	watchers   map[int]chan policyWatchEvent
	nextWatch  int
	listCalls  int
	watchCalls int
	closed     bool
}

func newFakePolicyKVStore() *fakePolicyKVStore {
	return &fakePolicyKVStore{kvs: make(map[string]policyKV), watchers: make(map[int]chan policyWatchEvent)}
}

func (s *fakePolicyKVStore) List(context.Context, string) ([]policyKV, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	out := make([]policyKV, 0, len(s.kvs))
	for _, kv := range s.kvs {
		out = append(out, clonePolicyKV(kv))
	}
	return out, s.revision, nil
}

func (s *fakePolicyKVStore) Watch(ctx context.Context, _ string, _ int64) (<-chan policyWatchEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchCalls++
	id := s.nextWatch
	s.nextWatch++
	updates := make(chan policyWatchEvent, 16)
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

func (s *fakePolicyKVStore) Close() error {
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

func (s *fakePolicyKVStore) PutSnapshot(key string, snapshot *aegisv1.PolicySnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putSnapshotLocked(key, snapshot)
}

func (s *fakePolicyKVStore) putSnapshotLocked(key string, snapshot *aegisv1.PolicySnapshot) {
	s.revision++
	kv := policyKV{Key: key, Value: marshalPolicySnapshotForFake(snapshot), ModRevision: s.revision}
	s.kvs[key] = kv
	s.broadcastLocked(policyWatchEvent{Revision: s.revision, Puts: []policyKV{clonePolicyKV(kv)}})
}

func (s *fakePolicyKVStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	delete(s.kvs, key)
	s.broadcastLocked(policyWatchEvent{Revision: s.revision, Deletes: []string{key}})
}

func (s *fakePolicyKVStore) CloseWatchers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.watchers {
		delete(s.watchers, id)
		close(ch)
	}
}

func (s *fakePolicyKVStore) Counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls, s.watchCalls
}

func (s *fakePolicyKVStore) broadcastLocked(event policyWatchEvent) {
	for _, ch := range s.watchers {
		select {
		case ch <- event:
		default:
		}
	}
}

func clonePolicyKV(kv policyKV) policyKV {
	return policyKV{Key: kv.Key, Value: append([]byte(nil), kv.Value...), ModRevision: kv.ModRevision}
}

func marshalPolicySnapshotForFake(snapshot *aegisv1.PolicySnapshot) []byte {
	out, err := proto.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	return out
}

func waitForPolicySnapshot(t *testing.T, store *EtcdStore, service string, ok func(*aegisv1.PolicySnapshot) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, found := store.Get(service)
		if found && ok(snapshot) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for policy %q, found=%v snapshot=%+v", service, found, snapshot)
		case <-ticker.C:
		}
	}
}

func waitForPolicyMissing(t *testing.T, store *EtcdStore, service string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, ok := store.Get(service); !ok {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for policy %q to be removed", service)
		case <-ticker.C:
		}
	}
}

func waitForBackendCounts(t *testing.T, kv *fakePolicyKVStore, wantList, wantWatch int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		listCalls, watchCalls := kv.Counts()
		if listCalls >= wantList && watchCalls >= wantWatch {
			return
		}
		select {
		case <-deadline:
			listCalls, watchCalls := kv.Counts()
			t.Fatalf("timed out waiting for backend counts list>=%d watch>=%d, got list=%d watch=%d", wantList, wantWatch, listCalls, watchCalls)
		case <-ticker.C:
		}
	}
}

func marshalPolicySnapshot(t *testing.T, snapshot *aegisv1.PolicySnapshot) []byte {
	t.Helper()
	out, err := proto.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal policy snapshot: %v", err)
	}
	return out
}

func testEtcdPolicyConfig(t *testing.T) EtcdStoreConfig {
	t.Helper()
	raw := os.Getenv("AEGIS_TEST_ETCD_ENDPOINTS")
	if raw == "" {
		t.Skip("set AEGIS_TEST_ETCD_ENDPOINTS to run live etcd policy integration tests")
	}
	return EtcdStoreConfig{
		Endpoints:      splitPolicyIntegrationList(raw),
		Prefix:         "/aegismesh/policy-test/" + strings.ToLower(t.Name()) + "-" + time.Now().Format("20060102150405.000000000"),
		DialTimeout:    3 * time.Second,
		RequestTimeout: 3 * time.Second,
		WatchBackoff:   10 * time.Millisecond,
		Username:       os.Getenv("AEGIS_TEST_ETCD_USERNAME"),
		Password:       os.Getenv("AEGIS_TEST_ETCD_PASSWORD"),
	}
}

func testEtcdPolicyClient(t *testing.T, cfg EtcdStoreConfig) *clientv3.Client {
	t.Helper()
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   append([]string(nil), cfg.Endpoints...),
		DialTimeout: cfg.DialTimeout,
		Username:    cfg.Username,
		Password:    cfg.Password,
	})
	if err != nil {
		t.Fatalf("create etcd client: %v", err)
	}
	return client
}

func splitPolicyIntegrationList(raw string) []string {
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
