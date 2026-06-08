# AegisMesh

AegisMesh is an adaptive RPC governance system for microservice slow-fault scenarios. This first milestone implements the runnable foundation:

1. demo microservice system
2. Controller + in-memory Registry
3. Go gRPC SDK
4. basic load balancing through gRPC round_robin
5. SDK and Controller Prometheus metrics
6. SDK-side EWMA latency windows
7. Controller-side slow_score calculation
8. endpoint state machine: HEALTHY / DEGRADED / EJECTED / PROBING
9. adaptive P2C gRPC load balancer
10. retry budget for bounded retries
11. endpoint circuit breaker
12. network / CPU / application-level fault injector
13. Mesh verifier for traffic-rule and trace validation
14. Prometheus scrape config and Grafana dashboard
15. eBPF telemetry interface with TCP event aggregation
16. DeathStarBench Social Network integration plan
17. file-backed persistent Registry and PolicyService YAML snapshots

## Layout

```text
api/proto/                 gRPC and protobuf contracts
cmd/controller/            Aegis Controller registry server
cmd/demo-user/             demo user-service instance
cmd/demo-order/            demo order-service instance
cmd/demo-frontend/         HTTP frontend that calls downstream services through the SDK
cmd/agent/                 Linux eBPF telemetry agent
cmd/fault-injector/        fault injection helper for Docker/tc experiments
cmd/verifier/              Mesh verifier CLI for config and trace checks
cmd/deathstarbench-adapter/ DeathStarBench integration plan generator
agent/ebpf/                eBPF telemetry interface and TCP event aggregation
demo/shop/services/        demo gRPC service implementations
demo/shop/runtime/         registration and heartbeat helper
dashboard/grafana/         importable Grafana dashboard
dashboard/prometheus/      local Prometheus scrape config
experiments/verifier/      verifier examples
experiments/traces/        runtime SDK trace JSONL output for real verifier runs
experiments/deathstarbench/ DeathStarBench adapter config
pkg/controller/            RegistryService implementation
pkg/circuitbreaker/        endpoint in-flight circuit breaker
pkg/fault/                 slow_score, endpoint state machine, health metrics
pkg/faultinjector/         tc netem and Docker fault command builders
pkg/deathstarbench/        DeathStarBench integration planning
pkg/verifier/              MeshTest-style verifier parser and oracle
pkg/registry/              in-memory service registry with TTL leases
pkg/policy/                YAML-backed dynamic policy snapshots for PolicyService
pkg/retry/                 retry budget window accounting
pkg/routing/               basic routing primitives
pkg/telemetry/             EWMA, SDK metrics recorder, Prometheus RPC metrics
sdk/go/aegisgrpc/          Go gRPC SDK resolver and DialService entrypoint
```

## Verify

```powershell
go test ./...
```

Regenerate protobuf code after editing `.proto` files:

```powershell
protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/proto/aegis/v1/registry.proto api/proto/aegis/v1/telemetry.proto api/proto/demo/shop/v1/shop.proto
```

## Run The Demo

One-command Docker path:

```bash
make demo-up
```

With Prometheus and Grafana:

```bash
make dashboard
```

Inject a slow network fault into `user-b`:

```bash
make inject-delay TARGET=aegis-user-b DELAY=200ms JITTER=50ms
make reset-faults TARGET=aegis-user-b
```

Run the benchmark scaffolding and generate local reports:

```bash
make bench
make report
```

The reproducible evaluation plan and CSV schemas live in `docs/evaluation.md` and `experiments/results/`. Checked-in schema files are not benchmark results.
The full comparison matrix and operator guide live in `docs/experiments.md`.
The final project report and resume-ready project wording live in `docs/project_report.md` and `docs/resume.md`.

Manual local path:

Start each command in a separate terminal:

```powershell
go run ./cmd/controller --addr 127.0.0.1:9000
```

The controller also exposes Prometheus metrics by default:

```text
http://127.0.0.1:9100/metrics
```

```powershell
go run ./cmd/demo-user --addr 127.0.0.1:7001 --controller 127.0.0.1:9000 --instance user-a --version v1
```

```powershell
go run ./cmd/demo-user --addr 127.0.0.1:7002 --controller 127.0.0.1:9000 --instance user-b --version v2
```

