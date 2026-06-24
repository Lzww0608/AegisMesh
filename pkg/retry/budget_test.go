package retry

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBudgetAllowsRetryWithinRatioAndMinBudget(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 0.15,
		MinBudget:   2,
		Window:      10 * time.Second,
		Now:         func() time.Time { return now },
	})

	for i := 0; i < 10; i++ {
		budget.RecordOriginal()
	}

	if !budget.TryAcquireRetry() {
		t.Fatalf("expected first retry to be allowed")
	}
	if !budget.TryAcquireRetry() {
		t.Fatalf("expected second retry to be allowed by min budget")
	}
	if budget.TryAcquireRetry() {
		t.Fatalf("expected retry budget to be exhausted")
	}
}

func TestBudgetResetsAfterWindow(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 0.1,
		MinBudget:   1,
		Window:      10 * time.Second,
		Now:         func() time.Time { return now },
	})

	budget.RecordOriginal()
	if !budget.TryAcquireRetry() {
		t.Fatalf("expected first retry to be allowed")
	}
	if budget.TryAcquireRetry() {
		t.Fatalf("expected retry budget exhausted before window reset")
	}

	now = now.Add(11 * time.Second)
	budget.RecordOriginal()
	if !budget.TryAcquireRetry() {
		t.Fatalf("expected retry budget to reset after window")
	}
}

func TestBudgetTryAcquireRetryDoesNotOversubscribeConcurrent(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 0.15,
		MinBudget:   0,
		Window:      time.Minute,
		Now:         func() time.Time { return now },
	})
	for i := 0; i < 100; i++ {
		budget.RecordOriginal()
	}

	var successes atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if budget.TryAcquireRetry() {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != 15 {
		t.Fatalf("expected exactly 15 retry acquisitions, got %d", got)
	}
	snapshot := budget.Snapshot()
	if snapshot.RetryRequests != 15 || snapshot.AllowedRetries != 15 {
		t.Fatalf("expected snapshot retry/allowed counts to be 15, got %+v", snapshot)
	}
}
