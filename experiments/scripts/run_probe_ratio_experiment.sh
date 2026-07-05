#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
RUNS_DIR="${RUNS_DIR:-experiments/results/runs}"
RESULTS_DIR="${RESULTS_DIR:-$RUNS_DIR/${RUN_ID}-probe-ratio}"
CONCURRENCY="${CONCURRENCY:-32}"
TARGET="${TARGET:-aegis-user-b}"
DEVICE="${DEVICE:-eth0}"
DELAY="${DELAY:-800ms}"
JITTER="${JITTER:-150ms}"
PRE_DURATION="${PRE_DURATION:-20s}"
FAULT_DURATION="${FAULT_DURATION:-80s}"
POST_DURATION="${POST_DURATION:-70s}"
RECOVERY_DURATION="${RECOVERY_DURATION:-180s}"
RECOVERY_INTERVAL="${RECOVERY_INTERVAL:-1s}"
URL_ADAPTIVE="${URL_ADAPTIVE:-http://127.0.0.1:8083/checkout}"
TRACE_LOG="${TRACE_LOG:-experiments/traces/frontend-adaptive.jsonl}"
PROBING_PORT="${PROBING_PORT:-7002}"
MAX_PROBE_RATIO="${MAX_PROBE_RATIO:-0.10}"

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
    exit 1
  fi
}

mkdir -p "$RESULTS_DIR" "$(dirname "$TRACE_LOG")"
: > "$TRACE_LOG"

cat > "$RESULTS_DIR/run_meta.txt" <<EOF
run_id=${RUN_ID}-probe-ratio
kind=probe_ratio
concurrency=$CONCURRENCY
target=$TARGET
delay=$DELAY
jitter=$JITTER
probing_port=$PROBING_PORT
max_probe_ratio=$MAX_PROBE_RATIO
created_at=$(date -Is)
host=$(hostname)
EOF

python experiments/scripts/wait_for_http.py --url "$URL_ADAPTIVE" --timeout 90s --interval 1s

go run ./cmd/experiment-recorder \
  --experiment probe_ratio \
  --variant adaptive_p2c \
  --duration "$RECOVERY_DURATION" \
  --interval "$RECOVERY_INTERVAL" \
  --out "$RESULTS_DIR/recovery.csv" &
recorder_pid=$!

python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$PRE_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment probe_ratio \
  --variant pre_fault \
  --latency-out "$RESULTS_DIR/latency.csv"

KIND=delay TARGET="$TARGET" DEVICE="$DEVICE" DELAY="$DELAY" JITTER="$JITTER" EXECUTE=true bash scripts/run_fault_experiment.sh
python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$FAULT_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment probe_ratio \
  --variant fault \
  --latency-out "$RESULTS_DIR/latency.csv"

TARGET="$TARGET" DEVICE="$DEVICE" bash scripts/reset_faults.sh
python experiments/scripts/run_sustained_load.py \
  --url "$URL_ADAPTIVE" \
  --duration "$POST_DURATION" \
  --concurrency "$CONCURRENCY" \
  --experiment probe_ratio \
  --variant post_fault \
  --latency-out "$RESULTS_DIR/latency.csv"

wait "$recorder_pid"
assert_recovery_rows "$RESULTS_DIR/recovery.csv"

python experiments/scripts/analyze_probe_ratio.py \
  --recovery "$RESULTS_DIR/recovery.csv" \
  --trace "$TRACE_LOG" \
  --out "$RESULTS_DIR/probe_ratio_summary.json" \
  --probing-port "$PROBING_PORT" \
  --max-probe-ratio "$MAX_PROBE_RATIO"

python experiments/scripts/check_results.py --results "$RESULTS_DIR" --allow-partial
echo "probe ratio run written to $RESULTS_DIR"
