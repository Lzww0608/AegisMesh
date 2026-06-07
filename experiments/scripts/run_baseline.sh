#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RESULTS_DIR="${RESULTS_DIR:-experiments/results}"
FRONTEND_URL="${FRONTEND_URL:-http://127.0.0.1:8080/checkout}"
REQUESTS="${REQUESTS:-200}"
CONCURRENCY="${CONCURRENCY:-16}"
EXPERIMENT="${EXPERIMENT:-baseline}"
VARIANT="${VARIANT:-aegismesh}"

mkdir -p "$RESULTS_DIR"
python experiments/scripts/run_http_benchmark.py \
  --url "$FRONTEND_URL" \
  --requests "$REQUESTS" \
  --concurrency "$CONCURRENCY" \
  --experiment "$EXPERIMENT" \
  --variant "$VARIANT" \
  --latency-out "$RESULTS_DIR/latency.csv"
