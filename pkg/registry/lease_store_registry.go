package registry

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

// leaseStore defines persistence operations for lease store state.
type leaseStore interface {
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) (int64, error)
	Update(ctx context.Context, key string, value []byte, ttl time.Duration, expectedModRevision int64) (int64, bool, error)
	Get(ctx context.Context, key string) ([]byte, int64, bool, error)
	List(ctx context.Context, prefix string) ([]leaseStoreKV, int64, error)
	Watch(ctx context.Context, prefix string, afterVersion int64) (<-chan leaseStoreRevision, error)
	Close() error
}

// leaseStoreSweeper defines the lease store sweeper contract used by registry persistence and watch paths.
type leaseStoreSweeper interface {
	SweepExpired(ctx context.Context) int
}

// leaseStoreKV carries lease store kv state for registry persistence and watch paths.
type leaseStoreKV struct {
	Key         string
	Value       []byte
	ModRevision int64
}

// leaseStoreRevision carries lease store revision state for registry persistence and watch paths.
type leaseStoreRevision struct {
	Revision int64
}

// LeaseStoreRegistry stores endpoint leases behind a revisioned key-value contract used by watches and heartbeats.
type LeaseStoreRegistry struct {
	store  leaseStore
	prefix string
	now    func() time.Time
}

// leaseStoreRecord carries lease store record state for registry persistence and watch paths.
type leaseStoreRecord struct {
	ID                string            `json:"id"`
	Service           string            `json:"service"`
	Address           string            `json:"address"`
	Status            string            `json:"status,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	LastSeenUnixNanos int64             `json:"last_seen_unix_nanos"`
	RegistrationEpoch string            `json:"registration_epoch,omitempty"`
	OwnerToken        string            `json:"owner_token,omitempty"`
}

// newLeaseStoreRegistry initializes lease store registry with package defaults for this package's call path.
func newLeaseStoreRegistry(store leaseStore, prefix string, now func() time.Time) (*LeaseStoreRegistry, error) {
	if store == nil {
		return nil, ErrInvalidInstance
	}
	if now == nil {
		now = time.Now
	}
	return &LeaseStoreRegistry{
		store:  store,
		prefix: cleanLeaseStorePrefix(prefix),
		now:    now,
	}, nil
}

// Register registers register with the controller or local registry.
func (r *LeaseStoreRegistry) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if inst.ID == "" || inst.Service == "" || inst.Address == "" || ttl <= 0 {
		return ErrInvalidInstance
	}
	if inst.Status == status.Unspecified {
		inst.Status = InstanceHealthy
	}
	inst.LastSeen = r.now()
	inst.RegistrationEpoch = newRegistrationEpoch()
	inst.OwnerToken = newOwnerToken()
	inst.Labels = cloneLabels(inst.Labels)
	value, err := encodeLeaseStoreRecord(inst)
	if err != nil {
		return err
	}
	_, err = r.store.Put(ctx, r.instanceKey(inst.Service, inst.ID), value, ttl)
	return err
}

// Heartbeat refreshes the instance lease using the current registration fence.
func (r *LeaseStoreRegistry) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	return r.heartbeat(ctx, service, id, "", "", ttl, false, false)
}

// HeartbeatWithEpoch refreshes the instance lease using the current registration fence.
func (r *LeaseStoreRegistry) HeartbeatWithEpoch(ctx context.Context, service, id, registrationEpoch string, ttl time.Duration) error {
	if registrationEpoch == "" {
		return ErrInvalidInstance
	}
	return r.heartbeat(ctx, service, id, registrationEpoch, "", ttl, true, false)
}

// HeartbeatWithOwner refreshes the instance lease using the current registration fence.
func (r *LeaseStoreRegistry) HeartbeatWithOwner(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration) error {
	if registrationEpoch == "" || ownerToken == "" {
		return ErrInvalidInstance
	}
	return r.heartbeat(ctx, service, id, registrationEpoch, ownerToken, ttl, true, true)
}

// heartbeat refreshes the instance lease using the current registration fence.
func (r *LeaseStoreRegistry) heartbeat(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration, requireEpoch, requireOwner bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == "" || id == "" || ttl <= 0 {
		return ErrInvalidInstance
	}
	key := r.instanceKey(service, id)
	raw, modRevision, ok, err := r.store.Get(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInstanceNotFound
	}
	inst, err := decodeLeaseStoreRecord(raw)
	if err != nil {
		return err
	}
	if requireEpoch && inst.RegistrationEpoch != registrationEpoch {
		return ErrRegistrationEpochMismatch
	}
	if requireOwner && inst.OwnerToken != ownerToken {
		return ErrRegistrationEpochMismatch
	}
	inst.LastSeen = r.now()
	value, err := encodeLeaseStoreRecord(inst)
	if err != nil {
		return err
	}
	_, updated, err := r.store.Update(ctx, key, value, ttl, modRevision)
	if err != nil {
		return err
	}
	if !updated {
		return ErrInstanceNotFound
	}
	return nil
}

// List returns a point-in-time list of list visible to the caller.
func (r *LeaseStoreRegistry) List(ctx context.Context, service string) ([]Instance, error) {
	snapshot, err := r.Snapshot(ctx, service)
	if err != nil {
		return nil, err
	}
	return snapshot.Instances, nil
}

// Snapshot returns an immutable snapshot of the current snapshot state.
func (r *LeaseStoreRegistry) Snapshot(ctx context.Context, service string) (InstanceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return InstanceSnapshot{}, err
	}
	if service == "" {
		return InstanceSnapshot{}, ErrInvalidInstance
	}
	kvs, revision, err := r.store.List(ctx, r.servicePrefix(service))
	if err != nil {
		return InstanceSnapshot{}, err
	}
	instances := make([]Instance, 0, len(kvs))
	serviceVersion := int64(0)
	for _, kv := range kvs {
		inst, err := decodeLeaseStoreRecord(kv.Value)
		if err != nil {
			return InstanceSnapshot{}, err
		}
		if inst.Service == service {
			instances = append(instances, inst)
			if kv.ModRevision > serviceVersion {
				serviceVersion = kv.ModRevision
			}
		}
	}
	if serviceVersion == 0 && revision > 0 {
		serviceVersion = revision
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID < instances[j].ID
	})
	return InstanceSnapshot{
		Service:   service,
		Version:   serviceVersion,
		Instances: instances,
	}, nil
}

// Watch streams backing-source changes to callers until the source or context closes.
func (r *LeaseStoreRegistry) Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if service == "" {
		return nil, ErrInvalidInstance
	}
	initial, err := r.Snapshot(ctx, service)
	if err != nil {
		return nil, err
	}
	watchFrom := initial.Version
	if afterVersion > watchFrom {
		watchFrom = afterVersion
	}
	revisions, err := r.store.Watch(ctx, r.servicePrefix(service), watchFrom)
	if err != nil {
		return nil, err
	}

	updates := make(chan InstanceSnapshot, 1)
	go r.watch(ctx, service, afterVersion, initial, revisions, updates)
	return updates, nil
}

// watch streams backing-source changes to callers until the source or context closes.
func (r *LeaseStoreRegistry) watch(ctx context.Context, service string, afterVersion int64, initial InstanceSnapshot, revisions <-chan leaseStoreRevision, updates chan InstanceSnapshot) {
	defer close(updates)

	lastSentVersion := afterVersion
	if initial.Version > lastSentVersion {
		lastSentVersion = initial.Version
		if !sendLatestSnapshot(ctx, updates, initial) {
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case revision, ok := <-revisions:
			if !ok {
				return
			}
			if revision.Revision != 0 && revision.Revision <= lastSentVersion {
				continue
			}
			snapshot, err := r.Snapshot(ctx, service)
			if err != nil {
				return
			}
			if revision.Revision > snapshot.Version {
				snapshot.Version = revision.Revision
			}
			if snapshot.Version <= lastSentVersion {
				continue
			}
			lastSentVersion = snapshot.Version
			if !sendLatestSnapshot(ctx, updates, snapshot) {
				return
			}
		}
	}
}

// SweepExpired removes expired sweep expired records from the backing store.
func (r *LeaseStoreRegistry) SweepExpired(ctx context.Context) int {
	if sweeper, ok := r.store.(leaseStoreSweeper); ok {
		return sweeper.SweepExpired(ctx)
	}
	return 0
}

// Close closes owned resources and makes repeated calls safe.
func (r *LeaseStoreRegistry) Close() error {
	return r.store.Close()
}

// instanceKey returns instance key data for LeaseStoreRegistry callers without handing out mutable receiver state.
func (r *LeaseStoreRegistry) instanceKey(service, id string) string {
	return r.servicePrefix(service) + escapeLeaseStorePathSegment(id)
}

// servicePrefix returns service prefix data for LeaseStoreRegistry callers without handing out mutable receiver state.
func (r *LeaseStoreRegistry) servicePrefix(service string) string {
	return r.prefix + "/" + escapeLeaseStorePathSegment(service) + "/"
}

// cleanLeaseStorePrefix provides the shared clean lease store prefix helper for registry persistence and watch paths.
func cleanLeaseStorePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "/aegismesh/registry"
	}
	prefix = "/" + strings.Trim(prefix, "/")
	return prefix
}

// escapeLeaseStorePathSegment provides the shared escape lease store path segment helper for registry persistence and watch paths.
func escapeLeaseStorePathSegment(value string) string {
	return url.PathEscape(value)
}

// encodeLeaseStoreRecord keeps encode lease store record rules consistent for registry persistence and watch paths.
func encodeLeaseStoreRecord(inst Instance) ([]byte, error) {
	record := leaseStoreRecord{
		ID:                inst.ID,
		Service:           inst.Service,
		Address:           inst.Address,
		Status:            inst.Status.String(),
		Labels:            cloneLabels(inst.Labels),
		LastSeenUnixNanos: inst.LastSeen.UnixNano(),
		RegistrationEpoch: inst.RegistrationEpoch,
		OwnerToken:        inst.OwnerToken,
	}
	return json.Marshal(record)
}

// decodeLeaseStoreRecord keeps decode lease store record rules consistent for registry persistence and watch paths.
func decodeLeaseStoreRecord(raw []byte) (Instance, error) {
	var record leaseStoreRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return Instance{}, err
	}
	state := status.Parse(record.Status)
	if state == status.Unspecified {
		state = InstanceHealthy
	}
	return Instance{
		ID:                record.ID,
		Service:           record.Service,
		Address:           record.Address,
		Status:            state,
		Labels:            cloneLabels(record.Labels),
		LastSeen:          time.Unix(0, record.LastSeenUnixNanos),
		RegistrationEpoch: record.RegistrationEpoch,
		OwnerToken:        record.OwnerToken,
	}, nil
}
