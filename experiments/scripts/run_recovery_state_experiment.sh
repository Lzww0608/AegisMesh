#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
RUNS_DIR="${RUNS_DIR:-experiments/results/runs}"
RESULTS_DIR="${RESULTS_DIR:-$RUNS_DIR/${RUN_ID}-recovery}"
CONCURRENCY="${CONCURRENCY:-32}"
TARGET="${TARGET:-aegis-user-b}"
DEVICE="${DEVICE:-eth0}"
DELAY="${DELAY:-500ms}"
JITTER="${JITTER:-100ms}"
PRE_DURATION="${PRE_DURATION:-15s}"
FAULT_DURATION="${FAULT_DURATION:-60s}"
POST_DURATION="${POST_DURATION:-30s}"
RECOVERY_DURATION="${RECOVERY_DURATION:-90s}"
RECOVERY_INTERVAL="${RECOVERY_INTERVAL:-1s}"
URL_ADAPTIVE="${URL_ADAPTIVE:-http://127.0.0.1:8083/checkout}"

# assert_recovery_rows checks that required experiment artifacts are present before continuing.
assert_recovery_rows() {
  local path="$1"
  if [[ ! -s "$path" ]]; then
    echo "recovery recorder wrote no endpoint rows: $path is missing or empty" >&2
    exit 1
  fi
  local rows
  rows=$(tail -n +2 "$path" | wc -l)
  if [[ "$rows" -le 0 ]]; then
    echo "recovery recorder wrote no endpoint rows: Controller health is empty" >&2
    echo "Check that frontend-adaptive is sending telemetry to controller and that controller is reachable at 127.0.0.1:9000." >&2
    exit 1
  fi
}

mkdir -p "$RESULTS_DIR"
cat > "$RESULTS_DIR/run_meta.txt" <<EOF
run_id=${RUN_ID}-recovery
kind=recovery_state
concurrency=$CONCURRENCY
delay=$DELAY
jitter=$JITTER
created_at=$(date -Is)
host=$(hostname)
EOF

go run ./cmd/experiment-recorder \
  --experiment recovery_curve \
  --variant adaptive_p2c \
  --duration "$RECOVERY_DURATION" \
  --interval "$RECOVERY_INTERVAL" \
  --out "$RESULTS_DIR/recovery.csv" &
recorder_pid=$!

python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$PRE_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment recovery_curve \
  --variant adaptive_p2c \
  --latency-out "$RESULTS_DIR/latency.csv"

KIND=delay TARGET="$TARGET" DEVICE="$DEVICE" DELAY="$DELAY" JITTER="$JITTER" EXECUTE=true bash scripts/run_fault_experiment.sh
python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$FAULT_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment recovery_curve \
  --variant adaptive_p2c \
  --latency-out "$RESULTS_DIR/latency.csv"

TARGET="$TARGET" DEVICE="$DEVICE" bash scripts/reset_faults.sh
python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$POST_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment recovery_curve \
  --variant adaptive_p2c \
  --latency-out "$RESULTS_DIR/latency.csv"

wait "$recorder_pid"
assert_recovery_rows "$RESULTS_DIR/recovery.csv"
python experiments/scripts/check_results.py --results "$RESULTS_DIR" --allow-partial
echo "recovery state run written to $RESULTS_DIR"
