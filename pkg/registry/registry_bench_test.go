package registry

import (
	"context"
	"fmt"
	"testing"
	"time"
)

var benchmarkRegistryInstances []Instance

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

type registryBenchmarkCase struct {
	services            int
	instancesPerService int
}

func registryBenchmarkCases() []registryBenchmarkCase {
	return []registryBenchmarkCase{
		{services: 1, instancesPerService: 1},
		{services: 8, instancesPerService: 16},
		{services: 64, instancesPerService: 64},
	}
}

func (c registryBenchmarkCase) name() string {
	return fmt.Sprintf("services=%d/instances_per_service=%d", c.services, c.instancesPerService)
}

func makeBenchmarkServices(count int) []string {
	services := make([]string, count)
	for i := range services {
		services[i] = fmt.Sprintf("service-%02d", i)
	}
	return services
}

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

func registerBenchmarkInstances(b *testing.B, registry *MemoryRegistry, instances []Instance, ttl time.Duration) {
	b.Helper()
	ctx := context.Background()
	for _, inst := range instances {
		if err := registry.Register(ctx, inst, ttl); err != nil {
			b.Fatal(err)
		}
	}
}

func fixedRegistryBenchmarkTime() time.Time {
	return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
}
