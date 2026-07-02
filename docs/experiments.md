# AegisMesh Experiment Guide

This guide is for reproducing the single-machine benchmark data used in the report. Do not put a number in the resume until `check-results` passes on measured CSV files.

## 1. Start The Experiment Stack

```bash
make experiments-up
```

This brings up:

- `frontend-direct` on `127.0.0.1:8081`
- `frontend-round-robin` on `127.0.0.1:8082`
- `frontend-adaptive` on `127.0.0.1:8083`
- `frontend-retry-unbudgeted` on `127.0.0.1:8084`
- `frontend-retry-off` on `127.0.0.1:8085`
- `frontend-retry-budgeted` on `127.0.0.1:8086`

The experiment controller also mounts `experiments/policy/demo-policy.yaml` and exposes `PolicyService`. SDK clients load the initial policy at dial time and watch updates for retry budget, per-method timeout, and idempotency-aware retry behavior. To make the registry survive controller restarts during local experiments, start the stack with:

```bash
AEGIS_REGISTRY_BACKEND=file make experiments-up
```

Use `make dashboard` if you also want Prometheus and Grafana.

## 2. Run The Required Matrix

For a new timestamped single-machine run that does not overwrite previous CSV files:

```bash
RUNS_DIR=experiments/results/runs REQUESTS=1000 CONCURRENCY=32 make bench-single-machine
```

Recommended single-machine retry/recovery run:

```bash
RUNS_DIR=experiments/results/runs \
REQUESTS=1000 \
CONCURRENCY=32 \
DELAY=300ms \
JITTER=50ms \
CPUS=0.20 \
LOSS=5 \
RECOVERY_DURATION=60s \
make bench-single-machine
```

The retry comparison uses `retry-user-service`, a demo service that always returns `UNAVAILABLE`. `frontend-retry-unbudgeted` shows two-attempt retry behavior without a budget. `frontend-retry-budgeted` runs the same failure with budget admission enabled.

For a direct run into `experiments/results`:

```bash
REQUESTS=1000 CONCURRENCY=32 make bench-required
```

The matrix is defined in `experiments/config/experiment_matrix.json`.
During the packet-loss eBPF comparison, the script pauses before `packet_loss/ebpf_network_score`. Start `cmd/agent` in another terminal at that point. For non-interactive smoke runs, set `WAIT_FOR_EBPF=false`.

Required comparisons:

| Experiment | Required variants | What it checks |
| --- | --- | --- |
| `baseline` | `no_mesh`, `aegismesh` | no-fault overhead and baseline latency |
| `single_instance_delay` | `round_robin`, `adaptive_p2c` | slow instance routing benefit |
| `cpu_throttle` | `static_threshold`, `slow_score` | slow_score vs non-adaptive baseline |
| `retry_budget` | `without_budget`, `with_budget` | retry amplification control |
| `packet_loss` | `no_ebpf_network_score`, `ebpf_network_score` | value of kernel TCP signals |
| `recovery_curve` | `adaptive_p2c` | slow_score, route weight, state, and p99 over time |

## 3. eBPF Packet-Loss Run

For `packet_loss/ebpf_network_score`, start the eBPF agent before the second packet-loss benchmark:

```bash
make -C agent/ebpf/bpf
sudo go run ./cmd/agent \
  --controller 127.0.0.1:9000 \
  --object agent/ebpf/bpf/tcp_metrics.bpf.o \
  --endpoint-map "127.0.0.1:7001=user-service/user-a,127.0.0.1:7002=user-service/user-b"
```

If the demo runs inside Docker, map the container IP and port visible from the host or run the agent in the same Linux namespace used for traffic observation.

## 4. Validate Results

```bash
make check-results
```

This checks that all required scenario/variant pairs have rows and prints the derived p99 and retry amplification comparisons.

For partial smoke runs:

```bash
python experiments/scripts/check_results.py --allow-partial
```

## 5. Generate Figures

```bash
make report
```

Generated files are written under `experiments/results/figures/`.

## 6. Merge Old And New Results

After running one or more timestamped runs, combine the previous flat result files and the new run directories:

```bash
make merge-results
```

The merged outputs are written to `experiments/results/combined` with a `run_id` column added to each CSV. Use this combined directory for the project report:

```bash
python experiments/scripts/check_results.py --results experiments/results/combined
python experiments/notebooks/plot_latency.py --results experiments/results/combined --out experiments/results/combined/figures
```

The flat files under `experiments/results/*.csv` are treated as `run_id=legacy`; each new timestamped run keeps its directory name as `run_id`.

