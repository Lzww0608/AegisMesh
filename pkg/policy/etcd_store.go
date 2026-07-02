package policy

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/security"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/proto"
)

const (
	defaultEtcdPolicyPrefix       = "/aegismesh/policy/v1"
	defaultEtcdPolicyRequestTime  = 3 * time.Second
	defaultEtcdPolicyDialTimeout  = 3 * time.Second
	defaultEtcdPolicyWatchBackoff = 500 * time.Millisecond
)

type EtcdStoreConfig struct {
	Endpoints      []string
	Prefix         string
	DialTimeout    time.Duration
	RequestTimeout time.Duration
	WatchBackoff   time.Duration
	Username       string
	Password       string
	TLS            security.TLSConfig
}

type EtcdStore struct {
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	store          policyKVStore
	prefix         string
	servicesPrefix string
	watchBackoff   time.Duration

	mu       sync.RWMutex
	revision int64
	policies map[string]*aegisv1.PolicySnapshot
}

type policyKV struct {
	Key         string
	Value       []byte
	ModRevision int64
}

type policyWatchEvent struct {
	Revision int64
	Puts     []policyKV
	Deletes  []string
	Err      error
}

type policyKVStore interface {
	List(ctx context.Context, prefix string) ([]policyKV, int64, error)
	Watch(ctx context.Context, prefix string, afterRevision int64) (<-chan policyWatchEvent, error)
	Close() error
}

func NewEtcdStore(ctx context.Context, cfg EtcdStoreConfig) (*EtcdStore, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("etcd policy store requires at least one endpoint")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultEtcdPolicyDialTimeout
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultEtcdPolicyRequestTime
	}
	tlsCfg, err := security.ClientTLSConfig(cfg.TLS)
	if err != nil {
		return nil, err
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   append([]string(nil), cfg.Endpoints...),
		DialTimeout: cfg.DialTimeout,
		Username:    cfg.Username,
		Password:    cfg.Password,
		TLS:         tlsCfg,
	})
	if err != nil {
		return nil, err
	}
	store, err := newEtcdStoreWithKV(ctx, &etcdPolicyKVStore{client: client, requestTimeout: cfg.RequestTimeout}, cfg)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return store, nil
}

func newEtcdStoreWithKV(ctx context.Context, kv policyKVStore, cfg EtcdStoreConfig) (*EtcdStore, error) {
	if kv == nil {
		return nil, fmt.Errorf("etcd policy store requires a kv store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	watchBackoff := cfg.WatchBackoff
	if watchBackoff <= 0 {
		watchBackoff = defaultEtcdPolicyWatchBackoff
	}
	storeCtx, cancel := context.WithCancel(ctx)
	store := &EtcdStore{
		ctx:          storeCtx,
		cancel:       cancel,
		store:        kv,
		prefix:       normalizeEtcdPolicyPrefix(cfg.Prefix),
		watchBackoff: watchBackoff,
		policies:     make(map[string]*aegisv1.PolicySnapshot),
	}
	store.servicesPrefix = policyServicePrefix(store.prefix)
	revision, err := store.reloadFull()
	if err != nil {
		cancel()
		return nil, err
	}
	store.startWatch(revision)
	return store, nil
}

func (s *EtcdStore) Get(service string) (*aegisv1.PolicySnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := s.policies[service]
	if snapshot == nil {
		return nil, false
	}
	return proto.Clone(snapshot).(*aegisv1.PolicySnapshot), true
}

func (s *EtcdStore) List() []*aegisv1.PolicySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*aegisv1.PolicySnapshot, 0, len(s.policies))
	for _, snapshot := range s.policies {
		if snapshot == nil {
			continue
		}
		out = append(out, proto.Clone(snapshot).(*aegisv1.PolicySnapshot))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func (s *EtcdStore) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *EtcdStore) startWatch(afterRevision int64) {
	s.wg.Add(1)
	go s.watchLoop(afterRevision)
}

func (s *EtcdStore) watchLoop(afterRevision int64) {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		updates, err := s.store.Watch(s.ctx, s.servicesPrefix, afterRevision)
		if err != nil {
			afterRevision = s.resyncAfterBackoff(afterRevision)
			continue
		}

		needResync := false
		for event := range updates {
			if s.ctx.Err() != nil {
				return
			}
			if event.Err != nil {
				needResync = true
				break
			}
			if event.Revision > 0 {
				afterRevision = event.Revision
			}
			if err := s.applyWatchEvent(event); err != nil {
				needResync = true
				break
			}
		}
		if s.ctx.Err() != nil {
			return
		}
		if needResync || updates != nil {
			afterRevision = s.resyncAfterBackoff(afterRevision)
		}
	}
}

func (s *EtcdStore) resyncAfterBackoff(currentRevision int64) int64 {
	if s.watchBackoff > 0 {
		timer := time.NewTimer(s.watchBackoff)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return currentRevision
		case <-timer.C:
		}
	}
	revision, err := s.reloadFull()
	if err != nil {
		return currentRevision
	}
	return revision
}

func (s *EtcdStore) reloadFull() (int64, error) {
	kvs, revision, err := s.store.List(s.ctx, s.servicesPrefix)
	if err != nil {
		return 0, err
	}
	policies, err := snapshotsFromEtcdKVs(s.servicesPrefix, kvs)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.policies = policies
	s.revision = revision
	s.mu.Unlock()
	return revision, nil
}

