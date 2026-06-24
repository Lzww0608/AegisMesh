package retry

import (
	"testing"
	"time"
)

func BenchmarkBudgetRecordOriginalParallel(b *testing.B) {
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 0.15,
		MinBudget:   10,
		Window:      time.Minute,
		Now:         fixedBenchmarkTime,
	})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			budget.RecordOriginal()
		}
	})
}

func BenchmarkBudgetTryAcquireRetryParallel(b *testing.B) {
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 1,
		MinBudget:   1 << 60,
		Window:      time.Minute,
		Now:         fixedBenchmarkTime,
	})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !budget.TryAcquireRetry() {
				panic("retry budget unexpectedly exhausted")
			}
		}
	})
}

func BenchmarkBudgetAllowThenRecordRetryParallel(b *testing.B) {
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 1,
		MinBudget:   1 << 60,
		Window:      time.Minute,
		Now:         fixedBenchmarkTime,
	})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !benchmarkAllowThenRecordRetry(budget) {
				panic("retry budget unexpectedly exhausted")
			}
		}
	})
}

func BenchmarkBudgetWindowRollover(b *testing.B) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	budget := NewBudget(BudgetConfig{
		BudgetRatio: 0.15,
		MinBudget:   10,
		Window:      10 * time.Second,
		Now:         func() time.Time { return now },
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(11 * time.Second)
		budget.RecordOriginal()
	}
}

func benchmarkAllowThenRecordRetry(budget *Budget) bool {
	if !budget.AllowRetry() {
		return false
	}
	budget.RecordRetry()
	return true
}

func fixedBenchmarkTime() time.Time {
	return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
}
