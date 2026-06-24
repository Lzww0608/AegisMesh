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

type FileRegistry struct {
	memory *MemoryRegistry
	path   string
}

type fileRegistrySnapshot struct {
	Records []fileRegistryRecord `json:"records"`
}

type fileRegistryRecord struct {
	Instance  Instance  `json:"instance"`
	ExpiresAt time.Time `json:"expires_at"`
}

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

func (r *FileRegistry) PersistencePath() string {
	return r.path
}

func (r *FileRegistry) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	if err := r.memory.Register(ctx, inst, ttl); err != nil {
		return err
	}
	return r.persist()
}

func (r *FileRegistry) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	if err := r.memory.Heartbeat(ctx, service, id, ttl); err != nil {
		return err
	}
	return r.persist()
}

func (r *FileRegistry) List(ctx context.Context, service string) ([]Instance, error) {
	return r.memory.List(ctx, service)
}

func (r *FileRegistry) Snapshot(ctx context.Context, service string) (InstanceSnapshot, error) {
	return r.memory.Snapshot(ctx, service)
}

func (r *FileRegistry) Watch(ctx context.Context, service string, afterVersion int64) (<-chan InstanceSnapshot, error) {
	return r.memory.Watch(ctx, service, afterVersion)
}

func (r *FileRegistry) SweepExpired(ctx context.Context) int {
	expired := r.memory.SweepExpired(ctx)
	if expired > 0 {
		_ = r.persist()
	}
	return expired
}

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
