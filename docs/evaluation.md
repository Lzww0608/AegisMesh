# AegisMesh Evaluation Plan

This document is the reproducible evaluation entrypoint. It records what should be measured, how to collect it, and where generated data belongs.

## Run Environment

Recorded environment for the checked-in measured CSV rows:

- CPU: 32 x 13th Gen Intel(R) Core(TM) i9-13900K
- Memory: 62.5 GiB
- OS / kernel: Linux 6.8.0-111-generic x86_64
- Docker version: Docker version 28.0.1, build 068a01e
- Go version: go version go1.26.3 linux/amd64
- AegisMesh commit: 1e0c20d
- Benchmark command: `make demo-up && make bench && make report`; Docker build was blocked by transient external Go proxy timeouts, so the same demo binaries were run locally and the benchmark/report scripts were executed against `127.0.0.1`.

## Baseline Throughput And Latency

Goal: compare no-mesh or direct-call baseline against AegisMesh under no injected fault.

Metrics:

- throughput RPS
- p50 / p95 / p99 latency
- error rate

Output: `experiments/results/latency.csv`.

## Slow Instance Delay

Goal: compare round-robin routing against adaptive P2C when one user-service instance has 200 ms injected network delay.

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

The current checked-in CSV files were generated from a local run with `REQUESTS=200` and `CONCURRENCY=16`.

| Experiment | Variant | p99 latency | Error rate | Throughput |
| --- | --- | ---: | ---: | ---: |
| baseline | aegismesh | 14.676 ms | 0.000 | 2040.816 RPS |
| single_instance_delay | adaptive_p2c | 202.925 ms | 0.000 | 740.741 RPS |
| retry_budget | with_budget | 204.172 ms | 0.235 | 873.362 RPS |

Quantitative conclusions:

- p99 latency increased from 14.676 ms at baseline to 202.925 ms under a 200 ms single-instance slow fault, matching the injected delay magnitude.
- slow_score convergence for the slow endpoint was observed within the first sampled recovery window: `127.0.0.1:7002` reached `slow_score=0.45`, reducing effective route weight to `0.689655`; the sampled convergence upper bound is 1.0 s.
- retry amplification with retry budget was 1.145x: 200 original requests produced 29 retries and 229 total attempts, keeping extra attempts below the 15% budget cap.

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
