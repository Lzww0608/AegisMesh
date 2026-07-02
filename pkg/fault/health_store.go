package fault

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aegismesh/aegismesh/pkg/security"
	"github.com/aegismesh/aegismesh/pkg/status"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	defaultEtcdHealthPrefix       = "/aegismesh/health/v1"
	defaultEtcdHealthDialTimeout  = 3 * time.Second
	defaultEtcdHealthRequestTime  = 3 * time.Second
	defaultEtcdHealthWatchBackoff = 500 * time.Millisecond
)

type HealthSnapshotStore interface {
	Load(ctx context.Context) ([]EndpointHealth, int64, error)
	Save(ctx context.Context, health []EndpointHealth) (int64, error)
	Watch(ctx context.Context, afterRevision int64) (<-chan HealthStoreEvent, error)
	Close() error
}

type HealthStoreEvent struct {
	Revision int64
	Err      error
}

type EtcdHealthStoreConfig struct {
	Endpoints      []string
	Prefix         string
	DialTimeout    time.Duration
	RequestTimeout time.Duration
	WatchBackoff   time.Duration
	Username       string
	Password       string
	TLS            security.TLSConfig
}

type EtcdHealthStore struct {
	kv              healthKVStore
	prefix          string
	instancesPrefix string
	requestTimeout  time.Duration
	watchBackoff    time.Duration
}

type healthKV struct {
	Key         string
	Value       []byte
	ModRevision int64
}

type healthKVStore interface {
	List(ctx context.Context, prefix string) ([]healthKV, int64, error)
	Get(ctx context.Context, key string) (healthKV, bool, error)
	PutIfModRevision(ctx context.Context, key string, value []byte, expectedModRevision int64) (int64, bool, error)
	Watch(ctx context.Context, prefix string, afterRevision int64) (<-chan HealthStoreEvent, error)
	Close() error
}

