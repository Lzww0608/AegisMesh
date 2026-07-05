package registry

import (
	"container/heap"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

// InstanceStatus mirrors the endpoint status vocabulary used by registry records.
type InstanceStatus = status.Code

const (
	InstanceHealthy  = status.Healthy
	InstanceDegraded = status.Degraded
	InstanceEjected  = status.Ejected
	InstanceProbing  = status.Probing
	InstanceDead     = status.Dead
)

var (
	ErrInvalidInstance           = errors.New("invalid service instance")
	ErrInstanceNotFound          = errors.New("service instance not found")
	ErrRegistrationEpochMismatch = errors.New("registration epoch mismatch")
)

// Instance is the controller-owned service discovery record for one endpoint.
type Instance struct {
	ID                string
	Service           string
	Address           string
	Status            InstanceStatus
	Labels            map[string]string
	LastSeen          time.Time
	RegistrationEpoch string
	OwnerToken        string
}

// Registry is the minimal service-discovery contract shared by memory, file, and lease-backed registries.
type Registry interface {
	Register(ctx context.Context, inst Instance, ttl time.Duration) error
	Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error
	List(ctx context.Context, service string) ([]Instance, error)
	SweepExpired(ctx context.Context) int
}

// EpochHeartbeater refreshes leases only when the caller presents the active epoch.
type EpochHeartbeater interface {
	HeartbeatWithEpoch(ctx context.Context, service, id, registrationEpoch string, ttl time.Duration) error
}

// OwnerHeartbeater refreshes leases only for the current epoch and owner token.
type OwnerHeartbeater interface {
	HeartbeatWithOwner(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration) error
}

// Snapshotter returns versioned registry snapshots for watch and resolver flows.
type Snapshotter interface {
	Snapshot(ctx context.Context, service string) (InstanceSnapshot, error)
}

// Watcher streams versioned registry snapshots after a caller-supplied revision.
type Watcher interface {
	Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error)
}

// InstanceSnapshot is an immutable point-in-time view of instances for one service.
type InstanceSnapshot struct {
	Service   string
	Version   int64
	Instances []Instance

	nextExpiresAt time.Time
}

// MemoryRegistry stores service instances in memory with TTL expiry and watches.
type MemoryRegistry struct {
	now func() time.Time

	services   sync.Map // map[string]*serviceState; each state owns per-service record locking.
	expiryMu   sync.Mutex
	expiries   expiryHeap
	generation atomic.Uint64 // fences stale expiry heap entries from refreshed records.
}

var registrationEpochFallback atomic.Uint64

// record carries record state for registry persistence and watch paths.
type record struct {
	instance   Instance
	expiresAt  time.Time
	generation uint64
}

// serviceState owns one service namespace, its immutable snapshot, and watcher signal.
type serviceState struct {
	mu       sync.Mutex
	records  map[string]record
	snapshot atomic.Pointer[InstanceSnapshot] // published snapshots are never mutated after Store.
	version  atomic.Int64
	notify   chan struct{}
}

// expiryEntry carries expiry entry state for registry persistence and watch paths.
type expiryEntry struct {
	service    string
	id         string
	expiresAt  time.Time
	generation uint64
}

// expiredRecord carries expired record state for registry persistence and watch paths.
type expiredRecord struct {
	service string
	id      string
}

// expiryHeap carries expiry heap state for registry persistence and watch paths.
type expiryHeap []expiryEntry

// Len reports the number of entries in the collection.
func (h expiryHeap) Len() int {
	return len(h)
}

// Less reports whether one collection entry sorts before another.
func (h expiryHeap) Less(i, j int) bool {
	return h[i].expiresAt.Before(h[j].expiresAt)
}

// Swap swaps two collection entries in place.
func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

// Push adds an entry to the heap while preserving heap.Interface semantics.
func (h *expiryHeap) Push(x any) {
	*h = append(*h, x.(expiryEntry))
}

// Pop removes and returns the heap tail entry for heap.Interface semantics.
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	*h = old[:n-1]
	return entry
}

// NewMemoryRegistry initializes memory registry with package defaults for this package's call path.
func NewMemoryRegistry(now func() time.Time) *MemoryRegistry {
	if now == nil {
		now = time.Now
	}
	return &MemoryRegistry{now: now}
}