## 7. Repeat Retry Budget Experiments

Start the experiment stack first:

```bash
make experiments-up
```

Then run five retry-only repetitions:

```bash
RUNS_DIR=experiments/results/runs \
RUN_ID=retry-$(date +%Y%m%d-%H%M%S) \
REQUESTS=1000 \
CONCURRENCY=32 \
REPETITIONS=5 \
make bench-retry-repeat
```

Expected direction, not a hard proof by itself:

- `without_budget`: amplification is expected to be close to `2.0x`
- `with_budget`: amplification is expected to be close to `1.15x`

Merge and inspect:

```bash
make merge-results
python experiments/scripts/check_results.py --results experiments/results/combined
```

For the retry conclusion, prefer the retry-only runs over legacy rows where retry was not triggered.

## 8. Single-Machine Recovery State Experiment

Recovery state transitions can be simulated on one machine, but the default thresholds are conservative. For a local run, restart the experiment stack with lower thresholds:

```bash
make experiments-down

AEGIS_DEGRADED_THRESHOLD=0.5 \
AEGIS_EJECT_THRESHOLD=0.8 \
AEGIS_CONSECUTIVE_WINDOWS=1 \
AEGIS_EJECTION_DURATION=10s \
AEGIS_RECOVERY_THRESHOLD=0.3 \
AEGIS_HEALTH_LATENCY_SLO=0s \
make experiments-up
```

`AEGIS_HEALTH_LATENCY_SLO=0s` keeps the recovery experiment focused on relative slow_score and endpoint-state behavior. Dedicated probe-ratio and absolute-SLO experiments are listed below.

Then run the dedicated recovery-state experiment:

```bash
RUNS_DIR=experiments/results/runs \
RUN_ID=recovery-$(date +%Y%m%d-%H%M%S) \
CONCURRENCY=16 \
DELAY=800ms \
JITTER=150ms \
PRE_DURATION=20s \
FAULT_DURATION=120s \
POST_DURATION=40s \
RECOVERY_DURATION=190s \
make bench-recovery-state
```

Evidence to look for:

- `recovery.csv` should include a nonzero slow_score for the delayed endpoint.
- With the lowered thresholds, `state` should move beyond `HEALTHY`, ideally to `DEGRADED` or `EJECTED`, then toward `PROBING` / `HEALTHY` after reset.
- `route_weight` should fall while the fault is active.

Validate:

```bash
latest=$(ls -td experiments/results/runs/*recovery* | head -1)
python experiments/scripts/check_results.py --results "$latest" --allow-partial
grep -E 'DEGRADED|EJECTED|PROBING' "$latest/recovery.csv"
```

## 9. PROBING Probe-Ratio Experiment

This checks that a `PROBING` endpoint does not immediately get normal traffic again. The script uses the real SDK trace file from `frontend-adaptive` plus Controller health samples. The probe ratio is calculated from trace rows inside the Controller's `PROBING` window.

Start the stack with aggressive local thresholds so the endpoint reaches `PROBING` in a short run:

```bash
make experiments-down

AEGIS_DEGRADED_THRESHOLD=0.5 \
AEGIS_EJECT_THRESHOLD=0.8 \
AEGIS_CONSECUTIVE_WINDOWS=1 \
AEGIS_EJECTION_DURATION=10s \
AEGIS_RECOVERY_THRESHOLD=0.3 \
AEGIS_HEALTH_LATENCY_SLO=0s \
make experiments-up
```

Run the probe-ratio benchmark:

```bash
RUN_ID=probe-ratio-$(date +%Y%m%d-%H%M%S) \
RUNS_DIR=experiments/results/runs \
CONCURRENCY=32 \
DELAY=800ms \
JITTER=150ms \
PRE_DURATION=20s \
FAULT_DURATION=80s \
POST_DURATION=70s \
RECOVERY_DURATION=180s \
MAX_PROBE_RATIO=0.10 \
make bench-probe-ratio
```

The script waits for `frontend-adaptive` before starting the recorder and load generator. If an old copy of the script fails with `ConnectionResetError: [Errno 104] Connection reset by peer`, sync the latest `run_sustained_load.py`, `wait_for_http.py`, and experiment shell scripts; connection resets during startup are now counted as failed requests instead of aborting the run.

Inspect:

```bash
latest=$(ls -td experiments/results/runs/*probe-ratio* | head -1)
cat "$latest/probe_ratio_summary.json"
grep PROBING "$latest/recovery.csv"
```

Evidence to look for:

