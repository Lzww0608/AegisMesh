# AegisMesh

AegisMesh is a Go/gRPC RPC governance project for fail-slow microservices. It is built around a simple problem: an instance can keep passing health checks while its p99 latency is already hurting the caller.

The repo has four main parts:

- a small shop demo with user, order, and frontend services
- a Controller with service registry, health state, telemetry, and YAML policy snapshots
- a Go gRPC SDK with resolver, adaptive P2C balancer, retry budget, tracing, and local telemetry
- experiment tooling for slow faults, retry amplification, recovery curves, verifier checks, and Linux eBPF TCP signals

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
protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/proto/aegis/v1/registry.proto api/proto/aegis/v1/telemetry.proto api/proto/aegis/v1/policy.proto api/proto/demo/shop/v1/shop.proto
```

## Run the demo

Docker path:

```bash
make demo-up
```

Prometheus and Grafana:

```bash
make dashboard
```

Inject a slow network fault into `user-b`:

```bash
make inject-delay TARGET=aegis-user-b DELAY=200ms JITTER=50ms
make reset-faults TARGET=aegis-user-b
```

Benchmark and report:

```bash
make bench
make report
```

`docs/evaluation.md` records the measured results and the run environment. `docs/experiments.md` is the runbook. CSV schema files under `experiments/results/` are only column definitions.

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
go run ./cmd/demo-user --addr 127.0.0.1:7001 --controller 127.0.0.1:9000 --instance user-a --variant primary
```

```powershell
go run ./cmd/demo-user --addr 127.0.0.1:7002 --controller 127.0.0.1:9000 --instance user-b --variant secondary
```

```powershell
go run ./cmd/demo-order --addr 127.0.0.1:7101 --controller 127.0.0.1:9000 --instance order-a --variant primary
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

With both `user-service` instances running, repeated requests should hit `primary` and `secondary` in turn. That is enough to check service discovery and basic balancing.

Application-level slow calls and errors can be injected directly into demo services:

```powershell
go run ./cmd/demo-user --addr 127.0.0.1:7002 --controller 127.0.0.1:9000 --instance user-b --variant secondary --slow-probability 1 --slow-duration 250ms
```

```powershell
go run ./cmd/demo-order --addr 127.0.0.1:7101 --controller 127.0.0.1:9000 --instance order-a --error-probability 0.2
```

## Metrics and health

The Go SDK records per-upstream RPC observations and exports:

```text
aegis_rpc_requests_total{source,destination,method,upstream,status}
aegis_rpc_latency_seconds_bucket{source,destination,method,upstream,status}
aegis_endpoint_inflight{source,destination,method,upstream}
aegis_endpoint_latency_ewma_seconds{source,destination,method,upstream}
```

Every SDK dial starts a telemetry reporter. It sends endpoint windows to the Controller, which maps addresses back to registered instances, computes `slow_score`, advances endpoint state, and exports:

```text
aegis_endpoint_slow_score{service,instance,endpoint}
aegis_endpoint_state{service,instance,endpoint,state}
```

`RegistryService.ListInstances` overlays the Controller health state onto discovered instances. The SDK resolver keeps `HEALTHY`, `DEGRADED`, and `PROBING` endpoints routable, and removes `EJECTED` or `DEAD` endpoints from the gRPC address list.

The slow_score latency component can combine relative peer-outlier scoring with an optional absolute p95 latency SLO:

```bash
go run ./cmd/controller --health-latency-slo 150ms
```

With this flag, latency scoring uses `max(relative_median_mad_score, p95_latency / latency_slo)`. That catches cases where a service has only one instance, or every instance is slow at the same time.

## Persistent registry and PolicyService

The Controller uses the in-memory registry by default. For local restart recovery, switch to the file-backed registry:

```bash
go run ./cmd/controller \
  --registry-backend file \
  --registry-file data/aegis-registry.json
```

The file backend writes registered instances and lease expiry timestamps to a JSON snapshot. After a restart, unexpired instances are restored; expired ones are ignored.

For lower heartbeat write amplification, use the WAL-backed file backend. It keeps the JSON snapshot as the compacted recovery point and appends short WAL records to `--registry-file + ".wal"` between compactions:

```bash
go run ./cmd/controller \
  --registry-backend file-v2 \
  --registry-file data/aegis-registry.json
```

Use `--registry-file-v2-sync always` for tests that need every record fsynced before the call returns. The default `batch` mode groups fsyncs by `--registry-file-v2-flush-records`, `--registry-file-v2-flush-bytes`, or `--registry-file-v2-flush-interval`; `--registry-file-v2-compact-bytes` controls snapshot compaction.

Controller-side dynamic policy snapshots can be loaded from YAML and served through `PolicyService.GetPolicy` / `PolicyService.WatchPolicy`:

```bash
go run ./cmd/controller \
  --policy-file experiments/policy/demo-policy.yaml \
  --policy-reload-interval 3s
