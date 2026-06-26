package registry

import (
	"context"
	"container/heap"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/status"
)

type InstanceStatus = status.Code

const (
	InstanceHealthy  = status.Healthy
	InstanceDegraded = status.Degraded
	InstanceEjected  = status.Ejected
	InstanceProbing  = status.Probing
	InstanceDead     = status.Dead
)

var (
	ErrInvalidInstance  = errors.New("invalid service instance")
	ErrInstanceNotFound = errors.New("service instance not found")
)

type Instance struct {
	ID       string
	Service  string
	Address  string
	Status   InstanceStatus
	Labels   map[string]string
	LastSeen time.Time
}

type Registry interface {
	Register(ctx context.Context, inst Instance, ttl time.Duration) error
	Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error
	List(ctx context.Context, service string) ([]Instance, error)
	SweepExpired(ctx context.Context) int
}

type Snapshotter interface {
	Snapshot(ctx context.Context, service string) (InstanceSnapshot, error)
}

type Watcher interface {
	Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error)
}

type InstanceSnapshot struct {
	Service   string
	Version   int64
	Instances []Instance

	nextExpiresAt time.Time
}

type MemoryRegistry struct {
	now func() time.Time

	services   sync.Map // map[string]*serviceState
	expiryMu   sync.Mutex
	expiries   expiryHeap
	generation atomic.Uint64
}

type record struct {
	instance   Instance
	expiresAt  time.Time
	generation uint64
}

type serviceState struct {
	mu       sync.Mutex
	records  map[string]record
	snapshot atomic.Pointer[InstanceSnapshot]
	version  atomic.Int64
	notify   chan struct{}
}

type expiryEntry struct {
	service    string
	id         string
	expiresAt  time.Time
	generation uint64
}

type expiredRecord struct {
	service string
	id      string
}

type expiryHeap []expiryEntry

func (h expiryHeap) Len() int {
	return len(h)
}

func (h expiryHeap) Less(i, j int) bool {
	return h[i].expiresAt.Before(h[j].expiresAt)
}

func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *expiryHeap) Push(x any) {
	*h = append(*h, x.(expiryEntry))
}

func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	*h = old[:n-1]
	return entry
}

func NewMemoryRegistry(now func() time.Time) *MemoryRegistry {
	if now == nil {
		now = time.Now
	}
	return &MemoryRegistry{now: now}
}

func (r *MemoryRegistry) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	return r.registerAt(ctx, inst, ttl, r.now())
}

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
	inst.Labels = cloneLabels(inst.Labels)
	expiresAt := inst.LastSeen.Add(ttl)
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

func (r *MemoryRegistry) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	return r.heartbeatAt(ctx, service, id, ttl, r.now())
}

func (r *MemoryRegistry) heartbeatAt(ctx context.Context, service, id string, ttl time.Duration, now time.Time) error {
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
	generation := r.generation.Add(1)

	state.mu.Lock()
	rec, ok := state.records[id]
	if !ok {
		state.mu.Unlock()
		return ErrInstanceNotFound
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

func (r *MemoryRegistry) List(ctx context.Context, service string) ([]Instance, error) {
	snapshot, err := r.Snapshot(ctx, service)
	if err != nil {
		return nil, err
	}
	return snapshot.Instances, nil
}

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

func (r *MemoryRegistry) SweepExpired(ctx context.Context) int {
	return len(r.sweepExpiredRecords(ctx))
}

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

func (r *MemoryRegistry) serviceState(service string) *serviceState {
	if state, ok := r.services.Load(service); ok {
		return state.(*serviceState)
	}
	state := newServiceState(service)
	actual, _ := r.services.LoadOrStore(service, state)
	return actual.(*serviceState)
}

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

func (r *MemoryRegistry) pushExpiry(entry expiryEntry) {
	r.expiryMu.Lock()
	heap.Push(&r.expiries, entry)
	r.expiryMu.Unlock()
}

func (r *MemoryRegistry) popExpired(now time.Time) (expiryEntry, bool) {
	r.expiryMu.Lock()
	defer r.expiryMu.Unlock()

	if len(r.expiries) == 0 || r.expiries[0].expiresAt.After(now) {
		return expiryEntry{}, false
	}
	return heap.Pop(&r.expiries).(expiryEntry), true
}

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

func snapshotNeedsExpiry(snapshot *InstanceSnapshot, now time.Time) bool {
	return snapshot != nil && !snapshot.nextExpiresAt.IsZero() && !snapshot.nextExpiresAt.After(now)
}

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

func (r *MemoryRegistry) restore(inst Instance, expiresAt time.Time) {
	r.restoreRecord(inst, expiresAt, true)
}

func (r *MemoryRegistry) restoreHistorical(inst Instance, expiresAt time.Time) {
	r.restoreRecord(inst, expiresAt, false)
}

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
	inst.Labels = cloneLabels(inst.Labels)

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

func cloneInstances(instances []Instance) []Instance {
	out := make([]Instance, len(instances))
	for i, inst := range instances {
		inst.Labels = cloneLabels(inst.Labels)
		out[i] = inst
	}
	return out
}

func cloneInstanceSnapshot(snapshot InstanceSnapshot) InstanceSnapshot {
	snapshot.Instances = cloneInstances(snapshot.Instances)
	return snapshot
}

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
