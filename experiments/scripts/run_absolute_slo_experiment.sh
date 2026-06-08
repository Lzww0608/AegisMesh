#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
RUNS_DIR="${RUNS_DIR:-experiments/results/runs}"
VARIANT="${VARIANT:-with_absolute_slo}"
RESULTS_DIR="${RESULTS_DIR:-$RUNS_DIR/${RUN_ID}-absolute-slo-$VARIANT}"
CONCURRENCY="${CONCURRENCY:-24}"
TARGETS="${TARGETS:-aegis-user-a aegis-user-b}"
DEVICE="${DEVICE:-eth0}"
DELAY="${DELAY:-500ms}"
JITTER="${JITTER:-0ms}"
PRE_DURATION="${PRE_DURATION:-15s}"
FAULT_DURATION="${FAULT_DURATION:-70s}"
POST_DURATION="${POST_DURATION:-20s}"
RECOVERY_DURATION="${RECOVERY_DURATION:-110s}"
RECOVERY_INTERVAL="${RECOVERY_INTERVAL:-1s}"
URL_ADAPTIVE="${URL_ADAPTIVE:-http://127.0.0.1:8083/checkout}"
MIN_SCORE="${MIN_SCORE:-1.0}"

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
    exit 1
  fi
}

reset_targets() {
  for target in $TARGETS; do
    TARGET="$target" DEVICE="$DEVICE" bash scripts/reset_faults.sh || true
  done
}

mkdir -p "$RESULTS_DIR"
cat > "$RESULTS_DIR/run_meta.txt" <<EOF
run_id=${RUN_ID}-absolute-slo-$VARIANT
kind=absolute_slo
variant=$VARIANT
concurrency=$CONCURRENCY
targets=$TARGETS
delay=$DELAY
jitter=$JITTER
min_score=$MIN_SCORE
created_at=$(date -Is)
host=$(hostname)
EOF

trap reset_targets EXIT

python experiments/scripts/wait_for_http.py --url "$URL_ADAPTIVE" --timeout 90s --interval 1s

go run ./cmd/experiment-recorder \
  --experiment absolute_slo \
  --variant "$VARIANT" \
  --duration "$RECOVERY_DURATION" \
  --interval "$RECOVERY_INTERVAL" \
  --out "$RESULTS_DIR/recovery.csv" &
recorder_pid=$!

python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$PRE_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment absolute_slo \
  --variant "${VARIANT}_pre_fault" \
  --latency-out "$RESULTS_DIR/latency.csv"

for target in $TARGETS; do
  KIND=delay TARGET="$target" DEVICE="$DEVICE" DELAY="$DELAY" JITTER="$JITTER" EXECUTE=true bash scripts/run_fault_experiment.sh
done

python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$FAULT_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment absolute_slo \
  --variant "$VARIANT" \
  --latency-out "$RESULTS_DIR/latency.csv"

reset_targets

python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$POST_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment absolute_slo \
  --variant "${VARIANT}_post_fault" \
  --latency-out "$RESULTS_DIR/latency.csv"

wait "$recorder_pid"
assert_recovery_rows "$RESULTS_DIR/recovery.csv"

python experiments/scripts/analyze_absolute_slo.py \
  --recovery "$RESULTS_DIR/recovery.csv" \
  --out "$RESULTS_DIR/absolute_slo_summary.json" \
  --min-score "$MIN_SCORE"

python experiments/scripts/check_results.py --results "$RESULTS_DIR" --allow-partial
echo "absolute SLO run written to $RESULTS_DIR"
