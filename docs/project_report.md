# AegisMesh Project Report

## Abstract

AegisMesh is a Go/gRPC project for one failure pattern I wanted to study closely: fail-slow. In this case an endpoint is still alive, still accepting TCP connections, and still returning responses, but it is slow enough to dominate p99 latency.

The implementation has a Controller, memory and file-backed registries, file/etcd-backed PolicyService, a Go gRPC SDK, Prometheus/Grafana metrics, endpoint telemetry, slow_score, an endpoint state machine, adaptive P2C routing, retry budget, circuit breaking, fault injection, CI checks, verifier support for real SDK trace JSONL, Linux eBPF TCP telemetry, and an opt-in DeathStarBench runner. This report focuses on the parts with measured evidence.

The merged single-machine results live in `experiments/results/combined`: 61 latency rows, 18 retry rows, and 747 recovery rows. `experiments/scripts/check_results.py --results experiments/results/combined` finds evidence for every required comparison. In the slow-instance delay run, adaptive P2C reduced median p99 from 348.682 ms with round-robin to 32.712 ms, a 90.62% drop. Under CPU throttle, slow_score reduced median p99 from 46.596 ms to 26.559 ms. Retry budget reduced amplification from 2.000x to 1.150x. The recovery run shows the delayed endpoint reaching `slow_score=0.95`, moving through `DEGRADED`, `EJECTED`, and `PROBING`, then returning to `HEALTHY`. Two smaller checks cover probe-ratio behavior and absolute SLO scoring.

## Problem Statement

Normal health checks handle fail-stop faults well. They are much weaker for fail-slow faults. A service can pass readiness, accept connections, and still be the reason a checkout path is slow.

Static thresholds are not enough either. A p99 threshold that works for one method can be too strict for another. A timeout that protects a write path may be too aggressive for a read path during a traffic spike. AegisMesh handles this as a feedback loop: SDKs record endpoint behavior, the Controller scores and classifies endpoints, and the SDK uses that state for routing and retry decisions.

## System Design

The control plane is `cmd/controller` plus the packages under `pkg/registry`, `pkg/policy`, `pkg/controller`, and `pkg/fault`. Services register with TTL leases and refresh them through heartbeat. `RegistryService.ListInstances` returns the instance list with Controller health state overlaid on top.

The registry can be in-memory for quick runs or file-backed for local restart recovery. PolicyService reads file YAML or shared etcd policy snapshots and exposes `GetPolicy` and `WatchPolicy`; SDK clients use it for routing selection, retry budget settings, per-method timeout, and idempotency-aware retry suppression. TelemetryService receives endpoint windows from SDK clients. A window includes request count, error count, timeout count, in-flight count, EWMA latency, p95 latency, and optional network counters.

The data plane is implemented in `sdk/go/aegisgrpc`. It provides an Aegis resolver, a default adaptive P2C balancer, unary telemetry interception, retry policy enforcement, and retry-budget admission. A client dials a logical service through the Controller address rather than through a static upstream list. The resolver periodically fetches registered instances and overlays Controller health state and slow_score onto gRPC resolver attributes. The balancer reads those attributes and keeps local in-flight and EWMA state for endpoint selection.

Metrics are split by purpose. Prometheus gets the data needed for dashboards. Controller telemetry gets the data needed for decisions. The eBPF agent can add TCP retransmit/connect signals on Linux, and the verifier can check SDK JSONL traces against route-distribution, retry-attempt, and forbidden-edge rules.

## Core mechanics

slow_score combines latency, errors, in-flight pressure, and network signals. Latency is scored relative to the service median and MAD. There is also an optional absolute p95 SLO term:

```text
latency_score = max(relative_median_mad_score, p95_latency / latency_slo)
```

That extra SLO term matters when the service has one instance or when every instance is slow. Without it, relative scoring can tell you "no one is worse than the others" while the whole service is missing its latency target.

