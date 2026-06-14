# AegisMesh Project Report

## Abstract

AegisMesh is a Go/gRPC RPC governance system for fail-slow microservices. It includes a Controller, memory and file-backed registries, PolicyService YAML snapshots, a Go gRPC SDK, Prometheus/Grafana metrics, endpoint telemetry windows, slow-fault scoring, an endpoint state machine, adaptive P2C routing, retry budgets, circuit breaking, fault injection, CI checks, a verifier for real SDK trace JSONL, eBPF TCP telemetry, and a DeathStarBench integration planner. The system targets a common failure mode: an endpoint still passes health checks, but its latency is high enough to hurt p99. AegisMesh detects that endpoint, shifts normal traffic away from it, limits retries, and brings it back through probing after the fault is removed.

The final single-machine benchmark matrix comes from `experiments/results/combined`. It contains 61 latency rows, 18 retry rows, and 747 recovery rows after obsolete intermediate recovery runs were removed. `experiments/scripts/check_results.py --results experiments/results/combined` reports evidence for every required comparison. In the slow-instance delay experiment, adaptive P2C reduced median p99 latency from 348.682 ms with round-robin to 32.712 ms, a 90.62% reduction. Under CPU throttle, slow_score reduced median p99 latency from 46.596 ms to 26.559 ms, a 43.00% reduction. Retry budget reduced amplification from 2.000x to 1.150x. In the `recovery-state-final` run, the delayed endpoint reached `slow_score=0.95`, moved through `DEGRADED`, `EJECTED`, and `PROBING`, then returned to `HEALTHY` after the fault reset. Two later runs cover narrower checks: PROBING traffic stayed at 0.2177% of user-service trace rows, and absolute SLO scoring raised max slow_score from 0.377401 to 1.007183, with `DEGRADED` appearing only in the SLO-enabled run.

## Problem Statement

Traditional service health checks are effective for fail-stop faults, but they are weak against fail-slow behavior. A microservice instance can keep accepting TCP connections and returning responses while its latency becomes high enough to dominate user-facing tail latency. Static outlier thresholds are also fragile because latency distributions vary by service, method, workload intensity, and deployment environment. A fixed timeout or a fixed p99 threshold can either miss a slow instance or eject healthy instances during legitimate workload changes.

AegisMesh treats slow fault handling as an online control problem. The SDK records per-endpoint RPC behavior. The Controller scores endpoints against their peers, applies hysteresis and ejection windows, and returns health state to the data plane. The SDK then uses that state for adaptive routing and bounded retries.

## System Design

The control plane consists of `cmd/controller`, `pkg/registry`, `pkg/policy`, `pkg/controller`, and `pkg/fault`. Services register themselves with TTL leases, periodically heartbeat, and are returned to clients through `RegistryService.ListInstances`. The Controller can use the default in-memory registry or a JSON snapshot registry that restores unexpired instances after restart. It also exposes `PolicyService.GetPolicy` and `PolicyService.WatchPolicy`, backed by a YAML policy file that can be reloaded and streamed as versioned snapshots. SDK clients consume these snapshots for initial routing-policy selection and live retry controls, including retry budget parameters, per-method timeout, and idempotency-aware retry suppression. The Controller also exposes `TelemetryService.ReportEndpointStats`, which receives endpoint windows from SDK clients. Each window includes request count, error count, timeout count, in-flight count, EWMA latency, p95 latency, and optional network-layer counters.

The data plane is implemented in `sdk/go/aegisgrpc`. It provides an Aegis resolver, a default adaptive P2C balancer, unary telemetry interception, retry policy enforcement, and retry-budget admission. A client dials a logical service through the Controller address rather than through a static upstream list. The resolver periodically fetches registered instances and overlays Controller health state and slow_score onto gRPC resolver attributes. The balancer reads those attributes and keeps local in-flight and EWMA state for endpoint selection.

The observability layer exports SDK-side RPC metrics and Controller-side endpoint health metrics. Prometheus scrape configurations and an importable Grafana dashboard are provided under `dashboard/`. The eBPF agent under `cmd/agent` and `agent/ebpf` can collect TCP retransmit/connect telemetry on Linux and report those signals into the same Controller telemetry path. The verifier under `cmd/verifier` checks trace JSONL files against route-distribution, retry-attempt, and forbidden-edge policies; the SDK can now emit real runtime trace JSONL containing trace IDs, span IDs, upstream routes, attempts, status, and retry-attempt counts.

## Core Algorithms