// Register stores a fresh instance lease and assigns a new ownership fence.
func (r *MemoryRegistry) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	return r.registerAt(ctx, inst, ttl, r.now())
}

// registerAt performs Register against an injected clock for deterministic tests.
func (r *MemoryRegistry) registerAt(ctx context.Context, inst Instance, ttl time.Duration, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if inst.ID == "" || inst.Service == "" || inst.Address == "" {
		return ErrInvalidInstance
	}
	if ttl <= 0 {
		return ErrInvalidInstance
	}
	if inst.Status == status.Unspecified {
		inst.Status = InstanceHealthy
	}

	inst.LastSeen = now
	inst.RegistrationEpoch = newRegistrationEpoch()
	inst.OwnerToken = newOwnerToken()
	inst.Labels = cloneLabels(inst.Labels)
	expiresAt := inst.LastSeen.Add(ttl)
	// The generation travels with both the record and heap entry so old expiry entries cannot delete refreshed leases.
	generation := r.generation.Add(1)

	state := r.serviceState(inst.Service)
	state.mu.Lock()
	state.records[inst.ID] = record{
		instance:   inst,
		expiresAt:  expiresAt,
		generation: generation,
	}
	state.rebuildSnapshotLocked(inst.Service, now)
	state.mu.Unlock()

	r.pushExpiry(expiryEntry{service: inst.Service, id: inst.ID, expiresAt: expiresAt, generation: generation})
	return nil
}

// Heartbeat refreshes the instance lease using the current registration fence.
func (r *MemoryRegistry) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	return r.heartbeatAt(ctx, service, id, ttl, r.now())
}

// HeartbeatWithEpoch refreshes the instance lease using the current registration fence.
func (r *MemoryRegistry) HeartbeatWithEpoch(ctx context.Context, service, id, registrationEpoch string, ttl time.Duration) error {
	if registrationEpoch == "" {
		return ErrInvalidInstance
	}
	return r.heartbeatAtWithOwner(ctx, service, id, registrationEpoch, "", ttl, r.now(), true, false)
}

// HeartbeatWithOwner refreshes the instance lease using the current registration fence.
func (r *MemoryRegistry) HeartbeatWithOwner(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration) error {
	if registrationEpoch == "" || ownerToken == "" {
		return ErrInvalidInstance
	}
	return r.heartbeatAtWithOwner(ctx, service, id, registrationEpoch, ownerToken, ttl, r.now(), true, true)
}

// heartbeatAt refreshes the instance lease using the current registration fence.
func (r *MemoryRegistry) heartbeatAt(ctx context.Context, service, id string, ttl time.Duration, now time.Time) error {
	return r.heartbeatAtWithOwner(ctx, service, id, "", "", ttl, now, false, false)
}

// heartbeatAtWithEpoch refreshes the instance lease using the current registration fence.
func (r *MemoryRegistry) heartbeatAtWithEpoch(ctx context.Context, service, id, registrationEpoch string, ttl time.Duration, now time.Time, requireEpoch bool) error {
	return r.heartbeatAtWithOwner(ctx, service, id, registrationEpoch, "", ttl, now, requireEpoch, false)
}

// validateHeartbeatWithOwner checks the epoch/token fence without extending the lease.
func (r *MemoryRegistry) validateHeartbeatWithOwner(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration, requireEpoch, requireOwner bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == "" || id == "" || ttl <= 0 {
		return ErrInvalidInstance
	}

	stateValue, ok := r.services.Load(service)
	if !ok {
		return ErrInstanceNotFound
	}
	state := stateValue.(*serviceState)
	state.mu.Lock()
	defer state.mu.Unlock()

	rec, ok := state.records[id]
	if !ok {
		return ErrInstanceNotFound
	}
	if requireEpoch && rec.instance.RegistrationEpoch != registrationEpoch {
		return ErrRegistrationEpochMismatch
	}
	if requireOwner && rec.instance.OwnerToken != ownerToken {
		return ErrRegistrationEpochMismatch
	}
	return nil
}

