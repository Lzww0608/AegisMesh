package retry

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegismesh/aegismesh/pkg/align"
)

// BudgetConfig controls retry admission for one rolling time window.
type BudgetConfig struct {
	BudgetRatio float64
	MinBudget   int64
	Window      time.Duration
	Now         func() time.Time
}

// Budget tracks original and retry attempts with low-contention atomic counters.
type Budget struct {
	mu          sync.RWMutex
	cfg         BudgetConfig
	windowStart time.Time
	counters    budgetCounters
}

// budgetCounters keeps hot counters on separate cache lines to reduce false sharing.
type budgetCounters struct {
	originalRequests atomic.Int64
	retryRequests    atomic.Int64
	_                align.Pad48
}

// NewBudget initializes budget with package defaults for this package's call path.
func NewBudget(cfg BudgetConfig) *Budget {
	if cfg.BudgetRatio < 0 || math.IsNaN(cfg.BudgetRatio) {
		cfg.BudgetRatio = 0
	}
	if cfg.MinBudget < 0 {
		cfg.MinBudget = 0
	}
	if cfg.Window <= 0 {
		cfg.Window = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Budget{
		cfg:         cfg,
		windowStart: cfg.Now(),
	}
}

// RecordOriginal counts one first-attempt request in the current window.
func (b *Budget) RecordOriginal() {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	b.counters.originalRequests.Add(1)
}

// AllowRetry reports whether another retry fits the current budget without reserving it.
func (b *Budget) AllowRetry() bool {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	return b.counters.retryRequests.Load() < b.allowedRetries()
}

// TryAcquireRetry attempts to reserve capacity without blocking the caller.
func (b *Budget) TryAcquireRetry() bool {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	allowed := b.allowedRetries()
	if allowed <= 0 {
		return false
	}
	retries := b.counters.retryRequests.Add(1)
	if retries <= allowed {
		return true
	}
	// Roll back the optimistic increment so failed admissions do not consume budget.
	b.counters.retryRequests.Add(-1)
	return false
}

// RecordRetry counts an already-issued retry in the current window.
func (b *Budget) RecordRetry() {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	b.counters.retryRequests.Add(1)
}

// Snapshot returns the current counters and allowed retry budget for observability.
func (b *Budget) Snapshot() Snapshot {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	originalRequests := b.counters.originalRequests.Load()
	return Snapshot{
		OriginalRequests: originalRequests,
		RetryRequests:    b.counters.retryRequests.Load(),
		AllowedRetries:   b.allowedRetriesFor(originalRequests),
		WindowStart:      b.windowStart,
		WindowEnd:        b.cfg.Now(),
	}
}

// lockCurrentWindow resets counters when the rolling window expires and leaves an RLock held.
func (b *Budget) lockCurrentWindow() {
	now := b.cfg.Now()
	b.mu.RLock()
	if now.Sub(b.windowStart) < b.cfg.Window {
		return
	}
	b.mu.RUnlock()

	// Upgrade from read to write lock only on rollover; the second time check prevents double resets.
	b.mu.Lock()
	now = b.cfg.Now()
	if now.Sub(b.windowStart) >= b.cfg.Window {
		b.windowStart = now
		b.counters.originalRequests.Store(0)
		b.counters.retryRequests.Store(0)
	}
	b.mu.Unlock()

	b.mu.RLock()
}

// allowedRetries calculates retry capacity from the current original request count.
func (b *Budget) allowedRetries() int64 {
	return b.allowedRetriesFor(b.counters.originalRequests.Load())
}

// allowedRetriesFor applies the ratio budget and the configured minimum floor.
func (b *Budget) allowedRetriesFor(originalRequests int64) int64 {
	ratioBudget := int64(math.Floor(float64(originalRequests)*b.cfg.BudgetRatio + 1e-9))
	if ratioBudget < b.cfg.MinBudget {
		return b.cfg.MinBudget
	}
	return ratioBudget
}

// Snapshot is a point-in-time retry-budget accounting view.
type Snapshot struct {
	OriginalRequests int64
	RetryRequests    int64
	AllowedRetries   int64
	WindowStart      time.Time
	WindowEnd        time.Time
}