The slow_score calculator combines latency, error, in-flight, and retransmit signals. For each service, latency is scored relative to the service median and median absolute deviation, while error and retransmit scores are normalized by service-level averages. It also supports an optional absolute p95 latency SLO, where the final latency component is `max(relative_median_mad_score, p95_latency / latency_slo)`. This lets the system detect a single-instance service or an all-slow service instead of relying only on peer outliers. The default weights are 0.45 for latency, 0.25 for errors, 0.20 for in-flight pressure, and 0.10 for retransmit/connect signals. This makes the score sensitive to a slow endpoint relative to its peers while still allowing network-level evidence to influence routing and health decisions.

The endpoint state machine has five states: `HEALTHY`, `DEGRADED`, `EJECTED`, `PROBING`, and `DEAD`. The production defaults are conservative: degraded threshold 1.5, eject threshold 2.5, three consecutive windows, 30 s ejection duration, and recovery threshold 1.0. For single-machine recovery demonstrations, the experiment stack can lower these thresholds so that a controlled delay fault produces observable state transitions in a short benchmark window. The state machine provides hysteresis: repeated slow windows are required before ejection, ejected endpoints wait before probing, and probing endpoints must meet success and score criteria before returning to healthy.

Adaptive P2C chooses two routable endpoints and selects the lower-cost endpoint. The cost function includes in-flight pressure divided by effective weight, local EWMA latency, slow_score, and an additional penalty for degraded endpoints. The effective weight is reduced as slow_score increases, which causes traffic to drift away from slow endpoints before hard ejection. `EJECTED` and `DEAD` endpoints are removed from resolver output and therefore do not receive normal traffic. `PROBING` endpoints remain discoverable but are separated from normal candidates while healthy or degraded endpoints exist; adaptive P2C admits them through a small probe ratio, defaulting to 2%, before returning them to normal routing.

Retries are bounded by a per-connection retry budget. The default retry policy allows up to two unary attempts for retryable transport codes, while the budget caps extra attempts at `max(10, 0.15 * original_requests)` per 10 s window. The experiment intentionally uses a dedicated always-unavailable retry service so that retry amplification can be measured directly.

## Evaluation Methodology

All reported numbers come from `experiments/results/combined` after running `make merge-results`. The result checker was run with:

```bash
python experiments/scripts/check_results.py --results experiments/results/combined
```

The checker reported 61 latency rows, 18 retry rows, 747 recovery rows, and all required experiment comparisons present. The experiments were run on a single Linux machine with Docker Compose. The setup simulates a small shop-style microservice graph with one frontend, two `user-service` instances, one `order-service` instance, and dedicated frontend variants for direct/no-mesh, round-robin, adaptive P2C, retry without budget, retry with budget, and retry-off modes.

The required matrix contains six comparisons. Baseline compares direct/no-mesh calls with AegisMesh in the no-fault case. Slow instance delay compares round-robin with adaptive P2C when one user-service instance is delayed. CPU throttle compares a static-threshold baseline with slow_score. Retry budget compares unbudgeted and budgeted retries under an always-unavailable upstream. Packet loss compares runs without and with eBPF network-score input. Recovery curve records slow_score, route weight, and state transitions over time.

## Results

### Latency And Throughput

| Scenario | Baseline Variant | Baseline Median p99 | AegisMesh Variant | Variant Median p99 | p99 Change | Median Throughput |
| --- | --- | ---: | --- | ---: | ---: | ---: |
| No fault | `no_mesh` | 24.721 ms | `aegismesh` | 28.053 ms | -13.48% | 2290.978 vs 2257.382 RPS |
| Single slow instance | `round_robin` | 348.682 ms | `adaptive_p2c` | 32.712 ms | 90.62% lower | 254.478 vs 1631.996 RPS |
| CPU throttle | `static_threshold` | 46.596 ms | `slow_score` | 26.559 ms | 43.00% lower | 2092.132 vs 2652.539 RPS |
| Packet loss | `no_ebpf_network_score` | 27.539 ms | `ebpf_network_score` | 26.456 ms | 3.93% lower | 2262.316 vs 2183.219 RPS |

The no-fault baseline gives the cost of the extra layer: AegisMesh adds 13.48% p99 latency in the merged median, while throughput stays close to the no-mesh path. The value shows up under fault. In the slow-instance delay experiment, round-robin keeps sending traffic to the delayed instance and reaches 348.682 ms median p99. Adaptive P2C keeps median p99 at 32.712 ms. CPU throttle also improves with slow_score because the degraded endpoint gets a lower effective routing weight.

The packet-loss result is small: median p99 improves by 3.93%. I would not present this as a major eBPF performance win. The fair claim is narrower: the eBPF path is wired into scoring, and a larger multi-host or namespace-isolated setup is needed before making stronger claims about network faults.

### Retry Amplification

| Variant | Median Original Requests | Median Retry Attempts | Median Total Attempts | Median Amplification |
| --- | ---: | ---: | ---: | ---: |
| `without_budget` | 1000 | 1000 | 2000 | 2.000x |
| `with_budget` | 1000 | 150 | 1150 | 1.150x |