type healthRecord struct {
	Service                 string    `json:"service"`
	InstanceID              string    `json:"instance_id"`
	Address                 string    `json:"address,omitempty"`
	RegistrationEpoch       string    `json:"registration_epoch,omitempty"`
	State                   string    `json:"state"`
	SlowScore               float64   `json:"slow_score,omitempty"`
	ConsecutiveSlowWindows  int       `json:"consecutive_slow_windows,omitempty"`
	ConsecutiveEjectWindows int       `json:"consecutive_eject_windows,omitempty"`
	LastTransitionAt        time.Time `json:"last_transition_at,omitempty"`
	EjectedAt               time.Time `json:"ejected_at,omitempty"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func NewEtcdHealthStore(ctx context.Context, cfg EtcdHealthStoreConfig) (*EtcdHealthStore, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("etcd health store requires at least one endpoint")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultEtcdHealthDialTimeout
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultEtcdHealthRequestTime
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
	store, err := newEtcdHealthStoreWithKV(&etcdHealthKVStore{client: client, requestTimeout: cfg.RequestTimeout}, cfg)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return store, nil
}

func newEtcdHealthStoreWithKV(kv healthKVStore, cfg EtcdHealthStoreConfig) (*EtcdHealthStore, error) {
	if kv == nil {
		return nil, fmt.Errorf("etcd health store requires a kv store")
	}
	watchBackoff := cfg.WatchBackoff
	if watchBackoff <= 0 {
		watchBackoff = defaultEtcdHealthWatchBackoff
	}
	prefix := normalizeEtcdHealthPrefix(cfg.Prefix)
	return &EtcdHealthStore{
		kv:              kv,
		prefix:          prefix,
		instancesPrefix: healthInstancesPrefix(prefix),
		requestTimeout:  cfg.RequestTimeout,
		watchBackoff:    watchBackoff,
	}, nil
}

func (s *EtcdHealthStore) Load(ctx context.Context) ([]EndpointHealth, int64, error) {
	kvs, revision, err := s.kv.List(ctx, s.instancesPrefix)
	if err != nil {
		return nil, 0, err
	}
	health := make([]EndpointHealth, 0, len(kvs))
	for _, kv := range kvs {
		endpoint, ok, err := healthFromKV(s.instancesPrefix, kv)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			health = append(health, endpoint)
		}
	}
	sort.Slice(health, func(i, j int) bool {
		if health[i].Service != health[j].Service {
			return health[i].Service < health[j].Service
		}
		return health[i].InstanceID < health[j].InstanceID
	})
	return health, revision, nil
}

func (s *EtcdHealthStore) Save(ctx context.Context, health []EndpointHealth) (int64, error) {
	var maxRevision int64
	for _, endpoint := range health {
		if endpoint.Service == "" || endpoint.InstanceID == "" {
			continue
		}
		if endpoint.State == status.Unspecified {
			endpoint.State = StateHealthy
		}
		if endpoint.UpdatedAt.IsZero() {
			endpoint.UpdatedAt = time.Now()
		}
		key := HealthInstanceKey(s.prefix, endpoint.Service, endpoint.InstanceID)
		value, err := encodeHealth(endpoint)
		if err != nil {
			return maxRevision, err
		}
		for {
			current, ok, err := s.kv.Get(ctx, key)
			if err != nil {
				return maxRevision, err
			}
			expectedRevision := int64(0)
			if ok {
				existing, _, err := healthFromKV(s.instancesPrefix, current)
				if err != nil {
					return maxRevision, err
				}
				if existing.UpdatedAt.After(endpoint.UpdatedAt) || existing == endpoint {
					break
				}
				expectedRevision = current.ModRevision
			}
			revision, stored, err := s.kv.PutIfModRevision(ctx, key, value, expectedRevision)
			if err != nil {
				return maxRevision, err
			}
			if stored {
				if revision > maxRevision {
					maxRevision = revision
				}
				break
			}
			if err := ctx.Err(); err != nil {
				return maxRevision, err
			}
		}
	}
	return maxRevision, nil
}

func (s *EtcdHealthStore) Watch(ctx context.Context, afterRevision int64) (<-chan HealthStoreEvent, error) {
	return s.kv.Watch(ctx, s.instancesPrefix, afterRevision)
}

func (s *EtcdHealthStore) Close() error {
	if s == nil || s.kv == nil {
		return nil
	}
	return s.kv.Close()
}

func encodeHealth(endpoint EndpointHealth) ([]byte, error) {
	return json.Marshal(healthRecord{
		Service:                 endpoint.Service,
		InstanceID:              endpoint.InstanceID,
		Address:                 endpoint.Address,
		RegistrationEpoch:       endpoint.RegistrationEpoch,
		State:                   status.Normalized(endpoint.State).String(),
		SlowScore:               endpoint.SlowScore,
		ConsecutiveSlowWindows:  endpoint.ConsecutiveSlowWindows,
		ConsecutiveEjectWindows: endpoint.ConsecutiveEjectWindows,
		LastTransitionAt:        endpoint.LastTransitionAt,
		EjectedAt:               endpoint.EjectedAt,
		UpdatedAt:               endpoint.UpdatedAt,
	})
}

func decodeHealth(value []byte) (EndpointHealth, error) {
	var record healthRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return EndpointHealth{}, err
	}
	if record.Service == "" || record.InstanceID == "" {
		return EndpointHealth{}, fmt.Errorf("health snapshot missing service or instance id")
	}
	state := status.Parse(record.State)
	if state == status.Unspecified {
		state = StateHealthy
	}
	return EndpointHealth{
		Service:                 record.Service,
		InstanceID:              record.InstanceID,
		Address:                 record.Address,
		RegistrationEpoch:       record.RegistrationEpoch,
		State:                   state,
		SlowScore:               record.SlowScore,
		ConsecutiveSlowWindows:  record.ConsecutiveSlowWindows,
		ConsecutiveEjectWindows: record.ConsecutiveEjectWindows,
		LastTransitionAt:        record.LastTransitionAt,
		EjectedAt:               record.EjectedAt,
		UpdatedAt:               record.UpdatedAt,
	}, nil
}

func healthFromKV(instancesPrefix string, kv healthKV) (EndpointHealth, bool, error) {
	service, instanceID, ok := healthIdentityFromKey(instancesPrefix, kv.Key)
	if !ok {
		return EndpointHealth{}, false, nil
	}
	health, err := decodeHealth(kv.Value)
	if err != nil {
		return EndpointHealth{}, false, fmt.Errorf("decode health %s/%s: %w", service, instanceID, err)
	}
	health.Service = service
	health.InstanceID = instanceID
	return health, true, nil
}

func normalizeEtcdHealthPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = defaultEtcdHealthPrefix
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(path.Clean(prefix), "/")
}

func healthInstancesPrefix(prefix string) string {
	return normalizeEtcdHealthPrefix(prefix) + "/services/"
}

func HealthInstanceKey(prefix, service, instanceID string) string {
	service = strings.TrimSpace(service)
	instanceID = strings.TrimSpace(instanceID)
	return healthInstancesPrefix(prefix) + url.PathEscape(service) + "/instances/" + url.PathEscape(instanceID)
}

func healthIdentityFromKey(instancesPrefix, key string) (string, string, bool) {
	if !strings.HasPrefix(key, instancesPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, instancesPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "instances" || parts[0] == "" || parts[2] == "" {
		return "", "", false
	}
	service, err := url.PathUnescape(parts[0])
	if err != nil || service == "" {
		return "", "", false
	}
	instanceID, err := url.PathUnescape(parts[2])
	if err != nil || instanceID == "" {
		return "", "", false
	}
	return service, instanceID, true
}

type etcdHealthKVStore struct {
	client         *clientv3.Client
	requestTimeout time.Duration
}

func (s *etcdHealthKVStore) List(ctx context.Context, prefix string) ([]healthKV, int64, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, 0, err
	}
	kvs := make([]healthKV, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		kvs = append(kvs, healthKV{Key: string(kv.Key), Value: append([]byte(nil), kv.Value...), ModRevision: kv.ModRevision})
	}
	return kvs, resp.Header.Revision, nil
}

func (s *etcdHealthKVStore) Get(ctx context.Context, key string) (healthKV, bool, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return healthKV{}, false, err
	}
	if len(resp.Kvs) == 0 {
		return healthKV{}, false, nil
	}
	kv := resp.Kvs[0]
	return healthKV{Key: string(kv.Key), Value: append([]byte(nil), kv.Value...), ModRevision: kv.ModRevision}, true, nil
}

func (s *etcdHealthKVStore) PutIfModRevision(ctx context.Context, key string, value []byte, expectedModRevision int64) (int64, bool, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	resp, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", expectedModRevision)).
		Then(clientv3.OpPut(key, string(value))).
		Commit()
	if err != nil {
		return 0, false, err
	}
	return resp.Header.Revision, resp.Succeeded, nil
}

func (s *etcdHealthKVStore) Watch(ctx context.Context, prefix string, afterRevision int64) (<-chan HealthStoreEvent, error) {
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if afterRevision > 0 {
		options = append(options, clientv3.WithRev(afterRevision+1))
	}
	source := s.client.Watch(ctx, prefix, options...)
	updates := make(chan HealthStoreEvent, 1)
	go func() {
		defer close(updates)
		for watchResp := range source {
			if watchResp.Err() != nil || watchResp.Canceled {
				select {
				case updates <- HealthStoreEvent{Revision: watchResp.Header.Revision, Err: watchResp.Err()}:
				case <-ctx.Done():
				}
				return
			}
			revision := watchResp.Header.Revision
			if revision == 0 {
				continue
			}
			select {
			case updates <- HealthStoreEvent{Revision: revision}:
			case <-ctx.Done():
				return
			default:
				select {
				case <-updates:
				case <-ctx.Done():
					return
				}
				select {
				case updates <- HealthStoreEvent{Revision: revision}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return updates, nil
}

func (s *etcdHealthKVStore) Close() error {
	return s.client.Close()
}

func (s *etcdHealthKVStore) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.requestTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.requestTimeout)
}