// heartbeatAtWithOwner refreshes a lease after validating any requested ownership fence.
func (r *MemoryRegistry) heartbeatAtWithOwner(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration, now time.Time, requireEpoch, requireOwner bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == "" || id == "" || ttl <= 0 {
		return ErrInvalidInstance
	}

	stateValue, ok := r.services.Load(service)
	if !ok {
		return ErrInstanceNotFound
	}
	state := stateValue.(*serviceState)
	expiresAt := now.Add(ttl)
	// The generation travels with both the record and heap entry so old expiry entries cannot delete refreshed leases.
	generation := r.generation.Add(1)

	state.mu.Lock()
	rec, ok := state.records[id]
	if !ok {
		state.mu.Unlock()
		return ErrInstanceNotFound
	}
	if requireEpoch && rec.instance.RegistrationEpoch != registrationEpoch {
		state.mu.Unlock()
		return ErrRegistrationEpochMismatch
	}
	if requireOwner && rec.instance.OwnerToken != ownerToken {
		state.mu.Unlock()
		return ErrRegistrationEpochMismatch
	}

	rec.instance.LastSeen = now
	rec.expiresAt = expiresAt
	rec.generation = generation
	state.records[id] = rec
	state.rebuildSnapshotLocked(service, now)
	state.mu.Unlock()

	r.pushExpiry(expiryEntry{service: service, id: id, expiresAt: expiresAt, generation: generation})
	return nil
}

// List returns a point-in-time list of instances visible to the caller.
func (r *MemoryRegistry) List(ctx context.Context, service string) ([]Instance, error) {
	snapshot, err := r.Snapshot(ctx, service)
	if err != nil {
		return nil, err
	}
	return snapshot.Instances, nil
}

// Snapshot returns an immutable snapshot of the current snapshot state.
func (r *MemoryRegistry) Snapshot(ctx context.Context, service string) (InstanceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return InstanceSnapshot{}, err
	}
	if service == "" {
		return InstanceSnapshot{}, ErrInvalidInstance
	}

	stateValue, ok := r.services.Load(service)
	if !ok {
		return InstanceSnapshot{Service: service, Instances: []Instance{}}, nil
	}

	now := r.now()
	state := stateValue.(*serviceState)
	snapshot := state.snapshot.Load()
	if snapshotNeedsExpiry(snapshot, now) {
		r.sweepExpiredForService(ctx, service, state, now)
		snapshot = state.snapshot.Load()
	}
	return cloneInstanceSnapshot(*snapshot), nil
}

// SweepExpired removes expired sweep expired records from the backing store.
func (r *MemoryRegistry) SweepExpired(ctx context.Context) int {
	return len(r.sweepExpiredRecords(ctx))
}

// sweepExpiredRecords removes expired sweep expired records records from the backing store.
func (r *MemoryRegistry) sweepExpiredRecords(ctx context.Context) []expiredRecord {
	if err := ctx.Err(); err != nil {
		return nil
	}

	now := r.now()
	expired := make([]expiredRecord, 0)
	dirtyServices := make(map[string]*serviceState)
	for {
		entry, ok := r.popExpired(now)
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			break
		}
		stateValue, ok := r.services.Load(entry.service)
		if !ok {
			continue
		}
		state := stateValue.(*serviceState)
		state.mu.Lock()
		rec, ok := state.records[entry.id]
		if ok && rec.generation == entry.generation && !rec.expiresAt.After(now) {
			delete(state.records, entry.id)
			dirtyServices[entry.service] = state
			expired = append(expired, expiredRecord{service: entry.service, id: entry.id})
		}
		state.mu.Unlock()
	}
	for service, state := range dirtyServices {
		state.mu.Lock()
		state.rebuildSnapshotLocked(service, now)
		state.mu.Unlock()
	}
	return expired
}

// Watch streams backing-source changes to callers until the source or context closes.
func (r *MemoryRegistry) Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if service == "" {
		return nil, ErrInvalidInstance
	}

	state := r.serviceState(service)
	updates := make(chan InstanceSnapshot, 1)
	go r.watch(ctx, service, state, afterVersion, updates)
	return updates, nil
}