The endpoint state machine uses `HEALTHY`, `DEGRADED`, `EJECTED`, `PROBING`, and `DEAD`. Defaults are intentionally conservative: degraded threshold 1.5, eject threshold 2.5, three consecutive windows, 30 s ejection duration, and recovery threshold 1.0. The experiment stack can lower those thresholds so a single-machine delay fault produces visible transitions in a short run. The important detail is hysteresis: one bad window is not enough to eject, and one good probe is not enough to return to full traffic.

Adaptive P2C chooses two routable endpoints and selects the lower-cost endpoint. The cost function includes in-flight pressure divided by effective weight, local EWMA latency, slow_score, and an additional penalty for degraded endpoints. The effective weight is reduced as slow_score increases, which causes traffic to drift away from slow endpoints before hard ejection. `EJECTED` and `DEAD` endpoints are removed from resolver output and therefore do not receive normal traffic. `PROBING` endpoints remain discoverable but are separated from normal candidates while healthy or degraded endpoints exist; adaptive P2C admits them through a small probe ratio, defaulting to 2%, before returning them to normal routing.

Retries are bounded by a per-connection retry budget. The default retry policy allows up to two unary attempts for retryable transport codes, while the budget caps extra attempts at `max(10, 0.15 * original_requests)` per 10 s window. The experiment intentionally uses a dedicated always-unavailable retry service so that retry amplification can be measured directly.

## Evaluation Methodology

All reported numbers come from `experiments/results/combined` after running `make merge-results`. The result checker was run with:

```bash
python experiments/scripts/check_results.py --results experiments/results/combined
```

The checker reported 61 latency rows, 18 retry rows, 747 recovery rows, and all required comparisons present. The experiments ran on one Linux machine with Docker Compose. The topology is deliberately small: one frontend, two `user-service` instances, one `order-service`, and separate frontend processes for direct/no-mesh, round-robin, adaptive P2C, retry without budget, retry with budget, and retry-off modes.

The matrix has six comparisons: no-fault overhead, slow-instance delay, CPU throttle, retry budget, packet loss with and without eBPF scoring, and recovery curve.

