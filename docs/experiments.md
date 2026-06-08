# AegisMesh Experiment Guide

This guide describes how to generate the benchmark data needed for a credible project report on a single machine. Do not copy the sample commands into a resume until `check-results` passes on measured CSV files.

## 1. Start The Experiment Stack

```bash
make experiments-up
```

This starts:

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

The retry comparison uses `retry-user-service`, a dedicated demo service that always returns `UNAVAILABLE`. `frontend-retry-unbudgeted` measures unbounded two-attempt retry behavior, while `frontend-retry-budgeted` measures retry-budget admission.

For a direct run into `experiments/results`:

```bash
REQUESTS=1000 CONCURRENCY=32 make bench-required
```

The matrix is defined in `experiments/config/experiment_matrix.json`.
During the packet-loss eBPF comparison, the script pauses before `packet_loss/ebpf_network_score` so you can start `cmd/agent` in another terminal. For non-interactive smoke runs, set `WAIT_FOR_EBPF=false`.

Required comparisons:

| Experiment | Required variants | Purpose |
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

This checks that all required scenario/variant pairs have evidence rows and prints derived p99 and retry amplification comparisons.

For partial smoke runs:

```bash
python experiments/scripts/check_results.py --allow-partial
```

## 5. Generate Figures

```bash
make report
```

Generated files go under `experiments/results/figures/`.

## 6. Merge Old And New Results

After running one or more timestamped runs, combine the previous flat result files and the new run directories:

```bash
make merge-results
```

The merged outputs are written to `experiments/results/combined` with a `run_id` column added to each CSV. Use this combined directory for the final report:

```bash
python experiments/scripts/check_results.py --results experiments/results/combined
python experiments/notebooks/plot_latency.py --results experiments/results/combined --out experiments/results/combined/figures
```

The existing flat files under `experiments/results/*.csv` are treated as `run_id=legacy`; each new timestamped run keeps its directory name as `run_id`.

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

Expected direction:

- `without_budget`: amplification should be close to `2.0x`
- `with_budget`: amplification should be close to `1.15x`

Merge and inspect:

```bash
make merge-results
python experiments/scripts/check_results.py --results experiments/results/combined
```

For the final retry conclusion, prefer the retry-only runs over legacy rows where retry was not triggered.

## 8. Single-Machine Recovery State Experiment

Recovery state transitions are possible on one machine, but the default production thresholds are intentionally conservative. For a local demonstration, restart the experiment stack with lower thresholds:

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

Expected evidence:

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

This validates that a `PROBING` endpoint is not immediately returned to normal traffic. The experiment uses the real SDK trace file from `frontend-adaptive` plus Controller health samples.
The measured probe ratio is calculated from trace rows that fall inside the Controller's `PROBING` state window.

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

Inspect:

```bash
latest=$(ls -td experiments/results/runs/*probe-ratio* | head -1)
cat "$latest/probe_ratio_summary.json"
grep PROBING "$latest/recovery.csv"
```

Expected evidence:

- `recovery.csv` contains `PROBING` rows for the delayed endpoint, usually port `7002`.
- `probe_ratio_summary.json` reports `within_expected_bound: true`.
- `probe_ratio` should be small. The implementation default is 2%, but this experiment uses `MAX_PROBE_RATIO=0.10` as a tolerant upper bound because sampling windows and resolver refresh timing are coarse on one machine.

If there are no `PROBING` rows, increase `FAULT_DURATION` or confirm the lowered thresholds are active. If there are no trace rows, check that `frontend-adaptive` is running with `--trace-log /traces/frontend-adaptive.jsonl`.

## 10. Absolute SLO Slow-Score Experiment

This validates that slow_score can detect a one-instance or all-slow service even when there is no relative peer outlier. Run it twice: once with absolute SLO disabled, then once with it enabled.

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

Expected evidence:

- Disabled run: max slow_score should be lower or no degraded/ejected/probing state should appear.
- Enabled run: `absolute_slo_summary.json` should report `score_pass: true` and `state_pass: true`.
- Both `user-a` and `user-b` can show elevated slow_score because both are delayed; this is the point of the all-slow absolute SLO test.

After the probe-ratio run and the two absolute-SLO runs finish, generate a compact Markdown summary:

```bash
make summarize-probe-slo
cat experiments/results/probe_slo_summary.md
```

Keep this file together with the JSON summaries when updating the project report. The existing benchmark report remains based on `experiments/results/combined`; these new runs add validation evidence for the two recently added mechanisms rather than replacing the earlier latency/retry/recovery matrix.

## 11. Reporting Rules

- Report `round_robin` vs `adaptive_p2c` only after both rows exist for the same fault setup.
- Report retry budget benefit only after both `without_budget` and `with_budget` rows exist.
- Report recovery time only when `recovery.csv` includes fault start, affected endpoint slow_score increase, state or weight change, and post-reset recovery.
- Report eBPF benefit only when `cmd/agent` was running for the `ebpf_network_score` variant.
- Do not use checked-in schema files as results.

## 12. Real SDK Trace Verifier

The verifier can read trace JSONL generated by the SDK rather than only hand-written examples. `frontend-adaptive` in `docker-compose.experiments.yml` starts `demo-frontend` with:

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

The generated records are verifier-compatible JSONL. Extra fields such as `span_id`, `source`, `destination`, `method`, `upstream`, and `attempt` are retained for debugging, while the verifier continues to validate `trace_id`, `route`, `path`, `retry_attempts`, and `status`.