- `recovery.csv` contains `PROBING` rows for the delayed endpoint, usually port `7002`.
- `probe_ratio_summary.json` reports `within_expected_bound: true`.
- Look for a small `probe_ratio`. The implementation default is 2%, but this experiment uses `MAX_PROBE_RATIO=0.10` as a tolerant upper bound because sampling windows and resolver refresh timing are coarse on one machine.

If there are no `PROBING` rows, increase `FAULT_DURATION` or confirm the lowered thresholds are active. If there are no trace rows, check that `frontend-adaptive` is running with `--trace-log /traces/frontend-adaptive.jsonl`.

## 10. Absolute SLO Slow-Score Experiment

This checks whether slow_score can detect a one-instance or all-slow service when there is no relative peer outlier. Run it twice: first with absolute SLO disabled, then with it enabled.

Disabled run:

```bash
make experiments-down

AEGIS_DEGRADED_THRESHOLD=1.0 \
AEGIS_EJECT_THRESHOLD=1.8 \
AEGIS_CONSECUTIVE_WINDOWS=1 \
AEGIS_EJECTION_DURATION=10s \
AEGIS_RECOVERY_THRESHOLD=0.3 \
AEGIS_HEALTH_LATENCY_SLO=0s \
make experiments-up

RUN_ID=absolute-slo-disabled-$(date +%Y%m%d-%H%M%S) \
VARIANT=without_absolute_slo \
RUNS_DIR=experiments/results/runs \
TARGETS="aegis-user-a aegis-user-b" \
CONCURRENCY=24 \
DELAY=500ms \
JITTER=0ms \
FAULT_DURATION=70s \
RECOVERY_DURATION=110s \
MIN_SCORE=1.0 \
make bench-absolute-slo || true
```

The disabled run may fail the analyzer because no state transition is expected when relative scores stay low. Keep its output directory as negative-control evidence.

Enabled run:

```bash
make experiments-down

AEGIS_DEGRADED_THRESHOLD=1.0 \
AEGIS_EJECT_THRESHOLD=1.8 \
AEGIS_CONSECUTIVE_WINDOWS=1 \
AEGIS_EJECTION_DURATION=10s \
AEGIS_RECOVERY_THRESHOLD=0.3 \
AEGIS_HEALTH_LATENCY_SLO=100ms \
make experiments-up

RUN_ID=absolute-slo-enabled-$(date +%Y%m%d-%H%M%S) \
VARIANT=with_absolute_slo \
RUNS_DIR=experiments/results/runs \
TARGETS="aegis-user-a aegis-user-b" \
CONCURRENCY=24 \
DELAY=500ms \
JITTER=0ms \
FAULT_DURATION=70s \
RECOVERY_DURATION=110s \
MIN_SCORE=1.0 \
make bench-absolute-slo
```

Inspect:

```bash
latest=$(ls -td experiments/results/runs/*absolute-slo*with_absolute_slo* | head -1)
cat "$latest/absolute_slo_summary.json"
cut -d, -f8 "$latest/recovery.csv" | sort | uniq -c
```

Evidence to look for:

- Disabled run: expect a lower max slow_score, or no degraded/ejected/probing state.
- Enabled run: `absolute_slo_summary.json` should report `score_pass: true` and `state_pass: true`.
- Both `user-a` and `user-b` can show elevated slow_score because both are delayed; this is the point of the all-slow absolute SLO test.

After the probe-ratio run and the two absolute-SLO runs finish, generate a compact Markdown summary:

```bash
make summarize-probe-slo
cat experiments/results/probe_slo_summary.md
```

Keep this file with the JSON summaries when updating the report. The benchmark report still uses `experiments/results/combined`; these newer runs add evidence for probe ratio and absolute SLO scoring, but they do not replace the latency/retry/recovery matrix.

The checked-in supplemental evidence currently records:

- `probe-ratio`: measured probe ratio `0.002177`, or `0.2177%`, with 560 probing-endpoint traces out of 257258 user-service traces.
- `absolute-slo-disabled`: max slow_score `0.377401`, all samples `HEALTHY`.
- `absolute-slo-enabled`: max slow_score `1.007183`, states `DEGRADED` and `HEALTHY`.

## 11. DeathStarBench Runner

DeathStarBench support is now an opt-in runner around an external checkout. It is not part of the single-machine benchmark matrix above, and a generated integration plan is not benchmark evidence.

Generate the plan without starting Docker:

```bash
make deathstarbench-plan
```

Run against a local DeathStarBench checkout:

