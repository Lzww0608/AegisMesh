package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileRegistry persists registry snapshots to a JSON file while delegating lease semantics to the in-memory registry.
type FileRegistry struct {
	memory *MemoryRegistry
	path   string
}

// fileRegistrySnapshot is an immutable point-in-time view of file registry snapshot state.
type fileRegistrySnapshot struct {
	Records []fileRegistryRecord `json:"records"`
}

// fileRegistryRecord carries file registry record state for registry persistence and watch paths.
type fileRegistryRecord struct {
	Instance  Instance  `json:"instance"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NewFileRegistry initializes file registry with package defaults for this package's call path.
func NewFileRegistry(path string, now func() time.Time) (*FileRegistry, error) {
	if path == "" {
		return nil, ErrInvalidInstance
	}
	reg := &FileRegistry{
		memory: NewMemoryRegistry(now),
		path:   path,
	}
	if err := reg.load(); err != nil {
		return nil, err
	}
	return reg, nil
}

// PersistencePath returns persistence path data for FileRegistry callers without handing out mutable receiver state.
func (r *FileRegistry) PersistencePath() string {
	return r.path
}

// Register registers register with the controller or local registry.
func (r *FileRegistry) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	if err := r.memory.Register(ctx, inst, ttl); err != nil {
		return err
	}
	return r.persist()
}

// Heartbeat refreshes the instance lease using the current registration fence.
func (r *FileRegistry) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	if err := r.memory.Heartbeat(ctx, service, id, ttl); err != nil {
		return err
	}
	return r.persist()
}

// HeartbeatWithEpoch refreshes the instance lease using the current registration fence.
func (r *FileRegistry) HeartbeatWithEpoch(ctx context.Context, service, id, registrationEpoch string, ttl time.Duration) error {
	if err := r.memory.HeartbeatWithEpoch(ctx, service, id, registrationEpoch, ttl); err != nil {
		return err
	}
	return r.persist()
}

// HeartbeatWithOwner refreshes the instance lease using the current registration fence.
func (r *FileRegistry) HeartbeatWithOwner(ctx context.Context, service, id, registrationEpoch, ownerToken string, ttl time.Duration) error {
	if err := r.memory.HeartbeatWithOwner(ctx, service, id, registrationEpoch, ownerToken, ttl); err != nil {
		return err
	}
	return r.persist()
}

// List returns a point-in-time list of list visible to the caller.
func (r *FileRegistry) List(ctx context.Context, service string) ([]Instance, error) {
	return r.memory.List(ctx, service)
}

// Snapshot returns an immutable snapshot of the current snapshot state.
func (r *FileRegistry) Snapshot(ctx context.Context, service string) (InstanceSnapshot, error) {
	return r.memory.Snapshot(ctx, service)
}

// Watch streams backing-source changes to callers until the source or context closes.
func (r *FileRegistry) Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error) {
	return r.memory.Watch(ctx, service, afterVersion)
}

// SweepExpired removes expired sweep expired records from the backing store.
func (r *FileRegistry) SweepExpired(ctx context.Context) int {
	expired := r.memory.SweepExpired(ctx)
	if expired > 0 {
		_ = r.persist()
	}
	return expired
}

// load reads the current state from the configured backing source.
func (r *FileRegistry) load() error {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(raw) == 0 {
		return nil
	}

	var snapshot fileRegistrySnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}

	for _, rec := range snapshot.Records {
		r.memory.restore(rec.Instance, rec.ExpiresAt)
	}
	return nil
}

// persist writes the current in-memory registry snapshot to the JSON backing file.
func (r *FileRegistry) persist() error {
	snapshot := r.snapshot()
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// snapshot returns an immutable snapshot of the current snapshot state.
func (r *FileRegistry) snapshot() fileRegistrySnapshot {
	now := r.memory.now()
	records := make([]fileRegistryRecord, 0)
	r.memory.services.Range(func(_, value any) bool {
		state := value.(*serviceState)
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, rec := range state.records {
			if !rec.expiresAt.After(now) {
				continue
			}
			inst := rec.instance
			inst.Labels = cloneLabels(inst.Labels)
			records = append(records, fileRegistryRecord{Instance: inst, ExpiresAt: rec.expiresAt})
		}
		return true
	})
	sort.Slice(records, func(i, j int) bool {
		if records[i].Instance.Service != records[j].Instance.Service {
			return records[i].Instance.Service < records[j].Instance.Service
		}
		return records[i].Instance.ID < records[j].Instance.ID
	})
	return fileRegistrySnapshot{Records: records}
}
