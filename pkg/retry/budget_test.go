package retry

import (
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

	if !budget.AllowRetry() {
		t.Fatalf("expected first retry to be allowed")
	}
	budget.RecordRetry()
	if !budget.AllowRetry() {
		t.Fatalf("expected second retry to be allowed by min budget")
	}
	budget.RecordRetry()
	if budget.AllowRetry() {
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
	budget.RecordRetry()
	if budget.AllowRetry() {
		t.Fatalf("expected retry budget exhausted before window reset")
	}

	now = now.Add(11 * time.Second)
	budget.RecordOriginal()
	if !budget.AllowRetry() {
		t.Fatalf("expected retry budget to reset after window")
	}
}
