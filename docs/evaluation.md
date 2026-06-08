# AegisMesh Evaluation Plan

This document is the reproducible evaluation entrypoint. It records what was measured, how to collect it again, and where generated data belongs. The final project report is `docs/project_report.md`.

## Run Environment

Recorded environment for the checked-in measured CSV rows:

- CPU: 32 x 13th Gen Intel(R) Core(TM) i9-13900K
- Memory: 62.5 GiB
- OS / kernel: Linux 6.8.0-111-generic x86_64
- Docker version: Docker version 28.0.1, build 068a01e
- Go version: go version go1.26.3 linux/amd64
- AegisMesh commit: record with `git rev-parse --short HEAD` when reproducing a new run
- Final merged result directory: `experiments/results/combined`
- Final result checker command: `python experiments/scripts/check_results.py --results experiments/results/combined`

## Baseline Throughput And Latency

Goal: compare no-mesh or direct-call baseline against AegisMesh under no injected fault.

Metrics:

- throughput RPS
- p50 / p95 / p99 latency
- error rate

Output: `experiments/results/latency.csv`.

## Slow Instance Delay

Goal: compare round-robin routing against adaptive P2C when one user-service instance has injected network delay.

Metrics:

- p99 latency during fault
- slow_score convergence time
- final traffic share to the slow endpoint

Output: `experiments/results/latency.csv` and `experiments/results/recovery.csv`.

## CPU Throttle Slow Fault

Goal: compare a static threshold baseline against slow_score when one endpoint is CPU throttled.

Metrics:

- false positive windows
- false negative windows
- time to DEGRADED / EJECTED
- p99 latency

Output: `experiments/results/recovery.csv`.

## Retry Budget Amplification

Goal: compare retry behavior with and without retry budget under transient errors or timeouts.

Metrics:

- original requests
- retry attempts
- total attempts
- retry amplification
- error rate

Output: `experiments/results/retry.csv`.

## Measured Results

The final merged CSV files contain 61 latency rows, 18 retry rows, and 747 recovery rows after obsolete intermediate recovery runs were removed. `check_results.py` reports that all required experiment comparisons have evidence rows.

| Experiment | Baseline variant | Baseline median p99 | AegisMesh variant | Variant median p99 | Delta |
| --- | --- | ---: | --- | ---: | ---: |
| baseline | no_mesh | 24.721 ms | aegismesh | 28.053 ms | -13.48% |
| single_instance_delay | round_robin | 348.682 ms | adaptive_p2c | 32.712 ms | 90.62% lower |
| cpu_throttle | static_threshold | 46.596 ms | slow_score | 26.559 ms | 43.00% lower |
| packet_loss | no_ebpf_network_score | 27.539 ms | ebpf_network_score | 26.456 ms | 3.93% lower |

Retry comparison:

| Variant | Median original requests | Median retries | Median total attempts | Median amplification |
| --- | ---: | ---: | ---: | ---: |
| without_budget | 1000 | 1000 | 2000 | 2.000x |
| with_budget | 1000 | 150 | 1150 | 1.150x |

Recovery comparison:

| Run | Fault setup | Max slow_score | Observed states |
| --- | --- | ---: | --- |
| recovery-state-final | 800 ms delay, 150 ms jitter, concurrency 16 | 0.950000 | HEALTHY, DEGRADED, EJECTED, PROBING |

Quantitative conclusions:

- In the no-fault baseline, AegisMesh adds 13.48% median p99 latency overhead compared with no-mesh direct calls.
- In the single slow instance experiment, adaptive P2C reduces median p99 latency by 90.62% compared with round-robin.
- In the CPU throttle experiment, slow_score reduces median p99 latency by 43.00% compared with the static-threshold baseline.
- In the retry experiment, retry budget reduces extra retry attempts from 1000 to 150 per 1000 original requests, limiting amplification from 2.000x to 1.150x.
- In the recovery experiment, the delayed endpoint completes the state loop `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`; the final sampled rows return to `HEALTHY`.

## Fault Recovery Curve

Goal: show route weight, slow_score, p99 latency, and endpoint state over time during fault injection and recovery.

Metrics:

- timestamp
- endpoint
- slow_score
- p99 latency
- route weight
- state

Output: `experiments/results/recovery.csv`.

## Supplemental PROBING And Absolute-SLO Validation

Goal: validate the two later mechanisms that are not part of the original latency/retry matrix:

- `PROBING` endpoints should receive only limited probe traffic before returning to normal routing.
- absolute SLO scoring should detect all-slow or single-instance slow services that relative peer-outlier scoring can miss.

Output:

- `experiments/results/probe_slo_summary.md`
- `experiments/results/runs/probe-ratio/probe_ratio_summary.json`
- `experiments/results/runs/absolute-slo-enabled/absolute_slo_summary.json`
- `experiments/results/runs/absolute-slo-disabled/absolute_slo_summary.json`

Measured supplemental results:

| Mechanism | Run | Measurement | Result |
| --- | --- | ---: | --- |
| PROBING probe ratio | probe-ratio | 0.2177% traffic to probing endpoint | PASS |
| Absolute SLO disabled | absolute-slo-disabled | max slow_score 0.377401, states HEALTHY only | Negative control |
| Absolute SLO enabled | absolute-slo-enabled | max slow_score 1.007183, states DEGRADED and HEALTHY | PASS |

Supplemental conclusions:

- During the PROBING window, endpoint `172.18.0.5:7002` received 560 of 257258 user-service trace rows, or 0.2177%, below the 10% experiment bound.
- Without absolute SLO scoring, the all-slow delay run stayed healthy and max slow_score remained 0.377401.
- With `AEGIS_HEALTH_LATENCY_SLO=100ms`, max slow_score rose to 1.007183 and the controller produced 75 `DEGRADED` samples.

## Ablation Matrix

Recommended variants:

| Variant | Purpose |
| --- | --- |
| round_robin | baseline routing |
| adaptive_p2c | route away from slow endpoints |
| static_threshold | baseline outlier detection |
| slow_score | adaptive relative and network-aware scoring |
| retry_without_budget | show retry amplification risk |
| retry_with_budget | show bounded amplification |
| no_ebpf_network_score | isolate application-only telemetry |
| ebpf_network_score | include retransmit/connect signals |

## Do Not Fabricate Results

Do not write `X%`, `Ys`, or any benchmark table until the CSV rows are generated from a real run. Placeholder schema files in `experiments/results` are column definitions, not results.
