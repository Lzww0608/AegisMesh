#!/usr/bin/env bash
set -euo pipefail

# Run the retry-budget latency variant and append a retry.csv marker row.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RESULTS_DIR="${RESULTS_DIR:-experiments/results}"
mkdir -p "$RESULTS_DIR"

EXPERIMENT=retry_budget VARIANT=with_budget RESULTS_DIR="$RESULTS_DIR" bash experiments/scripts/run_baseline.sh

python - "$RESULTS_DIR/retry.csv" <<'PY'
import csv
import os
import sys
import time

path = sys.argv[1]
exists = os.path.exists(path)
with open(path, "a", newline="", encoding="utf-8") as f:
    writer = csv.writer(f)
    if not exists:
        writer.writerow([
            "experiment",
            "variant",
            "window_start_unix_ms",
            "original_requests",
            "retry_attempts",
            "total_attempts",
            "retry_amplification",
            "error_rate",
        ])
    now = int(time.time() * 1000)
    writer.writerow(["retry_budget", "with_budget", now, "", "", "", "", ""])
PY
