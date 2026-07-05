package registry

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

var benchmarkRegistryInstances []Instance
var benchmarkRegistrySnapshot InstanceSnapshot

// BenchmarkMemoryRegistryRegister reports latency and allocation cost for memory registry register.
func BenchmarkMemoryRegistryRegister(b *testing.B) {
	for _, c := range registryBenchmarkCases() {
		b.Run(c.name(), func(b *testing.B) {
			now := fixedRegistryBenchmarkTime()
			registry := NewMemoryRegistry(func() time.Time { return now })
			instances := makeBenchmarkInstances(c.services, c.instancesPerService)
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				inst := instances[i%len(instances)]
				if err := registry.Register(ctx, inst, time.Minute); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMemoryRegistryHeartbeat reports latency and allocation cost for memory registry heartbeat.
func BenchmarkMemoryRegistryHeartbeat(b *testing.B) {
	for _, c := range registryBenchmarkCases() {
		b.Run(c.name(), func(b *testing.B) {
			now := fixedRegistryBenchmarkTime()
			registry := NewMemoryRegistry(func() time.Time { return now })
			instances := makeBenchmarkInstances(c.services, c.instancesPerService)
			ctx := context.Background()
			registerBenchmarkInstances(b, registry, instances, time.Minute)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				inst := instances[i%len(instances)]
				if err := registry.Heartbeat(ctx, inst.Service, inst.ID, time.Minute); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMemoryRegistryList reports latency and allocation cost for memory registry list.
func BenchmarkMemoryRegistryList(b *testing.B) {
	for _, c := range registryBenchmarkCases() {
		b.Run(c.name(), func(b *testing.B) {
			now := fixedRegistryBenchmarkTime()
			registry := NewMemoryRegistry(func() time.Time { return now })
			instances := makeBenchmarkInstances(c.services, c.instancesPerService)
			services := makeBenchmarkServices(c.services)
			ctx := context.Background()
			registerBenchmarkInstances(b, registry, instances, time.Minute)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				benchmarkRegistryInstances, err = registry.List(ctx, services[i%len(services)])
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMemoryRegistrySnapshot reports latency and allocation cost for memory registry snapshot.
func BenchmarkMemoryRegistrySnapshot(b *testing.B) {
	for _, c := range registryBenchmarkCases() {
		b.Run(c.name(), func(b *testing.B) {
			now := fixedRegistryBenchmarkTime()
			registry := NewMemoryRegistry(func() time.Time { return now })
			instances := makeBenchmarkInstances(c.services, c.instancesPerService)
			services := makeBenchmarkServices(c.services)
			ctx := context.Background()
			registerBenchmarkInstances(b, registry, instances, time.Minute)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				benchmarkRegistrySnapshot, err = registry.Snapshot(ctx, services[i%len(services)])
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkFileRegistryHeartbeat reports latency and allocation cost for file registry heartbeat.
func BenchmarkFileRegistryHeartbeat(b *testing.B) {
	instances := makeBenchmarkInstances(100, 10)
	ctx := context.Background()
	cases := []struct {
		name string
		open func(b *testing.B, path string, now func() time.Time) Registry
	}{
		{
			name: "json_snapshot",
			open: func(b *testing.B, path string, now func() time.Time) Registry {
				b.Helper()
				registry, err := NewFileRegistry(path, now)
				if err != nil {
					b.Fatal(err)
				}
				return registry
			},
		},
		{
			name: "wal_batch",
			open: func(b *testing.B, path string, now func() time.Time) Registry {
				b.Helper()
				registry, err := NewFileRegistryV2(
					path,
					now,
					WithFileRegistryV2SyncMode(FileRegistrySyncBatch),
					WithFileRegistryV2GroupCommit(1<<30, 1<<30, time.Hour),
					WithFileRegistryV2CompactBytes(0),
				)
				if err != nil {
					b.Fatal(err)
				}
				return registry
			},
		},
		{
			name: "wal_always",
			open: func(b *testing.B, path string, now func() time.Time) Registry {
				b.Helper()
				registry, err := NewFileRegistryV2(
					path,
					now,
					WithFileRegistryV2SyncMode(FileRegistrySyncAlways),
					WithFileRegistryV2CompactBytes(0),
				)
				if err != nil {
					b.Fatal(err)
				}
				return registry
			},
		},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			now := fixedRegistryBenchmarkTime()
			registry := c.open(b, filepath.Join(b.TempDir(), "registry.json"), func() time.Time { return now })
			registerBenchmarkFileInstances(b, registry, instances, time.Hour)
			latencies := make([]int64, 0, min(b.N, 100000))

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				inst := instances[i%len(instances)]
				started := time.Now()
				if err := registry.Heartbeat(ctx, inst.Service, inst.ID, time.Hour); err != nil {
					b.Fatal(err)
				}
				if len(latencies) < cap(latencies) {
					latencies = append(latencies, time.Since(started).Nanoseconds())
				}
			}
			b.StopTimer()
			reportRegistryP99Latency(b, latencies)
			if closer, ok := registry.(interface{ Close() error }); ok {
				if err := closer.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMemoryRegistrySweepExpired reports latency and allocation cost for memory registry sweep expired.
func BenchmarkMemoryRegistrySweepExpired(b *testing.B) {
	for _, c := range registryBenchmarkCases() {
		b.Run(c.name(), func(b *testing.B) {
			ctx := context.Background()
			instances := makeBenchmarkInstances(c.services, c.instancesPerService)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				now := fixedRegistryBenchmarkTime()
				registry := NewMemoryRegistry(func() time.Time { return now })
				registerBenchmarkInstances(b, registry, instances, time.Second)
				now = now.Add(2 * time.Second)
				b.StartTimer()
				expired := registry.SweepExpired(ctx)
				b.StopTimer()
				if expired != len(instances) {
					b.Fatalf("expected %d expired instances, got %d", len(instances), expired)
				}
				b.StartTimer()
			}
		})
	}
}

// registryBenchmarkCase carries registry benchmark case state for registry persistence and watch paths.
type registryBenchmarkCase struct {
	services            int
	instancesPerService int
}

// registryBenchmarkCases provides the shared registry benchmark cases helper for registry persistence and watch paths.
func registryBenchmarkCases() []registryBenchmarkCase {
	return []registryBenchmarkCase{
		{services: 1, instancesPerService: 1},
		{services: 8, instancesPerService: 16},
		{services: 64, instancesPerService: 64},
		{services: 100, instancesPerService: 10},
	}
}

// name returns name data for registryBenchmarkCase callers without handing out mutable receiver state.
func (c registryBenchmarkCase) name() string {
	return fmt.Sprintf("services=%d/instances_per_service=%d", c.services, c.instancesPerService)
}

// makeBenchmarkServices provides the shared make benchmark services helper for registry persistence and watch paths.
func makeBenchmarkServices(count int) []string {
	services := make([]string, count)
	for i := range services {
		services[i] = fmt.Sprintf("service-%02d", i)
	}
	return services
}

// makeBenchmarkInstances provides the shared make benchmark instances helper for registry persistence and watch paths.
func makeBenchmarkInstances(services, instancesPerService int) []Instance {
	names := makeBenchmarkServices(services)
	instances := make([]Instance, 0, services*instancesPerService)
	for serviceIndex, service := range names {
		for instanceIndex := 0; instanceIndex < instancesPerService; instanceIndex++ {
			instances = append(instances, Instance{
				ID:      fmt.Sprintf("%s-%03d", service, instanceIndex),
				Service: service,
				Address: fmt.Sprintf("10.%d.%d.%d:7001",
					serviceIndex/256,
					serviceIndex%256,
					instanceIndex+1,
				),
				Status: InstanceHealthy,
				Labels: map[string]string{"variant": "primary"},
			})
		}
	}
	return instances
}

// registerBenchmarkInstances registers register benchmark instances with the controller or local registry.
func registerBenchmarkInstances(b *testing.B, registry *MemoryRegistry, instances []Instance, ttl time.Duration) {
	b.Helper()
	ctx := context.Background()
	for _, inst := range instances {
		if err := registry.Register(ctx, inst, ttl); err != nil {
			b.Fatal(err)
		}
	}
}

// fixedRegistryBenchmarkTime provides the shared fixed registry benchmark time helper for registry persistence and watch paths.
func fixedRegistryBenchmarkTime() time.Time {
	return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
}

// registerBenchmarkFileInstances registers register benchmark file instances with the controller or local registry.
func registerBenchmarkFileInstances(b *testing.B, registry Registry, instances []Instance, ttl time.Duration) {
	b.Helper()
	ctx := context.Background()
	for _, inst := range instances {
		if err := registry.Register(ctx, inst, ttl); err != nil {
			b.Fatal(err)
		}
	}
}

// reportRegistryP99Latency provides the shared report registry p99 latency helper for registry persistence and watch paths.
func reportRegistryP99Latency(b *testing.B, latencies []int64) {
	b.Helper()
	if len(latencies) == 0 {
		return
	}
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})
	index := (len(latencies)*99 + 99) / 100
	if index > 0 {
		index--
	}
	if index >= len(latencies) {
		index = len(latencies) - 1
	}
	b.ReportMetric(float64(latencies[index]), "p99-ns/op")
}