Hot-path microbenchmarks and `SnapshotAndReset` grids (`benchmarks/baseline/`) were re-captured in parallel via `run_full_baseline.sh`; the race detector run that follows is green on all seven benchmarked packages, so no data races remain in production code. Two pre-existing test-only flakes (a `sync.Pool` assumption in `pkg/telemetry` and an unsynchronised clock closure in `pkg/registry`'s watch test) were fixed alongside this run. See `docs/experiments.md` §13.

## Results

### Latency And Throughput

| Scenario | Baseline Variant | Baseline Median p99 | AegisMesh Variant | Variant Median p99 | p99 Change | Median Throughput |
| --- | --- | ---: | --- | ---: | ---: | ---: |
| No fault | `no_mesh` | 24.721 ms | `aegismesh` | 28.053 ms | -13.48% | 2290.978 vs 2257.382 RPS |
| Single slow instance | `round_robin` | 348.682 ms | `adaptive_p2c` | 32.712 ms | 90.62% lower | 254.478 vs 1631.996 RPS |
| CPU throttle | `static_threshold` | 46.596 ms | `slow_score` | 26.559 ms | 43.00% lower | 2092.132 vs 2652.539 RPS |
| Packet loss | `no_ebpf_network_score` | 27.539 ms | `ebpf_network_score` | 26.456 ms | 3.93% lower | 2262.316 vs 2183.219 RPS |

The no-fault baseline is the cost of the extra layer: AegisMesh adds 13.48% p99 latency in the merged median, while throughput stays close to the no-mesh path. The tradeoff becomes worthwhile under fault. With one delayed user instance, round-robin keeps feeding the slow instance and reaches 348.682 ms median p99. Adaptive P2C holds median p99 at 32.712 ms. CPU throttle improves for the same reason: the degraded endpoint gets a lower effective routing weight.

The packet-loss result is small: median p99 improves by 3.93%. The supported claim is narrow: the eBPF path is wired into scoring, and a stronger network-fault result needs a multi-host or namespace-isolated setup.

### Retry Amplification

| Variant | Median Original Requests | Median Retry Attempts | Median Total Attempts | Median Amplification |
| --- | ---: | ---: | ---: | ---: |
| `without_budget` | 1000 | 1000 | 2000 | 2.000x |
| `with_budget` | 1000 | 150 | 1150 | 1.150x |

The retry experiment isolates retry amplification. Without a budget, every original request is retried once, so attempts double. With the default 15% budget, 1000 original requests produce 150 retries and 1150 total attempts. Extra retry attempts drop by 85%, and total downstream attempts drop by 42.5% compared with the unbudgeted case. The upstream is synthetic and always returns `UNAVAILABLE`, so `error_rate=1.0` is expected. It is not an availability result.

### Recovery Curve

The recovery run used in this report is `experiments/results/runs/recovery-state-final`. It used `CONCURRENCY=16`, `DELAY=800ms`, `JITTER=150ms`, `PRE_DURATION=20s`, `FAULT_DURATION=120s`, `POST_DURATION=40s`, and `RECOVERY_DURATION=190s`.

| State | Count |
| --- | ---: |
| `HEALTHY` | 282 |
| `DEGRADED` | 3 |
| `EJECTED` | 88 |
| `PROBING` | 4 |

The affected endpoint was `172.18.0.5:7002`. Its maximum slow_score was 0.95 and its route weight fell to 0.512821 while slow. The unaffected endpoint `172.18.0.6:7001` stayed healthy with max slow_score 0.0. Relative to the recorder start, the affected endpoint first entered `DEGRADED` at about 23 s and first entered `EJECTED` at about 28 s. Because the fault was injected after the 20 s warm-up window, the first degraded transition happened about 3 s after injection and the first ejection happened about 8 s after injection. After the fault reset around the end of the 120 s fault window, the endpoint returned to `HEALTHY` in the first sampled post-reset window and the last 30 sampled rows were all `HEALTHY`.

This run captures the recovery path: `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`. The state names matter less than the behavior. Weight drops while the endpoint is slow. Normal traffic stops during ejection. Probe traffic returns later. After the fault clears, the endpoint becomes healthy again.

### Supplemental Probe And SLO Checks

The supplemental results are stored in `experiments/results/probe_slo_summary.md` and the corresponding JSON files under `experiments/results/runs/`. These runs cover two mechanisms added after the main benchmark matrix: restricted traffic during `PROBING`, and absolute SLO scoring for all-slow or single-instance services.

| Mechanism | Run | Key Measurement | Result |
| --- | --- | ---: | --- |
| PROBING probe ratio | `probe-ratio` | 560 probing-endpoint traces / 257258 user-service traces = 0.2177% | PASS, below 10% experiment bound |
| Absolute SLO disabled | `absolute-slo-disabled` | max slow_score 0.377401; states `HEALTHY` only | Negative control, no state reaction |
| Absolute SLO enabled | `absolute-slo-enabled` | max slow_score 1.007183; states `DEGRADED`, `HEALTHY` | PASS, SLO scoring triggered health reaction |

The probe-ratio result checks a practical failure mode: a recovering endpoint should not get normal traffic too early. During the `PROBING` window, endpoint `172.18.0.5:7002` received 560 of 257258 traced user-service calls, or 0.2177%. The healthy peer received the rest. That is below the 10% experiment bound.

The absolute-SLO result shows the weakness of relative scoring when every endpoint is slow. With `AEGIS_HEALTH_LATENCY_SLO=0s`, the all-slow run stayed healthy and max slow_score was 0.377401. With `AEGIS_HEALTH_LATENCY_SLO=100ms`, max slow_score rose to 1.007183 and the controller observed 75 `DEGRADED` samples. That supports the `max(relative_score, p95_latency / latency_slo)` design without changing the main latency, retry, or recovery conclusions.

## Reproducibility

The standard workflow is:

```bash
make experiments-up
RUNS_DIR=experiments/results/runs REQUESTS=1000 CONCURRENCY=32 make bench-single-machine
make merge-results
python experiments/scripts/check_results.py --results experiments/results/combined
python experiments/notebooks/plot_latency.py --results experiments/results/combined --out experiments/results/combined/figures
```

For recovery-state reproduction on one machine, restart the experiment stack with lowered local thresholds and run:

```bash
make experiments-down
AEGIS_DEGRADED_THRESHOLD=0.5 \
AEGIS_EJECT_THRESHOLD=0.8 \
AEGIS_CONSECUTIVE_WINDOWS=1 \
AEGIS_EJECTION_DURATION=10s \
AEGIS_RECOVERY_THRESHOLD=0.3 \
make experiments-up

RUN_ID=recovery-$(date +%Y%m%d-%H%M%S) \
RUNS_DIR=experiments/results/runs \
CONCURRENCY=16 \
DELAY=800ms \
JITTER=150ms \
PRE_DURATION=20s \
FAULT_DURATION=120s \
POST_DURATION=40s \
RECOVERY_DURATION=190s \
make bench-recovery-state
```

Supplemental probe-ratio and absolute-SLO checks can be reproduced with:

```bash
make bench-probe-ratio
make bench-absolute-slo
make summarize-probe-slo
```

The checked-in supplemental summary used for this report is `experiments/results/probe_slo_summary.md`.

The exact result checker output used for this report is:

```text
latency rows:  61
retry rows:    18
recovery rows: 747
baseline: no_mesh median p99=24.721 ms, aegismesh median p99=28.053 ms
single_instance_delay: round_robin median p99=348.682 ms, adaptive_p2c median p99=32.712 ms
cpu_throttle: static_threshold median p99=46.596 ms, slow_score median p99=26.559 ms
packet_loss: no_ebpf_network_score median p99=27.539 ms, ebpf_network_score median p99=26.456 ms
without_budget: median amplification=2.000x
with_budget: median amplification=1.150x
recovery states: DEGRADED, EJECTED, HEALTHY, PROBING
max slow_score: 0.950000
```

## Limitations

This is still a single-machine evaluation. It is good for checking behavior and comparing policies under controlled faults. It is not the same as a multi-node benchmark. Docker networking, local CPU scheduling, and co-located containers can all move the numbers.

The file-backed registry is still local restart recovery, not a replicated HA control plane. The production-oriented path now uses etcd-backed registry, policy, and non-stale health snapshot sharing, Controller TLS/mTLS, service-scoped bearer-token RBAC, and SDK ordered failover; see `docs/control_plane_production.md`. That path is covered by unit/race tests and env-gated live etcd tests, but it is not a measured multi-node benchmark in this report. Policy hot-apply now covers service-scoped `outlier_detection` and SDK `circuit_breaker.max_inflight_per_endpoint`; `routing_policy` remains dial-time only. The verifier reads real SDK JSONL traces, but it is still a file-based loop. DeathStarBench support now includes an opt-in runner that launches an external checkout, writes an AegisMesh compose metadata overlay, runs the workload, collects artifacts, and validates the run directory. The default overlay is metadata injection, not sidecar/proxy traffic governance, so it is still not a completed DeathStarBench benchmark result.

The eBPF result also needs restraint. The code path exists, and the packet-loss comparison shows a small p99 improvement, but the measured effect on one machine is modest. A stronger claim needs a more realistic network-fault setup and automatic endpoint mapping from Controller registry state.

## Conclusion

AegisMesh is a measured RPC governance project: the control loop runs, the experiments can be repeated, and the main claims have numbers behind them. Resume wording should stay precise: adaptive P2C under slow-instance delay, slow_score under CPU throttle, bounded retry amplification, recovery through probing, restricted PROBING traffic, and absolute-SLO detection. The caveats should stay visible in deeper documentation: single-machine evaluation, file-registry persistence versus etcd-backed HA deployment, file-based verifier, and DeathStarBench runner support rather than measured DeathStarBench results.
