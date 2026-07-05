#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOG_DIR="${ROOT}/benchmarks/baseline"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
LOG="${LOG_DIR}/run_full_${STAMP}.log"
PID_FILE="${LOG_DIR}/run_full.pid"

# 32-core host default: 16 parallel shards, 2 cores per go test.
CAPTURE_JOBS="${CAPTURE_JOBS:-16}"
PER_JOB_GOMAXPROCS="${PER_JOB_GOMAXPROCS:-2}"
BENCH_COUNT="${BENCH_COUNT:-10}"

cd "${ROOT}"
echo "${BASHPID}" >"${PID_FILE}"
echo "=== AegisMesh full microbenchmark baseline (parallel) ===" | tee -a "${LOG}"
echo "started_at=${STAMP}" | tee -a "${LOG}"
echo "log=${LOG}" | tee -a "${LOG}"
echo "capture_jobs=${CAPTURE_JOBS} per_job_gomax=${PER_JOB_GOMAXPROCS} bench_count=${BENCH_COUNT}" | tee -a "${LOG}"
echo | tee -a "${LOG}"

# run_step runs the run step experiment step and records its outputs.
run_step() {
	local title="$1"
	shift
	echo ">>> ${title} ($(date -u +%Y-%m-%dT%H:%M:%SZ))" | tee -a "${LOG}"
	"$@" 2>&1 | tee -a "${LOG}"
	local code="${PIPESTATUS[0]}"
	if [[ "${code}" -ne 0 ]]; then
		echo "FAILED: ${title} (exit=${code})" | tee -a "${LOG}"
		echo "finished_at=$(date -u +%Y%m%dT%H%M%SZ) status=failed step=${title}" >>"${LOG}"
		rm -f "${PID_FILE}"
		exit "${code}"
	fi
	echo | tee -a "${LOG}"
}

export CAPTURE_JOBS PER_JOB_GOMAXPROCS BENCH_COUNT

run_step "capture main (parallel)" bash scripts/run_microbench.sh capture main
run_step "capture snapshots (parallel shards)" bash scripts/run_microbench.sh capture-snapshots main
run_step "microbench-race (hot-path)" make microbench-race
run_step "test-race (full repo)" make test-race

echo "finished_at=$(date -u +%Y%m%dT%H%M%SZ) status=success" | tee -a "${LOG}"
echo "Baseline artifacts under ${LOG_DIR}" | tee -a "${LOG}"
rm -f "${PID_FILE}"
