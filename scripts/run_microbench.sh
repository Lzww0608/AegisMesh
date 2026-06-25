#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASELINE_DIR="${BASELINE_DIR:-$ROOT/benchmarks/baseline}"
COUNT="${BENCH_COUNT:-10}"
BENCHTIME="${BENCHTIME:-}"
GOMAXPROCS_VALUE="${GOMAXPROCS:-}"

usage() {
	cat <<'EOF'
Usage: scripts/run_microbench.sh <command>

Commands:
  capture [label]   Run microbenchmarks and write results under benchmarks/baseline/.
                    Optional label defaults to "main" (e.g. main, pr-123).
  race              Run race detector on hot-path packages (hard gate for balancer/retry/breaker).
  compare OLD NEW   Compare two bench result files with benchstat.
  adaptive OLD NEW  Compare adaptive balancer results at GOMAXPROCS=1 and GOMAXPROCS=8.

Environment:
  BENCH_COUNT       Iterations per benchmark (default: 10).
  BENCHTIME         Passed to go test -benchtime (optional).
  GOMAXPROCS        CPU limit for capture/adaptive runs (optional).
  BASELINE_DIR      Output directory (default: benchmarks/baseline).

Examples:
  make microbench-baseline
  make microbench-race
  BENCH_COUNT=10 make microbench-adaptive OLD=benchmarks/baseline/adaptive_gomax1_main.txt NEW=/tmp/adaptive.txt
  scripts/run_microbench.sh compare benchmarks/baseline/adaptive_gomax1_main.txt /tmp/adaptive.txt
EOF
}

bench_args() {
	local args=(-run=NONE -benchmem "-count=${COUNT}")
	if [[ -n "${BENCHTIME}" ]]; then
		args+=("-benchtime=${BENCHTIME}")
	fi
	printf '%s\n' "${args[@]}"
}

run_bench() {
	local output="$1"
	shift
	local -a args
	mapfile -t args < <(bench_args)
	if [[ -n "${GOMAXPROCS_VALUE}" ]]; then
		GOMAXPROCS="${GOMAXPROCS_VALUE}" go test "$@" "${args[@]}" >"${output}"
	else
		go test "$@" "${args[@]}" >"${output}"
	fi
}

capture_package() {
	local name="$1"
	local bench_filter="$2"
	local label="$3"
	shift 3
	local output="${BASELINE_DIR}/${name}_${label}.txt"
	mkdir -p "${BASELINE_DIR}"
	echo "==> ${name} -> ${output}"
	run_bench "${output}" "$@" -bench="${bench_filter}"
}

capture_all() {
	local label="${1:-main}"
	local telemetry_bench="."
	local ebpf_bench="."
	case "${label}" in
	quick)
		telemetry_bench="Observe|Prometheus"
		ebpf_bench="Decode|ObserveParallel"
		;;
	main)
		telemetry_bench="Observe|SnapshotAndReset/upstreams=1/observations=1000|SnapshotAndReset/upstreams=8/observations=1000|SnapshotAndReset/upstreams=64/observations=1000"
		ebpf_bench="Decode|ObserveParallel|SnapshotAndReset/endpoints=1/observations=1000|SnapshotAndReset/endpoints=8/observations=1000|SnapshotAndReset/endpoints=64/observations=1000"
		;;
	esac
	echo "Capturing microbenchmark baseline (label=${label}, count=${COUNT}, benchtime=${BENCHTIME:-default})"
	GOMAXPROCS_VALUE=1 capture_package adaptive_gomax1 "Adaptive" "${label}" ./sdk/go/aegisgrpc
	GOMAXPROCS_VALUE=8 capture_package adaptive_gomax8 "Adaptive" "${label}" ./sdk/go/aegisgrpc
	capture_package aegisgrpc_policy "Policy" "${label}" ./sdk/go/aegisgrpc
	capture_package aegisgrpc_retry "Retry" "${label}" ./sdk/go/aegisgrpc
	capture_package telemetry "${telemetry_bench}" "${label}" ./pkg/telemetry
	capture_package retry "." "${label}" ./pkg/retry
	capture_package circuitbreaker "." "${label}" ./pkg/circuitbreaker
	capture_package registry "Memory" "${label}" ./pkg/registry
	capture_package policy "." "${label}" ./pkg/policy
	capture_package ebpf "${ebpf_bench}" "${label}" ./agent/ebpf
	echo "Baseline written to ${BASELINE_DIR}"
}

run_race() {
	echo "Running race detector on hot-path packages..."
	go test -race ./sdk/go/aegisgrpc/ ./pkg/retry/ ./pkg/circuitbreaker/
	echo "Running race detector on remaining benchmarked packages..."
	go test -race ./pkg/telemetry/ ./pkg/registry/ ./pkg/policy/ ./agent/ebpf/
	echo "Race checks passed."
}

compare_adaptive() {
	local old="${1:?old result file}"
	local new="${2:?new result file}"
	if ! command -v benchstat >/dev/null 2>&1; then
		echo "benchstat not found; install with: go install golang.org/x/perf/cmd/benchstat@latest" >&2
		exit 1
	fi
	echo "==> GOMAXPROCS=1"
	GOMAXPROCS=1 benchstat "${old}" "${new}"
	echo "==> GOMAXPROCS=8"
	GOMAXPROCS=8 benchstat "${old}" "${new}"
}

main() {
	local cmd="${1:-}"
	case "${cmd}" in
	capture)
		capture_all "${2:-main}"
		;;
	race)
		run_race
		;;
	compare)
		shift
		if ! command -v benchstat >/dev/null 2>&1; then
			echo "benchstat not found; install with: go install golang.org/x/perf/cmd/benchstat@latest" >&2
			exit 1
		fi
		benchstat "$@"
		;;
	adaptive)
		shift
		compare_adaptive "$@"
		;;
	-h | --help | help | "")
		usage
		[[ -n "${cmd}" ]] || exit 1
		;;
	*)
		echo "unknown command: ${cmd}" >&2
		usage
		exit 1
		;;
	esac
}

main "$@"
