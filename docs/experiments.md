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

AEGIS_DEGRADED_THRESHOLD=0.05 \
AEGIS_EJECT_THRESHOLD=0.09 \
AEGIS_CONSECUTIVE_WINDOWS=2 \
AEGIS_EJECTION_DURATION=10s \
AEGIS_RECOVERY_THRESHOLD=0.03 \
make experiments-up
```

Then run the dedicated recovery-state experiment:

```bash
RUNS_DIR=experiments/results/runs \
RUN_ID=recovery-$(date +%Y%m%d-%H%M%S) \
CONCURRENCY=32 \
DELAY=500ms \
JITTER=100ms \
PRE_DURATION=15s \
FAULT_DURATION=60s \
POST_DURATION=30s \
RECOVERY_DURATION=90s \
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

## 9. Reporting Rules

- Report `round_robin` vs `adaptive_p2c` only after both rows exist for the same fault setup.
- Report retry budget benefit only after both `without_budget` and `with_budget` rows exist.
- Report recovery time only when `recovery.csv` includes fault start, affected endpoint slow_score increase, state or weight change, and post-reset recovery.
- Report eBPF benefit only when `cmd/agent` was running for the `ebpf_network_score` variant.
- Do not use checked-in schema files as results.
