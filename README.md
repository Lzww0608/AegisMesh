# AegisMesh

AegisMesh is a Go/gRPC RPC governance project for fail-slow microservices. It focuses on the case where an endpoint is still alive, still passes health checks, and still returns responses, but is slow enough to dominate the caller's p99 latency.

The project is built as a closed loop. The Controller keeps service registry, policy, telemetry, and endpoint health state. The Go SDK feeds per-endpoint observations back to the Controller, then uses the returned state for resolver output, adaptive P2C routing, retry admission, tracing, and local metrics.

That makes AegisMesh useful to discuss in interviews because the core claims connect code, algorithms, and measured behavior: endpoint scoring changes route weight, route weight changes tail latency, retry budgets cap amplification, and the state machine shows how a slow endpoint is removed, probed, and returned.

## What It Implements

- Controller-side service registry with TTL leases, memory and file-backed backends, and health state overlay.
- YAML-backed `PolicyService` with SDK `GetPolicy` and `WatchPolicy` support.
- Go gRPC SDK with Aegis resolver, adaptive P2C balancer, unary telemetry, retry budget, trace JSONL, and endpoint circuit breaker.
- `slow_score` built from latency, error, in-flight, and optional Linux TCP signals.
- Endpoint state machine: `HEALTHY`, `DEGRADED`, `EJECTED`, `PROBING`, `DEAD`.
- Prometheus/Grafana observability, fault injection helpers, a trace verifier, and Linux eBPF telemetry plumbing.

## Core Mechanisms

```text
SDK telemetry windows
  -> Controller slow_score
  -> endpoint state and route weight
  -> SDK resolver and adaptive P2C
  -> retry budget and circuit breaker
  -> new telemetry windows
```

Adaptive P2C samples two ready endpoints and chooses the lower-cost one. The cost combines in-flight pressure, EWMA latency, `slow_score`, and endpoint state. A slow endpoint is down-weighted first, then removed from normal routing if the state machine ejects it.

Retry budget is tracked per SDK connection. The default retry policy can retry transport failures, but extra attempts are capped by a time-window budget so a bad upstream does not double downstream traffic indefinitely.

`PROBING` is separated from normal routing. A recovering endpoint stays discoverable, but receives only limited probe traffic before it returns to full routing.

## Measured Evidence

The checked-in report and evaluation log record the merged single-machine Docker results, and `experiments/results/probe_slo_summary.md` records the supplemental probe/SLO checks.

| Question | Result |
| --- | --- |
| Can routing avoid one slow instance? | adaptive P2C reduced median p99 from `348.682 ms` to `32.712 ms` versus round-robin. |
| Can retries avoid amplification? | retry budget reduced amplification from `2.000x` to `1.150x`. |
| Does recovery move through explicit states? | the delayed endpoint followed `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`. |

The same result set also records CPU-throttle improvement with `slow_score`, bounded `PROBING` traffic, and absolute-SLO scoring for all-slow cases. See [docs/project_report.md](docs/project_report.md) and [docs/evaluation.md](docs/evaluation.md) for the full numbers and methodology.

## Quick Start

Run the Go test suite:

```bash
go test ./...
```

Start the experiment stack:

```bash
make experiments-up
```

Check the merged experiment results:

```bash
make check-results
```

The detailed reproduction flow is in [docs/experiments.md](docs/experiments.md).

## Repository Map

```text
cmd/controller/             Controller server
sdk/go/aegisgrpc/           Go gRPC SDK resolver, balancer, retry, telemetry
pkg/fault/                  slow_score and endpoint state machine
pkg/registry/               service registry implementations
pkg/policy/                 YAML policy snapshots and watcher
pkg/retry/                  retry budget accounting
agent/ebpf/                 Linux TCP telemetry collector and aggregation
cmd/fault-injector/         Docker/tc fault command helper
cmd/verifier/               trace-policy verifier CLI
experiments/                benchmark scripts, configs, checked-in results
dashboard/                  Prometheus and Grafana assets
interview/                  system design interview question bank
```

## Documentation

- [docs/README.md](docs/README.md): documentation index.
- [docs/project_report.md](docs/project_report.md): full project report and experiment narrative.
- [docs/evaluation.md](docs/evaluation.md): measurement log and result sources.
- [docs/experiments.md](docs/experiments.md): reproduction runbook.
- [docs/resume.md](docs/resume.md): resume wording and interview talking points.
- [interview/README.md](interview/README.md): structured interview question bank.
- [agent/ebpf/README.md](agent/ebpf/README.md): Linux eBPF validation notes.

## Common Commands

```bash
make experiments-up
make bench-single-machine
make merge-results
make check-results
make report
make experiments-down
```

Use `docs/experiments.md` before quoting new performance numbers. The README only lists checked-in evidence from the current report.
