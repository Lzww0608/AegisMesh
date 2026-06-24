package retry

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

type BudgetConfig struct {
	BudgetRatio float64
	MinBudget   int64
	Window      time.Duration
	Now         func() time.Time
}

type Budget struct {
	mu               sync.RWMutex
	cfg              BudgetConfig
	windowStart      time.Time
	originalRequests atomic.Int64
	retryRequests    atomic.Int64
}

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

func (b *Budget) RecordOriginal() {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	b.originalRequests.Add(1)
}

func (b *Budget) AllowRetry() bool {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	return b.retryRequests.Load() < b.allowedRetries()
}

func (b *Budget) TryAcquireRetry() bool {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	allowed := b.allowedRetries()
	if allowed <= 0 {
		return false
	}
	retries := b.retryRequests.Add(1)
	if retries <= allowed {
		return true
	}
	b.retryRequests.Add(-1)
	return false
}

func (b *Budget) RecordRetry() {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	b.retryRequests.Add(1)
}

func (b *Budget) Snapshot() Snapshot {
	b.lockCurrentWindow()
	defer b.mu.RUnlock()
	originalRequests := b.originalRequests.Load()
	return Snapshot{
		OriginalRequests: originalRequests,
		RetryRequests:    b.retryRequests.Load(),
		AllowedRetries:   b.allowedRetriesFor(originalRequests),
		WindowStart:      b.windowStart,
		WindowEnd:        b.cfg.Now(),
	}
}

func (b *Budget) lockCurrentWindow() {
	now := b.cfg.Now()
	b.mu.RLock()
	if now.Sub(b.windowStart) < b.cfg.Window {
		return
	}
	b.mu.RUnlock()

	b.mu.Lock()
	now = b.cfg.Now()
	if now.Sub(b.windowStart) >= b.cfg.Window {
		b.windowStart = now
		b.originalRequests.Store(0)
		b.retryRequests.Store(0)
	}
	b.mu.Unlock()

	b.mu.RLock()
}

func (b *Budget) allowedRetries() int64 {
	return b.allowedRetriesFor(b.originalRequests.Load())
}

func (b *Budget) allowedRetriesFor(originalRequests int64) int64 {
	ratioBudget := int64(math.Floor(float64(originalRequests)*b.cfg.BudgetRatio + 1e-9))
	if ratioBudget < b.cfg.MinBudget {
		return b.cfg.MinBudget
	}
	return ratioBudget
}

type Snapshot struct {
	OriginalRequests int64
	RetryRequests    int64
	AllowedRetries   int64
	WindowStart      time.Time
	WindowEnd        time.Time
}
