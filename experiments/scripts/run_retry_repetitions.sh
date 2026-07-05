#!/usr/bin/env bash
set -euo pipefail

# Repeat retry-amplification measurements into per-run result directories.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
RUNS_DIR="${RUNS_DIR:-experiments/results/runs}"
REQUESTS="${REQUESTS:-1000}"
CONCURRENCY="${CONCURRENCY:-32}"
REPETITIONS="${REPETITIONS:-5}"
URL_RETRY_UNBUDGETED="${URL_RETRY_UNBUDGETED:-http://127.0.0.1:8084/checkout}"
URL_RETRY_BUDGETED="${URL_RETRY_BUDGETED:-http://127.0.0.1:8086/checkout}"
METRICS_RETRY_UNBUDGETED="${METRICS_RETRY_UNBUDGETED:-http://127.0.0.1:8084/metrics}"
METRICS_RETRY_BUDGETED="${METRICS_RETRY_BUDGETED:-http://127.0.0.1:8086/metrics}"

for idx in $(seq 1 "$REPETITIONS"); do
  run_dir="$RUNS_DIR/${RUN_ID}-retry-${idx}"
  mkdir -p "$run_dir"
  cat > "$run_dir/run_meta.txt" <<EOF
run_id=${RUN_ID}-retry-${idx}
kind=retry
requests=$REQUESTS
concurrency=$CONCURRENCY
created_at=$(date -Is)
host=$(hostname)
EOF
  python experiments/scripts/run_retry_amplification.py \
    --without-url "$URL_RETRY_UNBUDGETED" \
    --with-url "$URL_RETRY_BUDGETED" \
    --without-metrics "$METRICS_RETRY_UNBUDGETED" \
    --with-metrics "$METRICS_RETRY_BUDGETED" \
    --requests "$REQUESTS" \
    --concurrency "$CONCURRENCY" \
    --latency-out "$run_dir/latency.csv" \
    --retry-out "$run_dir/retry.csv"
  python experiments/scripts/check_results.py --results "$run_dir" --allow-partial
done

echo "retry repetition runs written under $RUNS_DIR/${RUN_ID}-retry-*"