The retry experiment isolates retry amplification. Without a budget, every original request is retried once, so attempts double. With the default 15% budget, 1000 original requests produce 150 retries and 1150 total attempts. Extra retry attempts drop by 85%, and total downstream attempts drop by 42.5% compared with the unbudgeted case. The upstream is synthetic and always returns `UNAVAILABLE`, so `error_rate=1.0` is expected. It is not an availability result.

### Recovery Curve

The final recovery run is `experiments/results/runs/recovery-state-final`. It used `CONCURRENCY=16`, `DELAY=800ms`, `JITTER=150ms`, `PRE_DURATION=20s`, `FAULT_DURATION=120s`, `POST_DURATION=40s`, and `RECOVERY_DURATION=190s`.

| State | Count |
| --- | ---: |
| `HEALTHY` | 282 |
| `DEGRADED` | 3 |
| `EJECTED` | 88 |
| `PROBING` | 4 |

The affected endpoint was `172.18.0.5:7002`. Its maximum slow_score was 0.95 and its route weight fell to 0.512821 while slow. The unaffected endpoint `172.18.0.6:7001` stayed healthy with max slow_score 0.0. Relative to the recorder start, the affected endpoint first entered `DEGRADED` at about 23 s and first entered `EJECTED` at about 28 s. Because the fault was injected after the 20 s pre-fault phase, the first degraded transition happened about 3 s after injection and the first ejection happened about 8 s after injection. After the fault reset around the end of the 120 s fault phase, the endpoint returned to `HEALTHY` in the first sampled post-reset window and the final 30 sampled rows were all `HEALTHY`.

This run captures the recovery path needed for the report: `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`. The important part is not the state names themselves, but the behavior behind them: weight drops while the endpoint is slow, normal traffic stops during ejection, small probe traffic is allowed later, and the endpoint returns after the fault clears.

### Supplemental Probe And SLO Checks

The supplemental results are stored in `experiments/results/probe_slo_summary.md` and the corresponding JSON files under `experiments/results/runs/`. These runs cover two mechanisms added after the main benchmark matrix: restricted traffic during `PROBING`, and absolute SLO scoring for all-slow or single-instance services.

| Mechanism | Run | Key Measurement | Result |
| --- | --- | ---: | --- |
| PROBING probe ratio | `probe-ratio` | 560 probing-endpoint traces / 257258 user-service traces = 0.2177% | PASS, below 10% experiment bound |
| Absolute SLO disabled | `absolute-slo-disabled` | max slow_score 0.377401; states `HEALTHY` only | Negative control, no state reaction |
| Absolute SLO enabled | `absolute-slo-enabled` | max slow_score 1.007183; states `DEGRADED`, `HEALTHY` | PASS, SLO scoring triggered health reaction |

The probe-ratio result shows that a recovering endpoint was not immediately restored to normal traffic. During the `PROBING` window, endpoint `172.18.0.5:7002` received only 560 of 257258 traced user-service calls, or 0.2177%, while the healthy peer received the remaining 256698 calls. This is below the configured 10% experiment bound and is consistent with the intended small-probe behavior.

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

The current evaluation is a single-machine simulation. It is useful for checking system behavior and comparing routing policies under controlled faults, but it does not replace a multi-node production-like benchmark. Docker networking, local CPU scheduling, and co-located containers can influence latency and recovery timing. The Controller now supports a file-backed registry snapshot, but this is local restart recovery rather than a replicated high-availability control plane. The policy layer exposes dynamic YAML snapshots through PolicyService, and the SDK applies the most important client-side fields for routing initialization, retry budgets, method timeout, and idempotency-aware retry control; broader online application of every outlier-detection and circuit-breaker field remains future work. The verifier now supports real SDK JSONL trace output, but it is still a file-based verification loop rather than a central online trace collector. The DeathStarBench component is a plan generator rather than a completed DeathStarBench benchmark result, so the project should not claim DeathStarBench evaluation until that experiment is actually run.

The eBPF experiment should also be described conservatively. The code path exists and the merged packet-loss comparison shows a small p99 improvement, but the measured effect on one machine is modest. A stronger claim would require a more realistic network-fault setup and automated endpoint mapping from Controller registry state.

## Conclusion

AegisMesh has moved past the demo stage. It now has a runnable control plane and SDK, a single-machine experiment matrix, and measured results for the main claims. The clearest evidence is adaptive P2C under slow-instance delay, slow_score under CPU throttle, bounded retry amplification, endpoint recovery through probing, restricted PROBING traffic, and absolute-SLO detection. The resume version should still keep the caveats: the evaluation is single-machine, the file-backed registry is not a replicated control plane, the verifier is file-based, and DeathStarBench is an integration planner rather than a measured benchmark.
