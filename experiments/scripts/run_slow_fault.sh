#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

TARGET="${TARGET:-aegis-user-b}"
DELAY="${DELAY:-200ms}"
JITTER="${JITTER:-50ms}"
RESULTS_DIR="${RESULTS_DIR:-experiments/results}"

KIND=delay TARGET="$TARGET" DELAY="$DELAY" JITTER="$JITTER" EXECUTE=true bash scripts/run_fault_experiment.sh
EXPERIMENT=single_instance_delay VARIANT=adaptive_p2c RESULTS_DIR="$RESULTS_DIR" bash experiments/scripts/run_baseline.sh
TARGET="$TARGET" bash scripts/reset_faults.sh