func (s *EtcdStore) applyWatchEvent(event policyWatchEvent) error {
	puts := make(map[string]*aegisv1.PolicySnapshot, len(event.Puts))
	for _, kv := range event.Puts {
		snapshot, ok, err := snapshotFromEtcdKV(s.servicesPrefix, kv)
		if err != nil {
			return err
		}
		if ok {
			puts[snapshot.Service] = snapshot
		}
	}
	deletes := make([]string, 0, len(event.Deletes))
	for _, key := range event.Deletes {
		service, ok := serviceFromPolicyKey(s.servicesPrefix, key)
		if ok {
			deletes = append(deletes, service)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Revision > 0 && event.Revision < s.revision {
		return nil
	}
	for service, snapshot := range puts {
		s.policies[service] = snapshot
	}
	for _, service := range deletes {
		delete(s.policies, service)
	}
	if event.Revision > s.revision {
		s.revision = event.Revision
	}
	return nil
}

type etcdPolicyKVStore struct {
	client         *clientv3.Client
	requestTimeout time.Duration
}

func (s *etcdPolicyKVStore) List(ctx context.Context, prefix string) ([]policyKV, int64, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, 0, err
	}
	kvs := make([]policyKV, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		kvs = append(kvs, policyKV{
			Key:         string(kv.Key),
			Value:       append([]byte(nil), kv.Value...),
			ModRevision: kv.ModRevision,
		})
	}
	return kvs, resp.Header.Revision, nil
}

func (s *etcdPolicyKVStore) Watch(ctx context.Context, prefix string, afterRevision int64) (<-chan policyWatchEvent, error) {
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if afterRevision > 0 {
		options = append(options, clientv3.WithRev(afterRevision+1))
	}
	source := s.client.Watch(ctx, prefix, options...)
	updates := make(chan policyWatchEvent, 1)
	go func() {
		defer close(updates)
		for watchResp := range source {
			if watchResp.Err() != nil || watchResp.Canceled {
				select {
				case updates <- policyWatchEvent{Revision: watchResp.Header.Revision, Err: watchResp.Err()}:
				case <-ctx.Done():
				}
				return
			}
			event := policyWatchEvent{Revision: watchResp.Header.Revision}
			for _, rawEvent := range watchResp.Events {
				if rawEvent == nil || rawEvent.Kv == nil {
					continue
				}
				switch rawEvent.Type {
				case clientv3.EventTypePut:
					event.Puts = append(event.Puts, policyKV{
						Key:         string(rawEvent.Kv.Key),
						Value:       append([]byte(nil), rawEvent.Kv.Value...),
						ModRevision: rawEvent.Kv.ModRevision,
					})
				case clientv3.EventTypeDelete:
					event.Deletes = append(event.Deletes, string(rawEvent.Kv.Key))
				}
			}
			select {
			case updates <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return updates, nil
}

func (s *etcdPolicyKVStore) Close() error {
	return s.client.Close()
}

func (s *etcdPolicyKVStore) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.requestTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.requestTimeout)
}

func snapshotsFromEtcdKVs(servicesPrefix string, kvs []policyKV) (map[string]*aegisv1.PolicySnapshot, error) {
	policies := make(map[string]*aegisv1.PolicySnapshot, len(kvs))
	for _, kv := range kvs {
		snapshot, ok, err := snapshotFromEtcdKV(servicesPrefix, kv)
		if err != nil {
			return nil, err
		}
		if ok {
			policies[snapshot.Service] = snapshot
		}
	}
	return policies, nil
}

func snapshotFromEtcdKV(servicesPrefix string, kv policyKV) (*aegisv1.PolicySnapshot, bool, error) {
	service, ok := serviceFromPolicyKey(servicesPrefix, kv.Key)
	if !ok {
		return nil, false, nil
	}
	var snapshot aegisv1.PolicySnapshot
	if err := proto.Unmarshal(kv.Value, &snapshot); err != nil {
		return nil, false, fmt.Errorf("decode policy %q: %w", service, err)
	}
	snapshot.Service = service
	snapshot.Revision = kv.ModRevision
	return &snapshot, true, nil
}

func normalizeEtcdPolicyPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = defaultEtcdPolicyPrefix
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(path.Clean(prefix), "/")
}

func policyServicePrefix(prefix string) string {
	return normalizeEtcdPolicyPrefix(prefix) + "/services/"
}

func PolicyServiceKey(prefix, service string) string {
	service = strings.TrimSpace(service)
	return policyServicePrefix(prefix) + url.PathEscape(service) + "/snapshot"
}

func serviceFromPolicyKey(servicesPrefix, key string) (string, bool) {
	if !strings.HasPrefix(key, servicesPrefix) || !strings.HasSuffix(key, "/snapshot") {
		return "", false
	}
	escaped := strings.TrimSuffix(strings.TrimPrefix(key, servicesPrefix), "/snapshot")
	if escaped == "" || strings.Contains(escaped, "/") {
		return "", false
	}
	service, err := url.PathUnescape(escaped)
	if err != nil || service == "" {
		return "", false
	}
	return service, true
}