```powershell
go run ./cmd/demo-order --addr 127.0.0.1:7101 --controller 127.0.0.1:9000 --instance order-a --version v1
```

```powershell
go run ./cmd/demo-frontend --addr 127.0.0.1:8080 --controller 127.0.0.1:9000
```

The frontend exposes SDK-side Prometheus metrics on the same HTTP server:

```text
http://127.0.0.1:8080/metrics
```

Call the frontend:

```powershell
Invoke-RestMethod 'http://127.0.0.1:8080/checkout?user_id=u-42&items=sku-9,sku-10'
```

With two `user-service` instances running, repeated requests should alternate between `version: v1` and `version: v2`, showing that service discovery and basic load balancing are active.

Application-level slow calls and errors can be injected directly into demo services:

```powershell
go run ./cmd/demo-user --addr 127.0.0.1:7002 --controller 127.0.0.1:9000 --instance user-b --version v2 --slow-probability 1 --slow-duration 250ms
```

```powershell
go run ./cmd/demo-order --addr 127.0.0.1:7101 --controller 127.0.0.1:9000 --instance order-a --error-probability 0.2
```

## Metrics And Health

The Go SDK records per-upstream RPC observations and exports:

```text
aegis_rpc_requests_total{source,destination,method,upstream,status}
aegis_rpc_latency_seconds_bucket{source,destination,method,upstream,status}
aegis_endpoint_inflight{source,destination,method,upstream}
aegis_endpoint_latency_ewma_seconds{source,destination,method,upstream}
```

Every SDK dial starts a telemetry reporter that periodically sends endpoint windows to the Controller. The Controller resolves endpoint addresses back to registered instance IDs, computes `slow_score`, advances the endpoint state machine, and exports:

```text
aegis_endpoint_slow_score{service,instance,endpoint}
aegis_endpoint_state{service,instance,endpoint,state}
```

`RegistryService.ListInstances` overlays the Controller health state onto discovered instances. The SDK resolver keeps `HEALTHY`, `DEGRADED`, and `PROBING` endpoints routable, and removes `EJECTED` or `DEAD` endpoints from the gRPC address list.

The slow_score latency component can combine relative peer-outlier scoring with an optional absolute p95 latency SLO:

```bash
go run ./cmd/controller --health-latency-slo 150ms
```

When this is enabled, latency scoring uses `max(relative_median_mad_score, p95_latency / latency_slo)`, so a one-instance service or an all-slow service can still be marked slow instead of hiding behind a service-wide relative median.

## Persistent Registry And PolicyService

The Controller defaults to the in-memory registry for fast local demos. For restart recovery of registered instances, use the file-backed registry:

```bash
go run ./cmd/controller \
  --registry-backend file \
  --registry-file data/aegis-registry.json
```

The file backend persists registered instances and lease expiry timestamps to a JSON snapshot. On restart, unexpired instances are restored and expired instances remain hidden.

Controller-side dynamic policy snapshots can be loaded from YAML and served through `PolicyService.GetPolicy` / `PolicyService.WatchPolicy`:

```bash
go run ./cmd/controller \
  --policy-file experiments/policy/demo-policy.yaml \
  --policy-reload-interval 3s
```

The experiment compose stack mounts `experiments/policy/demo-policy.yaml` into the controller and enables `PolicyService` by default. SDK clients call `GetPolicy` during dial, then keep a `WatchPolicy` stream open. The initial snapshot selects the routing policy, and live snapshots are applied to service-level retry settings, retry budget windows, per-method timeout, and idempotency-aware retry behavior. For example, `GetUser` can remain retryable while `CreateOrder` is configured as non-idempotent and forced to a single attempt.

## Routing, Retry, And Breakers

The SDK now defaults to the `aegis_adaptive_p2c` gRPC balancer. For each request it picks two ready endpoints and chooses the lower-cost endpoint:

```text
cost(endpoint) =
  inflight / effective_weight
  + latency_penalty * latency_ewma
  + slow_penalty * slow_score

effective_weight = base_weight / (1 + slow_score)
```

The balancer reads `status` and `slow_score` from Registry resolver attributes, keeps local in-flight/EWMA state from completed calls, and applies a per-endpoint circuit breaker with a default in-flight cap of `128`. `PROBING` endpoints are not treated as normal traffic candidates while healthy or degraded endpoints exist; adaptive P2C admits them only through a small probe ratio, defaulting to `2%`, so recovery checks do not immediately restore full load to a recently ejected instance.