```

The experiment compose stack mounts `experiments/policy/demo-policy.yaml` into the Controller. SDK clients call `GetPolicy` during dial and then keep a `WatchPolicy` stream open. The first snapshot selects the routing policy; later snapshots can change retry settings, retry budget windows, per-method timeout, and idempotency rules. In the demo policy, `GetUser` can retry, while `CreateOrder` is treated as non-idempotent and kept to one attempt.

## Routing, retry, and breakers

The SDK defaults to the `aegis_adaptive_p2c` gRPC balancer. For each request it samples two ready endpoints and chooses the lower-cost one:

```text
cost(endpoint) =
  inflight / effective_weight
  + latency_penalty * latency_ewma
  + slow_penalty * slow_score

effective_weight = base_weight / (1 + slow_score)
```

The balancer reads `status` and `slow_score` from resolver attributes, then combines them with local in-flight and EWMA state. A per-endpoint breaker caps in-flight calls at `128` by default. `PROBING` endpoints are kept out of normal traffic while healthy or degraded endpoints exist; they only get a small probe share, `2%` by default.

Two focused experiments cover probe traffic and absolute SLO scoring:

```bash
make bench-probe-ratio
make bench-absolute-slo
make summarize-probe-slo
```

See `docs/experiments.md` for the thresholds. The checked-in supplemental summary is `experiments/results/probe_slo_summary.md`: PROBING traffic was `0.2177%`; absolute SLO scoring raised max slow_score from `0.377401` to `1.007183`, and `DEGRADED` appeared only when SLO scoring was enabled.

Unary SDK calls also use a bounded retry policy:

```text
max_attempts: 2
retryable_codes: UNAVAILABLE, DEADLINE_EXCEEDED
retry_budget: max(10, 0.15 * original_requests) per 10s window
per_try_timeout: 750ms
```

Retry budget is tracked per SDK `ClientConn`, which keeps a bad upstream from doubling traffic forever.

## Fault injector

The CLI prints commands unless `--execute` is set.

Network delay:

```powershell
go run ./cmd/fault-injector --kind delay --container aegis-user-b --delay 200ms --jitter 50ms
```

Packet loss:

```powershell
go run ./cmd/fault-injector --kind loss --container aegis-user-b --loss-percent 2
```

CPU throttling:

```powershell
go run ./cmd/fault-injector --kind cpu --container aegis-user-b --cpus 0.25
```

Reset `tc` qdisc:

```powershell
go run ./cmd/fault-injector --kind reset --container aegis-user-b
```

## Verifier

The verifier compares observed traces with a traffic policy: route distribution, retry attempts, and forbidden service-call edges.

```powershell
go run ./cmd/verifier --spec experiments/verifier/canary-user-service.yaml --traces experiments/verifier/sample-traces.jsonl
```

Trace JSONL records use this shape:

```json
{"trace_id":"trace-1","route":"user-service:primary","path":["frontend","user-service:primary"],"retry_attempts":0,"status":"OK"}
```

The sample trace file uses a 9/1 split and passes the 90/10 canary check. Real runs should use SDK trace output or request logs.

For a real SDK trace smoke run, `frontend-adaptive` writes JSONL traces to `experiments/traces/frontend-adaptive.jsonl`:

```bash
rm -f experiments/traces/frontend-adaptive.jsonl
make experiments-up
curl 'http://127.0.0.1:8083/checkout'
go run ./cmd/verifier --spec experiments/verifier/real-trace-smoke.yaml --traces experiments/traces/frontend-adaptive.jsonl
```

Each SDK trace event includes `x-aegis-trace-id`, `x-aegis-span-id`, `x-aegis-attempt`, route, path, upstream, status, and retry-attempt count. The verifier reads those files directly.

## Dashboard

Prometheus scrape config:

```text
dashboard/prometheus/prometheus.yml
```

Grafana import file:

```text
dashboard/grafana/aegismesh-overview.json
```

The dashboard has panels for RPC throughput, p99 latency, EWMA latency, endpoint slow score, endpoint state, and in-flight RPCs.

## eBPF telemetry

`agent/ebpf` maps Linux TCP events into Aegis endpoint samples. On non-Linux hosts the collector returns `ErrUnsupportedPlatform`, but aggregation and Controller conversion still have Go tests. On Linux, `cmd/agent` loads the compiled BPF object, attaches kprobes, reads the `events` ringbuf, and reports samples to the Controller.

The BPF program lives under:

```text
agent/ebpf/bpf/tcp_metrics.bpf.c
```

eBPF signals:

```text
tcp_retransmit
connect_error
connect_latency
```

`tcp_retransmit` and `connect_error` contribute to the Controller's network score even when the sample comes from the agent rather than from an RPC window.

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

AegisMesh has a Social Network adapter config and a plan generator:

```powershell
go run ./cmd/deathstarbench-adapter --config experiments/deathstarbench/social-network.yaml
```

The generated plan prints the Docker Compose command, Controller environment, service mapping, and workload command. It does not clone or modify the DeathStarBench repo.
