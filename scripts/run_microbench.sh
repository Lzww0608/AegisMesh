#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASELINE_DIR="${BASELINE_DIR:-$ROOT/benchmarks/baseline}"
COUNT="${BENCH_COUNT:-10}"
BENCHTIME="${BENCHTIME:-}"
CAPTURE_JOBS="${CAPTURE_JOBS:-8}"
PER_JOB_GOMAXPROCS="${PER_JOB_GOMAXPROCS:-4}"

RUNNING_JOBS=0

usage() {
	cat <<'EOF'
Usage: scripts/run_microbench.sh <command>

Commands:
  capture [label]       Run hot-path microbenchmarks in parallel (main|quick).
  capture-snapshots     Run SnapshotAndReset grids sharded by upstream/endpoints.
  race                  Run race detector on hot-path packages.
  compare OLD NEW       Compare bench result files with benchstat.
  adaptive OLD NEW      Compare adaptive balancer at GOMAXPROCS=1 and 8.

Environment:
  BENCH_COUNT           Iterations per benchmark (default: 10).
  CAPTURE_JOBS          Max parallel go test processes (default: 8).
  PER_JOB_GOMAXPROCS    GOMAXPROCS per parallel job (default: 4).
EOF
}

bench_args() {
	local args=(-run=NONE -benchmem "-count=${COUNT}")
	if [[ -n "${BENCHTIME}" ]]; then
		args+=("-benchtime=${BENCHTIME}")
	fi
	printf '%s\n' "${args[@]}"
}

run_bench_to() {
	local output="$1"
	local gomax="$2"
	local bench_filter="$3"
	shift 3
	local -a args
	mapfile -t args < <(bench_args)
	mkdir -p "$(dirname "${output}")"
	GOMAXPROCS="${gomax}" go test "$@" "${args[@]}" -bench="${bench_filter}" >"${output}"
}

wait_for_slot() {
	while ((RUNNING_JOBS >= CAPTURE_JOBS)); do
		wait -n
		RUNNING_JOBS=$((RUNNING_JOBS - 1))
	done
}

launch_job() {
	wait_for_slot
	"$@" &
	RUNNING_JOBS=$((RUNNING_JOBS + 1))
}

wait_all_jobs() {
	while ((RUNNING_JOBS > 0)); do
		wait -n
		RUNNING_JOBS=$((RUNNING_JOBS - 1))
	done
}

launch_capture() {
	local name="$1"
	local bench_filter="$2"
	local label="$3"
	local gomax="$4"
	shift 4
	local output="${BASELINE_DIR}/${name}_${label}.txt"
	echo "==> ${name} -> ${output} (GOMAXPROCS=${gomax})"
	launch_job run_bench_to "${output}" "${gomax}" "${bench_filter}" "$@"
}

capture_all() {
	local label="${1:-main}"
	local telemetry_bench="Observe"
	local ebpf_bench="Decode|ObserveParallel"
	case "${label}" in
	quick)
		telemetry_bench="Observe|Prometheus"
		;;
	main)
		telemetry_bench="Observe"
		ebpf_bench="Decode|ObserveParallel"
		;;
	*)
		echo "unknown capture label: ${label}" >&2
		exit 1
		;;
	esac

	echo "Capturing hot-path baseline (label=${label}, count=${COUNT}, jobs=${CAPTURE_JOBS}, per_job_gomax=${PER_JOB_GOMAXPROCS})"
	launch_capture adaptive_gomax1 "Adaptive" "${label}" 1 ./sdk/go/aegisgrpc
	launch_capture adaptive_gomax8 "Adaptive" "${label}" 8 ./sdk/go/aegisgrpc
	launch_capture aegisgrpc_policy "Policy" "${label}" "${PER_JOB_GOMAXPROCS}" ./sdk/go/aegisgrpc
	launch_capture aegisgrpc_retry "Retry" "${label}" "${PER_JOB_GOMAXPROCS}" ./sdk/go/aegisgrpc
	launch_capture telemetry "${telemetry_bench}" "${label}" "${PER_JOB_GOMAXPROCS}" ./pkg/telemetry
	launch_capture retry "." "${label}" "${PER_JOB_GOMAXPROCS}" ./pkg/retry
	launch_capture circuitbreaker "." "${label}" "${PER_JOB_GOMAXPROCS}" ./pkg/circuitbreaker
	launch_capture registry "Memory" "${label}" "${PER_JOB_GOMAXPROCS}" ./pkg/registry
	launch_capture policy "." "${label}" "${PER_JOB_GOMAXPROCS}" ./pkg/policy
	launch_capture ebpf "${ebpf_bench}" "${label}" "${PER_JOB_GOMAXPROCS}" ./agent/ebpf
	wait_all_jobs
	echo "Hot-path baseline written to ${BASELINE_DIR}"
}

capture_snapshots_parallel() {
	local label="${1:-main}"
	echo "Capturing snapshot grids (count=${COUNT}, jobs=${CAPTURE_JOBS}, per_job_gomax=${PER_JOB_GOMAXPROCS})"
	local tmp="${BASELINE_DIR}/.snapshot_shards_${label}"
	rm -rf "${tmp}"
	mkdir -p "${tmp}"

	for upstreams in 1 8 64; do
		local out="${tmp}/telemetry_u${upstreams}.txt"
		local bench="SnapshotAndReset/upstreams=${upstreams}"
		echo "==> telemetry shard upstreams=${upstreams} (1K/10K/100K)"
		launch_job run_bench_to "${out}" "${PER_JOB_GOMAXPROCS}" "${bench}" ./pkg/telemetry
	done
	for endpoints in 1 8 64; do
		local out="${tmp}/ebpf_e${endpoints}.txt"
		local bench="SnapshotAndReset/endpoints=${endpoints}"
		echo "==> ebpf shard endpoints=${endpoints} (1K/10K/100K)"
		launch_job run_bench_to "${out}" "${PER_JOB_GOMAXPROCS}" "${bench}" ./agent/ebpf
	done
	wait_all_jobs

	{
		echo "# telemetry SnapshotAndReset shards (count=${COUNT})"
		for upstreams in 1 8 64; do
			cat "${tmp}/telemetry_u${upstreams}.txt"
		done
	} >"${BASELINE_DIR}/telemetry_snapshot_${label}.txt"

	{
		echo "# ebpf SnapshotAndReset shards (count=${COUNT})"
		for endpoints in 1 8 64; do
			cat "${tmp}/ebpf_e${endpoints}.txt"
		done
	} >"${BASELINE_DIR}/ebpf_snapshot_${label}.txt"

	rm -rf "${tmp}"
	echo "Snapshot baselines: telemetry_snapshot_${label}.txt, ebpf_snapshot_${label}.txt"
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
	capture-snapshots)
		capture_snapshots_parallel "${2:-main}"
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