Unary SDK calls also use a bounded retry policy:

```text
max_attempts: 2
retryable_codes: UNAVAILABLE, DEADLINE_EXCEEDED
retry_budget: max(10, 0.15 * original_requests) per 10s window
per_try_timeout: 750ms
```

Retries are budgeted per SDK `ClientConn`, so slow faults cannot trigger unbounded retry amplification.

## Fault Injector

The CLI prints commands by default and only executes them with `--execute`.

Network delay:

```powershell
go run ./cmd/fault-injector --kind delay --container user-v2 --delay 200ms --jitter 50ms
```

Packet loss:

```powershell
go run ./cmd/fault-injector --kind loss --container user-v2 --loss-percent 2
```

CPU throttling:

```powershell
go run ./cmd/fault-injector --kind cpu --container user-v2 --cpus 0.25
```

Reset `tc` qdisc:

```powershell
go run ./cmd/fault-injector --kind reset --container user-v2
```

## Verifier

The verifier checks whether observed traces match an expected traffic policy. It validates route distribution, retry attempts, and forbidden service-call edges.

```powershell
go run ./cmd/verifier --spec experiments/verifier/canary-user-service.yaml --traces experiments/verifier/sample-traces.jsonl
```

Trace JSONL records use this shape:

```json
{"trace_id":"trace-1","route":"user-service:v1","path":["frontend","user-service:v1"],"retry_attempts":0,"status":"OK"}
```

The sample trace file uses a 9/1 split and should pass the 90/10 canary check. Real runs should feed traces collected from SDK metadata or request logs.

For a real SDK trace smoke run, `frontend-adaptive` in the experiment compose writes JSONL traces to `experiments/traces/frontend-adaptive.jsonl`:

```bash
rm -f experiments/traces/frontend-adaptive.jsonl
make experiments-up
curl 'http://127.0.0.1:8083/checkout'
go run ./cmd/verifier --spec experiments/verifier/real-trace-smoke.yaml --traces experiments/traces/frontend-adaptive.jsonl
```

Each SDK trace event includes `x-aegis-trace-id`, `x-aegis-span-id`, `x-aegis-attempt`, route, path, upstream, status, and retry-attempt count. This turns the verifier from a sample-only checker into a verifier that can validate real SDK runtime traces.

## Dashboard

Prometheus scrape config:

```text
dashboard/prometheus/prometheus.yml
```

Grafana import file:

```text
dashboard/grafana/aegismesh-overview.json
```

The dashboard includes panels for RPC throughput, p99 latency, EWMA latency, endpoint slow score, endpoint state, and in-flight RPCs.

## eBPF Telemetry

`agent/ebpf` defines the TCP telemetry interface and a user-space aggregator that maps network events to Aegis endpoint samples. On non-Linux hosts the collector returns `ErrUnsupportedPlatform` but the aggregation and Controller telemetry conversion remain testable. On Linux, `cmd/agent` loads the compiled BPF object, attaches kprobes, reads the `events` ringbuf, and reports samples to the Controller.

The BPF program lives under:

```text
agent/ebpf/bpf/tcp_metrics.bpf.c
```

Current eBPF signals wired into telemetry:

```text
tcp_retransmit
connect_error
connect_latency
```

`tcp_retransmit` and `connect_error` contribute to the Controller's network score even when the sample comes from network telemetry rather than an RPC window.

Build the BPF object on a Linux host with `clang` and `bpftool`:

```bash
make -C agent/ebpf/bpf
```

Run the agent with endpoint mappings:

```bash
sudo go run ./cmd/agent \
  --controller 127.0.0.1:9000 \
  --object agent/ebpf/bpf/tcp_metrics.bpf.o \
  --endpoint-map "10.0.0.2:7001=user-service/user-a,10.0.0.3:7002=user-service/user-b"
```

See `agent/ebpf/README.md` for the Linux validation checklist.

## DeathStarBench

AegisMesh includes a Social Network adapter config and plan generator:

```powershell
go run ./cmd/deathstarbench-adapter --config experiments/deathstarbench/social-network.yaml
```

The generated plan includes the Docker Compose command, Aegis controller environment, service mapping, and workload command. This keeps the benchmark integration explicit without cloning or mutating the external DeathStarBench repo from this workspace.
