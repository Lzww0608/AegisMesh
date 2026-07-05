package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/aegismesh/aegismesh/pkg/security"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// EtcdRegistryConfig selects etcd endpoints, key prefix, timeouts, credentials, and TLS for lease-backed registry storage.
type EtcdRegistryConfig struct {
	Endpoints      []string
	Prefix         string
	DialTimeout    time.Duration
	RequestTimeout time.Duration
	Username       string
	Password       string
	TLS            security.TLSConfig
}

// NewEtcdRegistry initializes etcd registry with package defaults for this package's call path.
func NewEtcdRegistry(cfg EtcdRegistryConfig, now func() time.Time) (*LeaseStoreRegistry, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, fmt.Errorf("etcd registry requires at least one endpoint")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 3 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 3 * time.Second
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
	reg, err := newLeaseStoreRegistry(&etcdLeaseStore{client: client, requestTimeout: cfg.RequestTimeout}, cfg.Prefix, now)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return reg, nil
}

// etcdLeaseStore defines persistence operations for etcd lease store state.
type etcdLeaseStore struct {
	client         *clientv3.Client
	requestTimeout time.Duration
}

// Put writes a leased key and revokes the previous lease after etcd accepts the replacement.
func (s *etcdLeaseStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (int64, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	lease, err := s.client.Grant(ctx, leaseTTLSeconds(ttl))
	if err != nil {
		return 0, err
	}
	resp, err := s.client.Put(ctx, key, string(value), clientv3.WithLease(lease.ID), clientv3.WithPrevKV())
	if err != nil {
		s.revokeLease(lease.ID)
		return 0, err
	}
	s.revokePreviousLease(resp.PrevKv, lease.ID)
	return resp.Header.Revision, nil
}

// Update writes a leased key only when the stored mod revision still matches the caller expectation.
func (s *etcdLeaseStore) Update(ctx context.Context, key string, value []byte, ttl time.Duration, expectedModRevision int64) (int64, bool, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	lease, err := s.client.Grant(ctx, leaseTTLSeconds(ttl))
	if err != nil {
		return 0, false, err
	}
	resp, err := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(key), "=", expectedModRevision)).
		Then(clientv3.OpPut(key, string(value), clientv3.WithLease(lease.ID), clientv3.WithPrevKV())).
		Commit()
	if err != nil {
		s.revokeLease(lease.ID)
		return 0, false, err
	}
	if !resp.Succeeded {
		s.revokeLease(lease.ID)
		return resp.Header.Revision, false, nil
	}
	s.revokePreviousLease(txnPutPrevKV(resp), lease.ID)
	return resp.Header.Revision, true, nil
}

// Get returns get state for the requested key.
func (s *etcdLeaseStore) Get(ctx context.Context, key string) ([]byte, int64, bool, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return nil, 0, false, err
	}
	if len(resp.Kvs) == 0 {
		return nil, resp.Header.Revision, false, nil
	}
	kv := resp.Kvs[0]
	return append([]byte(nil), kv.Value...), kv.ModRevision, true, nil
}

// List returns a point-in-time list of list visible to the caller.
func (s *etcdLeaseStore) List(ctx context.Context, prefix string) ([]leaseStoreKV, int64, error) {
	ctx, cancel := s.withRequestTimeout(ctx)
	defer cancel()
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, 0, err
	}
	kvs := make([]leaseStoreKV, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		kvs = append(kvs, leaseStoreKV{
			Key:         string(kv.Key),
			Value:       append([]byte(nil), kv.Value...),
			ModRevision: kv.ModRevision,
		})
	}
	return kvs, resp.Header.Revision, nil
}

// Watch streams backing-source changes to callers until the source or context closes.
func (s *etcdLeaseStore) Watch(ctx context.Context, prefix string, afterVersion int64) (<-chan leaseStoreRevision, error) {
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if afterVersion > 0 {
		options = append(options, clientv3.WithRev(afterVersion+1))
	}
	source := s.client.Watch(ctx, prefix, options...)
	updates := make(chan leaseStoreRevision, 1)
	go func() {
		defer close(updates)
		for watchResp := range source {
			if watchResp.Err() != nil || watchResp.Canceled {
				return
			}
			revision := watchResp.Header.Revision
			if revision == 0 {
				continue
			}
			select {
			case updates <- leaseStoreRevision{Revision: revision}:
			case <-ctx.Done():
				return
			default:
				select {
				case <-updates:
				case <-ctx.Done():
					return
				}
				select {
				case updates <- leaseStoreRevision{Revision: revision}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return updates, nil
}

// Close closes owned resources and makes repeated calls safe.
func (s *etcdLeaseStore) Close() error {
	return s.client.Close()
}

// revokePreviousLease releases the old etcd lease after a replacement key has been committed.
func (s *etcdLeaseStore) revokePreviousLease(prev *mvccpb.KeyValue, current clientv3.LeaseID) {
	if prev == nil || prev.Lease == 0 || clientv3.LeaseID(prev.Lease) == current {
		return
	}
	s.revokeLease(clientv3.LeaseID(prev.Lease))
}

// revokeLease best-effort revokes an etcd lease using the store request timeout.
func (s *etcdLeaseStore) revokeLease(id clientv3.LeaseID) {
	if id == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if s.requestTimeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), s.requestTimeout)
	}
	defer cancel()
	_, _ = s.client.Revoke(ctx, id)
}

// txnPutPrevKV provides the shared txn put prev kv helper for registry persistence and watch paths.
func txnPutPrevKV(resp *clientv3.TxnResponse) *mvccpb.KeyValue {
	if resp == nil || len(resp.Responses) == 0 {
		return nil
	}
	put := resp.Responses[0].GetResponsePut()
	if put == nil {
		return nil
	}
	return put.PrevKv
}

// withRequestTimeout returns with request timeout data for etcdLeaseStore callers without handing out mutable receiver state.
func (s *etcdLeaseStore) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.requestTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.requestTimeout)
}

// leaseTTLSeconds provides the shared lease ttl seconds helper for registry persistence and watch paths.
func leaseTTLSeconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 1
	}
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	if seconds <= 0 {
		seconds = 1
	}
	return seconds
}