// watch streams backing-source changes to callers until the source or context closes.
func (r *MemoryRegistry) watch(ctx context.Context, service string, state *serviceState, afterVersion int64, updates chan InstanceSnapshot) {
	defer close(updates)

	lastVersion := afterVersion
	for {
		if err := ctx.Err(); err != nil {
			return
		}

		now := r.now()
		snapshot := state.snapshot.Load()
		if snapshotNeedsExpiry(snapshot, now) {
			r.sweepExpiredForService(ctx, service, state, now)
		}

		state.mu.Lock()
		snapshot = state.snapshot.Load()
		notify := state.notify
		state.mu.Unlock()

		if snapshot.Version > lastVersion {
			lastVersion = snapshot.Version
			if !sendLatestSnapshot(ctx, updates, *snapshot) {
				return
			}
			continue
		}

		expiryTimer, expiry := nextExpiryTimer(snapshot, now)
		select {
		case <-ctx.Done():
			stopTimer(expiryTimer)
			return
		case <-notify:
			stopTimer(expiryTimer)
		case <-expiry:
		}
	}
}

// serviceState returns service state data for MemoryRegistry callers without handing out mutable receiver state.
func (r *MemoryRegistry) serviceState(service string) *serviceState {
	if state, ok := r.services.Load(service); ok {
		return state.(*serviceState)
	}
	state := newServiceState(service)
	actual, _ := r.services.LoadOrStore(service, state)
	return actual.(*serviceState)
}

// newServiceState initializes service state with package defaults for this package's call path.
func newServiceState(service string) *serviceState {
	state := &serviceState{
		records: make(map[string]record),
		notify:  make(chan struct{}),
	}
	state.snapshot.Store(&InstanceSnapshot{
		Service:   service,
		Instances: []Instance{},
	})
	return state
}

// rebuildSnapshotLocked rebuilds the immutable service snapshot and wakes active watchers.
func (s *serviceState) rebuildSnapshotLocked(service string, now time.Time) {
	instances := make([]Instance, 0, len(s.records))
	var nextExpiresAt time.Time
	for _, rec := range s.records {
		if !rec.expiresAt.After(now) {
			continue
		}
		instances = append(instances, rec.instance)
		if nextExpiresAt.IsZero() || rec.expiresAt.Before(nextExpiresAt) {
			nextExpiresAt = rec.expiresAt
		}
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID < instances[j].ID
	})

	s.snapshot.Store(&InstanceSnapshot{
		Service:       service,
		Version:       s.version.Add(1),
		Instances:     instances,
		nextExpiresAt: nextExpiresAt,
	})
	close(s.notify)
	s.notify = make(chan struct{})
}

// pushExpiry records a lease deadline in the shared expiry heap under its own lock.
func (r *MemoryRegistry) pushExpiry(entry expiryEntry) {
	r.expiryMu.Lock()
	heap.Push(&r.expiries, entry)
	r.expiryMu.Unlock()
}

// popExpired returns pop expired data for MemoryRegistry callers without handing out mutable receiver state.
func (r *MemoryRegistry) popExpired(now time.Time) (expiryEntry, bool) {
	r.expiryMu.Lock()
	defer r.expiryMu.Unlock()

	if len(r.expiries) == 0 || r.expiries[0].expiresAt.After(now) {
		return expiryEntry{}, false
	}
	return heap.Pop(&r.expiries).(expiryEntry), true
}

