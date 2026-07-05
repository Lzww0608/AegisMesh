#!/usr/bin/env bash
set -euo pipefail

# Run the single-machine experiment sequence and write merged result inputs.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
RUNS_DIR="${RUNS_DIR:-experiments/results/runs}"
RESULTS_DIR="${RESULTS_DIR:-$RUNS_DIR/$RUN_ID}"
REQUESTS="${REQUESTS:-1000}"
CONCURRENCY="${CONCURRENCY:-32}"

mkdir -p "$RESULTS_DIR"
cat > "$RESULTS_DIR/run_meta.txt" <<EOF
run_id=$RUN_ID
requests=$REQUESTS
concurrency=$CONCURRENCY
created_at=$(date -Is)
host=$(hostname)
EOF

RESULTS_DIR="$RESULTS_DIR" REQUESTS="$REQUESTS" CONCURRENCY="$CONCURRENCY" bash experiments/scripts/run_required_experiments.sh
python experiments/scripts/check_results.py --results "$RESULTS_DIR"

echo "single-machine run written to $RESULTS_DIR"
echo "merge with: python experiments/scripts/merge_results.py --inputs experiments/results $RUNS_DIR --out experiments/results/combined"