```bash
make deathstarbench-run DSB_REPO_DIR=/path/to/DeathStarBench
```

By default this writes artifacts to:

```text
experiments/results/runs/deathstarbench-social-network-<timestamp>/
```

Expected artifacts:

- `integration_plan.json`: compose, workload, frontend, readiness, and AegisMesh environment plan.
- `aegis-compose.override.yml`: generated metadata overlay for mapped DeathStarBench services.
- `run_manifest.json`: stage status, commands, artifact paths, injection mode, and traffic-governance status.
- `workload_stdout.txt` and `workload_stderr.txt`: raw workload output.
- `compose_ps.json` and `compose_logs.txt`: Docker Compose collection outputs.
- `latency.csv`: parsed workload latency row using the same schema as `experiments/results/latency_schema.csv`.

Validate the run directory:

```bash
make check-deathstarbench-run DSB_RUN_DIR=experiments/results/runs/deathstarbench-social-network-<timestamp>
```

The default generated overlay injects `AEGIS_CONTROLLER`, `AEGIS_SERVICE_MAP`, per-service Aegis names, instance IDs, ports, and labels. This makes the run auditable and prepares the service map, but it does not by itself route DeathStarBench's non-Go/non-gRPC service calls through the Go SDK. Treat `traffic_governance=metadata_only` as runner evidence, not as an AegisMesh routing benchmark. A future sidecar/proxy or service rewrite should set the manifest to sidecar/proxy governed traffic; only then should validation use `--require-governed-traffic`.
## 12. Reporting rules

- Report `round_robin` vs `adaptive_p2c` only after both rows exist for the same fault setup.
- Report retry budget benefit only after both `without_budget` and `with_budget` rows exist.
- Report recovery time only when `recovery.csv` includes fault start, affected endpoint slow_score increase, state or weight change, and post-reset recovery.
- Report eBPF benefit only when `cmd/agent` was running for the `ebpf_network_score` variant.
- Do not use checked-in schema files as results.

## 13. Real SDK Trace Verifier

The verifier can read trace JSONL generated by the SDK, not just hand-written examples. `frontend-adaptive` in `docker-compose.experiments.yml` starts `demo-frontend` with:

```text
--trace-log /traces/frontend-adaptive.jsonl
```

and mounts that path to `experiments/traces/` on the host. To verify real runtime traces:

```bash
rm -f experiments/traces/frontend-adaptive.jsonl
make experiments-up
for i in $(seq 1 20); do curl -fsS 'http://127.0.0.1:8083/checkout' > /dev/null; done
go run ./cmd/verifier --spec experiments/verifier/real-trace-smoke.yaml --traces experiments/traces/frontend-adaptive.jsonl
```

The generated records are verifier-compatible JSONL. Extra fields such as `span_id`, `source`, `destination`, `method`, `upstream`, and `attempt` are kept for debugging. The verifier still checks the stable fields: `trace_id`, `route`, `path`, `retry_attempts`, and `status`.

## 14. Microbenchmark Baselines

Hot-path baselines live in `benchmarks/baseline/` and are captured by:

```bash
bash benchmarks/baseline/run_full_baseline.sh
```

The script parallel-runs `scripts/run_microbench.sh capture` for hot-path packages, then `capture-snapshots` to shard `SnapshotAndReset` grids (telemetry `upstreams ∈ {1,8,64}`, ebpf `endpoints ∈ {1,8,64}`, observations `∈ {1K,10K,100K}`, count=10), and finally `race`.

Findings from the latest full run:

- Hot-path numbers and snapshot grids are reproducible; results are in `*_main.txt` and `telemetry_snapshot_main.txt` / `ebpf_snapshot_main.txt`.
- `go test -race` is green for all 7 benchmarked packages (`sdk/go/aegisgrpc`, `pkg/retry`, `pkg/circuitbreaker`, `pkg/telemetry`, `pkg/registry`, `pkg/policy`, `agent/ebpf`). No data races in production code.
- Two test-only flakes were fixed during this run, both in test code, not production:
  - `pkg/telemetry/stats_pool_test.go`: relied on `sync.Pool` retaining a `Put`, which is not guaranteed under `-race` (random drop, per-P caches). The test now pins to `GOMAXPROCS=1` and retries.
  - `pkg/registry/memory_registry_test.go` (`TestMemoryRegistryWatchCoalescesUpdatesForSlowConsumer`): the test's clock closure mutated a shared `now` from the main goroutine while the watcher read it. Guarded the variable with a mutex; the `MemoryRegistry` contract (clock may be called concurrently) is unchanged.

