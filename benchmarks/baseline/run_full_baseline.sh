#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOG_DIR="${ROOT}/benchmarks/baseline"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
LOG="${LOG_DIR}/run_full_${STAMP}.log"
PID_FILE="${LOG_DIR}/run_full.pid"

cd "${ROOT}"
echo "${BASHPID}" >"${PID_FILE}"
echo "=== AegisMesh full microbenchmark baseline ===" | tee -a "${LOG}"
echo "started_at=${STAMP}" | tee -a "${LOG}"
echo "log=${LOG}" | tee -a "${LOG}"
echo "cwd=${ROOT}" | tee -a "${LOG}"
echo "go=$(go version 2>&1)" | tee -a "${LOG}"
echo "gomaxprocs=${GOMAXPROCS:-default}" | tee -a "${LOG}"
echo | tee -a "${LOG}"

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

run_step "microbench-baseline-full" make microbench-baseline-full
run_step "microbench-race (hot-path)" make microbench-race
run_step "test-race (full repo)" make test-race

echo "finished_at=$(date -u +%Y%m%dT%H%M%SZ) status=success" | tee -a "${LOG}"
echo "Baseline artifacts under ${LOG_DIR}" | tee -a "${LOG}"
rm -f "${PID_FILE}"
