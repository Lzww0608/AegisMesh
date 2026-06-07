package registry

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type InstanceStatus string

const (
	InstanceHealthy  InstanceStatus = "HEALTHY"
	InstanceDegraded InstanceStatus = "DEGRADED"
	InstanceEjected  InstanceStatus = "EJECTED"
	InstanceProbing  InstanceStatus = "PROBING"
	InstanceDead     InstanceStatus = "DEAD"
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

type MemoryRegistry struct {
	mu    sync.RWMutex
	now   func() time.Time
	items map[string]map[string]record
}

type record struct {
	instance  Instance
	expiresAt time.Time
}

func NewMemoryRegistry(now func() time.Time) *MemoryRegistry {
	if now == nil {
		now = time.Now
	}
	return &MemoryRegistry{
		now:   now,
		items: make(map[string]map[string]record),
	}
}

func (r *MemoryRegistry) Register(ctx context.Context, inst Instance, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if inst.ID == "" || inst.Service == "" || inst.Address == "" {
		return ErrInvalidInstance
	}
	if ttl <= 0 {
		return ErrInvalidInstance
	}
	if inst.Status == "" {
		inst.Status = InstanceHealthy
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	serviceInstances := r.items[inst.Service]
	if serviceInstances == nil {
		serviceInstances = make(map[string]record)
		r.items[inst.Service] = serviceInstances
	}

	inst.LastSeen = r.now()
	inst.Labels = cloneLabels(inst.Labels)
	serviceInstances[inst.ID] = record{
		instance:  inst,
		expiresAt: inst.LastSeen.Add(ttl),
	}
	return nil
}

func (r *MemoryRegistry) Heartbeat(ctx context.Context, service, id string, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == "" || id == "" || ttl <= 0 {
		return ErrInvalidInstance
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	serviceInstances := r.items[service]
	if serviceInstances == nil {
		return ErrInstanceNotFound
	}
	rec, ok := serviceInstances[id]
	if !ok {
		return ErrInstanceNotFound
	}

	rec.instance.LastSeen = r.now()
	rec.expiresAt = rec.instance.LastSeen.Add(ttl)
	serviceInstances[id] = rec
	return nil
}

func (r *MemoryRegistry) List(ctx context.Context, service string) ([]Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if service == "" {
		return nil, ErrInvalidInstance
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	now := r.now()
	serviceInstances := r.items[service]
	out := make([]Instance, 0, len(serviceInstances))
	for _, rec := range serviceInstances {
		if !rec.expiresAt.After(now) {
			continue
		}
		inst := rec.instance
		inst.Labels = cloneLabels(inst.Labels)
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r *MemoryRegistry) SweepExpired(ctx context.Context) int {
	if err := ctx.Err(); err != nil {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	expired := 0
	for service, serviceInstances := range r.items {
		for id, rec := range serviceInstances {
			if rec.expiresAt.After(now) {
				continue
			}
			delete(serviceInstances, id)
			expired++
		}
		if len(serviceInstances) == 0 {
			delete(r.items, service)
		}
	}
	return expired
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