// sweepExpiredForService removes expired sweep expired for service records from the backing store.
func (r *MemoryRegistry) sweepExpiredForService(ctx context.Context, service string, state *serviceState, now time.Time) int {
	if err := ctx.Err(); err != nil {
		return 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	expired := 0
	for id, rec := range state.records {
		if rec.expiresAt.After(now) {
			continue
		}
		delete(state.records, id)
		expired++
	}
	if expired > 0 {
		state.rebuildSnapshotLocked(service, now)
	}
	return expired
}

// recordExists records record exists in the current accounting window.
func (r *MemoryRegistry) recordExists(service, id string) bool {
	stateValue, ok := r.services.Load(service)
	if !ok {
		return false
	}
	state := stateValue.(*serviceState)
	state.mu.Lock()
	defer state.mu.Unlock()
	_, ok = state.records[id]
	return ok
}

// deleteRecord returns delete record data for MemoryRegistry callers without handing out mutable receiver state.
func (r *MemoryRegistry) deleteRecord(service, id string, now time.Time) bool {
	stateValue, ok := r.services.Load(service)
	if !ok {
		return false
	}
	state := stateValue.(*serviceState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, ok := state.records[id]; !ok {
		return false
	}
	delete(state.records, id)
	state.rebuildSnapshotLocked(service, now)
	return true
}

// snapshotNeedsExpiry returns an immutable snapshot of the current snapshot needs expiry state.
func snapshotNeedsExpiry(snapshot *InstanceSnapshot, now time.Time) bool {
	return snapshot != nil && !snapshot.nextExpiresAt.IsZero() && !snapshot.nextExpiresAt.After(now)
}

// nextExpiryTimer provides the shared next expiry timer helper for registry persistence and watch paths.
func nextExpiryTimer(snapshot *InstanceSnapshot, now time.Time) (*time.Timer, <-chan time.Time) {
	if snapshot == nil || snapshot.nextExpiresAt.IsZero() {
		return nil, nil
	}
	delay := snapshot.nextExpiresAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	return timer, timer.C
}

// stopTimer stops the expiry timer and drains its channel so later resets are not racing stale ticks.
func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

// restore rehydrates a live record only if it has not already expired.
func (r *MemoryRegistry) restore(inst Instance, expiresAt time.Time) {
	r.restoreRecord(inst, expiresAt, true)
}

// restoreHistorical replays persisted registry history without filtering expired records.
func (r *MemoryRegistry) restoreHistorical(inst Instance, expiresAt time.Time) {
	r.restoreRecord(inst, expiresAt, false)
}

// restoreRecord inserts replayed registry state and rebuilds the affected service snapshot.
func (r *MemoryRegistry) restoreRecord(inst Instance, expiresAt time.Time, filterExpired bool) {
	if inst.ID == "" || inst.Service == "" || inst.Address == "" {
		return
	}
	if filterExpired && !expiresAt.After(r.now()) {
		return
	}
	if inst.Status == status.Unspecified {
		inst.Status = InstanceHealthy
	}
	if inst.RegistrationEpoch == "" {
		inst.RegistrationEpoch = newRegistrationEpoch()
	}
	if inst.OwnerToken == "" {
		inst.OwnerToken = newOwnerToken()
	}
	inst.Labels = cloneLabels(inst.Labels)

	// The generation travels with both the record and heap entry so old expiry entries cannot delete refreshed leases.
	generation := r.generation.Add(1)
	state := r.serviceState(inst.Service)
	state.mu.Lock()
	state.records[inst.ID] = record{
		instance:   inst,
		expiresAt:  expiresAt,
		generation: generation,
	}
	state.rebuildSnapshotLocked(inst.Service, r.now())
	state.mu.Unlock()

	r.pushExpiry(expiryEntry{service: inst.Service, id: inst.ID, expiresAt: expiresAt, generation: generation})
}

// newRegistrationEpoch initializes registration epoch with package defaults for this package's call path.
func newRegistrationEpoch() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("fallback-%d-%d", time.Now().UnixNano(), registrationEpochFallback.Add(1))
}

// newOwnerToken initializes owner token with package defaults for this package's call path.
func newOwnerToken() string {
	return newRegistrationEpoch()
}

// cloneLabels returns an isolated copy of clone labels input so callers cannot mutate shared state.
func cloneLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// cloneInstances returns an isolated copy of clone instances input so callers cannot mutate shared state.
func cloneInstances(instances []Instance) []Instance {
	out := make([]Instance, len(instances))
	for i, inst := range instances {
		inst.Labels = cloneLabels(inst.Labels)
		out[i] = inst
	}
	return out
}

// cloneInstanceSnapshot returns an isolated copy of clone instance snapshot input so callers cannot mutate shared state.
func cloneInstanceSnapshot(snapshot InstanceSnapshot) InstanceSnapshot {
	snapshot.Instances = cloneInstances(snapshot.Instances)
	return snapshot
}

// sendLatestSnapshot provides the shared send latest snapshot helper for registry persistence and watch paths.
func sendLatestSnapshot(ctx context.Context, updates chan InstanceSnapshot, snapshot InstanceSnapshot) bool {
	cloned := cloneInstanceSnapshot(snapshot)
	select {
	case <-ctx.Done():
		return false
	default:
	}
	select {
	case updates <- cloned:
		return true
	case <-ctx.Done():
		return false
	default:
		select {
		case <-updates:
		case <-ctx.Done():
			return false
		}
		select {
		case updates <- cloned:
			return true
		case <-ctx.Done():
			return false
		}
	}
}
