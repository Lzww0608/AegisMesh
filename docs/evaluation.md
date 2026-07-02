# AegisMesh Evaluation Plan

This file is the measurement log: what was run, what numbers are checked in, and how to reproduce them. The narrative report is `docs/project_report.md`.

## Run Environment

Environment used for the checked-in CSV rows:

- CPU: 32 x 13th Gen Intel(R) Core(TM) i9-13900K
- Memory: 62.5 GiB
- OS / kernel: Linux 6.8.0-111-generic x86_64
- Docker: 28.0.1, build 068a01e
- Go: go1.26.3 linux/amd64
- AegisMesh commit: record `git rev-parse --short HEAD` for new runs
- Merged result directory: `experiments/results/combined`
- Result checker command: `python experiments/scripts/check_results.py --results experiments/results/combined`

## Baseline Throughput And Latency

Compare direct/no-mesh calls against AegisMesh with no injected fault.

Metrics:

- throughput RPS
- p50 / p95 / p99 latency
- error rate

Output: `experiments/results/latency.csv`.

## Slow Instance Delay

Compare round-robin routing with adaptive P2C while one `user-service` instance has injected delay.

Metrics:

- p99 latency during fault
- slow_score convergence time
- traffic share to the slow endpoint near the end of the run

Output: `experiments/results/latency.csv` and `experiments/results/recovery.csv`.

## CPU Throttle Slow Fault

Compare a static-threshold baseline with slow_score while one endpoint is CPU throttled.

Metrics:

- false positive windows
- false negative windows
- time to DEGRADED / EJECTED
- p99 latency

Output: `experiments/results/recovery.csv`.

## Retry Budget Amplification

Compare retry behavior with and without a retry budget under errors or timeouts.

Metrics:

- original requests
- retry attempts
- total attempts
- retry amplification
- error rate

Output: `experiments/results/retry.csv`.

## Measured Results

The merged CSV files contain 61 latency rows, 18 retry rows, and 747 recovery rows after obsolete intermediate recovery runs were removed. `check_results.py` reports evidence rows for all required comparisons.

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

Numbers used in the report:

- No fault: AegisMesh adds 13.48% median p99 overhead compared with no-mesh direct calls.
- Single slow instance: adaptive P2C lowers median p99 by 90.62% compared with round-robin.
- CPU throttle: slow_score lowers median p99 by 43.00% compared with the static-threshold baseline.
- Retry budget: extra retries drop from 1000 to 150 per 1000 original requests; amplification goes from 2.000x to 1.150x.
- Recovery: the delayed endpoint moves through `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`, and the last sampled rows return to `HEALTHY`.

## Fault Recovery Curve

Record route weight, slow_score, p99 latency, and endpoint state during fault injection and recovery.

Metrics:

- timestamp
- endpoint
- slow_score
- p99 latency
- route weight
- state

Output: `experiments/results/recovery.csv`.

## Supplemental PROBING And Absolute-SLO Checks

These checks cover two mechanisms outside the original latency/retry matrix:

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

Supplemental notes:

- During the PROBING window, endpoint `172.18.0.5:7002` received 560 of 257258 user-service trace rows, or 0.2177%, below the 10% experiment bound.
- Without absolute SLO scoring, the all-slow delay run stayed healthy and max slow_score remained 0.377401.
- With `AEGIS_HEALTH_LATENCY_SLO=100ms`, max slow_score rose to 1.007183 and the controller produced 75 `DEGRADED` samples.

## Ablation Matrix

Useful variants:

| Variant | What it checks |
| --- | --- |
| round_robin | baseline routing |
| adaptive_p2c | route away from slow endpoints |
| static_threshold | baseline outlier detection |
| slow_score | adaptive relative and network-aware scoring |
| retry_without_budget | show retry amplification risk |
| retry_with_budget | show bounded amplification |
| no_ebpf_network_score | isolate application-only telemetry |
| ebpf_network_score | include retransmit/connect signals |

## DeathStarBench Runner Contract

DeathStarBench runs are recorded separately from the measured single-machine Docker matrix. A valid runner directory lives under `experiments/results/runs/deathstarbench-*` and must include `run_manifest.json`, `integration_plan.json`, `aegis-compose.override.yml`, workload stdout/stderr, compose ps/logs, and `latency.csv` with the standard latency schema.

Validation command:

```bash
go run ./cmd/deathstarbench-adapter --validate-run experiments/results/runs/deathstarbench-social-network-<timestamp>
```

The validator rejects plan-only directories and failed runs. It checks that the manifest completed, `integration_plan.json` and the metadata overlay are non-empty, workload stdout is non-empty, workload stderr exists, compose ps and logs are non-empty, `latency.csv` has at least one data row, the Aegis service map is present, and the injection mode is known. Use `--require-governed-traffic` only after a sidecar/proxy or service rewrite actually routes DeathStarBench calls through AegisMesh; that stricter mode requires both `mode=sidecar_proxy` and `traffic_governance=sidecar_proxy`. The current generated overlay records `traffic_governance=metadata_only`.

Do not merge DeathStarBench runner artifacts into `experiments/results/combined` or quote them as benchmark results until a real governed run and its validation output are checked in.

## Reporting guardrail

Do not write `X%`, `Ys`, or a benchmark table until the rows come from a real run. The schema files in `experiments/results` define columns only; they are not results. A DeathStarBench plan or metadata-only runner manifest is also not a benchmark result.
