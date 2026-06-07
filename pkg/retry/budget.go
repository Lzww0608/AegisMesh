package retry

import (
	"math"
	"sync"
	"time"
)

type BudgetConfig struct {
	BudgetRatio float64
	MinBudget   int64
	Window      time.Duration
	Now         func() time.Time
}

type Budget struct {
	mu               sync.Mutex
	cfg              BudgetConfig
	windowStart      time.Time
	originalRequests int64
	retryRequests    int64
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
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNeededLocked()
	b.originalRequests++
}

func (b *Budget) AllowRetry() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNeededLocked()
	return b.retryRequests < b.allowedRetriesLocked()
}

func (b *Budget) RecordRetry() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNeededLocked()
	b.retryRequests++
}

func (b *Budget) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNeededLocked()
	return Snapshot{
		OriginalRequests: b.originalRequests,
		RetryRequests:    b.retryRequests,
		AllowedRetries:   b.allowedRetriesLocked(),
		WindowStart:      b.windowStart,
		WindowEnd:        b.cfg.Now(),
	}
}

func (b *Budget) resetIfNeededLocked() {
	now := b.cfg.Now()
	if now.Sub(b.windowStart) < b.cfg.Window {
		return
	}
	b.windowStart = now
	b.originalRequests = 0
	b.retryRequests = 0
}

func (b *Budget) allowedRetriesLocked() int64 {
	ratioBudget := int64(math.Floor(float64(b.originalRequests)*b.cfg.BudgetRatio + 1e-9))
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
