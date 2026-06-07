#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RESULTS_DIR="${RESULTS_DIR:-experiments/results}"
REQUESTS="${REQUESTS:-1000}"
CONCURRENCY="${CONCURRENCY:-32}"
TARGET="${TARGET:-aegis-user-b}"
DEVICE="${DEVICE:-eth0}"
DELAY="${DELAY:-200ms}"
JITTER="${JITTER:-50ms}"
LOSS="${LOSS:-2}"
CPUS="${CPUS:-0.25}"
RECOVERY_DURATION="${RECOVERY_DURATION:-30s}"
RECOVERY_INTERVAL="${RECOVERY_INTERVAL:-1s}"
EXECUTE_FAULTS="${EXECUTE_FAULTS:-true}"
WAIT_FOR_EBPF="${WAIT_FOR_EBPF:-true}"

URL_DIRECT="${URL_DIRECT:-http://127.0.0.1:8081/checkout}"
URL_ROUND_ROBIN="${URL_ROUND_ROBIN:-http://127.0.0.1:8082/checkout}"
URL_ADAPTIVE="${URL_ADAPTIVE:-http://127.0.0.1:8083/checkout}"
URL_RETRY_UNBUDGETED="${URL_RETRY_UNBUDGETED:-http://127.0.0.1:8084/checkout}"
URL_RETRY_BUDGETED="${URL_RETRY_BUDGETED:-http://127.0.0.1:8086/checkout}"
METRICS_RETRY_UNBUDGETED="${METRICS_RETRY_UNBUDGETED:-http://127.0.0.1:8084/metrics}"
METRICS_RETRY_BUDGETED="${METRICS_RETRY_BUDGETED:-http://127.0.0.1:8086/metrics}"

mkdir -p "$RESULTS_DIR"

run_latency() {
  local experiment="$1"
  local variant="$2"
  local url="$3"
  python experiments/scripts/run_http_benchmark.py \
    --url "$url" \
    --requests "$REQUESTS" \
    --concurrency "$CONCURRENCY" \
    --experiment "$experiment" \
    --variant "$variant" \
    --latency-out "$RESULTS_DIR/latency.csv"
}

record_recovery() {
  local experiment="$1"
  local variant="$2"
  go run ./cmd/experiment-recorder \
    --experiment "$experiment" \
    --variant "$variant" \
    --duration "$RECOVERY_DURATION" \
    --interval "$RECOVERY_INTERVAL" \
    --out "$RESULTS_DIR/recovery.csv"
}

reset_faults() {
  if [[ "$EXECUTE_FAULTS" != "true" ]]; then
    return
  fi
  TARGET="$TARGET" DEVICE="$DEVICE" bash scripts/reset_faults.sh
}

echo "== Baseline: no mesh vs AegisMesh =="
run_latency baseline no_mesh "$URL_DIRECT"
run_latency baseline aegismesh "$URL_ADAPTIVE"

echo "== Single-instance 200ms delay: round-robin vs adaptive P2C =="
if [[ "$EXECUTE_FAULTS" == "true" ]]; then
  KIND=delay TARGET="$TARGET" DEVICE="$DEVICE" DELAY="$DELAY" JITTER="$JITTER" EXECUTE=true bash scripts/run_fault_experiment.sh
else
  echo "EXECUTE_FAULTS=false; skipping delay injection"
fi
record_recovery single_instance_delay adaptive_p2c &
recovery_pid=$!
run_latency single_instance_delay round_robin "$URL_ROUND_ROBIN"
run_latency single_instance_delay adaptive_p2c "$URL_ADAPTIVE"
wait "$recovery_pid" || true
reset_faults

echo "== CPU throttle: static-threshold baseline vs slow_score =="
if [[ "$EXECUTE_FAULTS" == "true" ]]; then
  KIND=cpu TARGET="$TARGET" CPUS="$CPUS" EXECUTE=true bash scripts/run_fault_experiment.sh
else
  echo "EXECUTE_FAULTS=false; skipping CPU throttle"
fi
record_recovery cpu_throttle slow_score &
recovery_pid=$!
run_latency cpu_throttle static_threshold "$URL_ROUND_ROBIN"
run_latency cpu_throttle slow_score "$URL_ADAPTIVE"
wait "$recovery_pid" || true
reset_faults

echo "== Retry budget: without budget vs with budget =="
python experiments/scripts/run_retry_amplification.py \
  --without-url "$URL_RETRY_UNBUDGETED" \
  --with-url "$URL_RETRY_BUDGETED" \
  --without-metrics "$METRICS_RETRY_UNBUDGETED" \
  --with-metrics "$METRICS_RETRY_BUDGETED" \
  --requests "$REQUESTS" \
  --concurrency "$CONCURRENCY" \
  --latency-out "$RESULTS_DIR/latency.csv" \
  --retry-out "$RESULTS_DIR/retry.csv"

echo "== Packet loss: without eBPF vs with eBPF network score =="
if [[ "$EXECUTE_FAULTS" == "true" ]]; then
  KIND=loss TARGET="$TARGET" DEVICE="$DEVICE" LOSS="$LOSS" EXECUTE=true bash scripts/run_fault_experiment.sh
else
  echo "EXECUTE_FAULTS=false; skipping packet loss"
fi
run_latency packet_loss no_ebpf_network_score "$URL_ADAPTIVE"
if [[ "$WAIT_FOR_EBPF" == "true" ]]; then
  echo "Start cmd/agent in another terminal before the ebpf_network_score run, then press Enter."
  read -r
else
  echo "WAIT_FOR_EBPF=false; running ebpf_network_score without waiting for agent startup."
fi
run_latency packet_loss ebpf_network_score "$URL_ADAPTIVE"
reset_faults

echo "== Recovery curve =="
if [[ "$EXECUTE_FAULTS" == "true" ]]; then
  KIND=delay TARGET="$TARGET" DEVICE="$DEVICE" DELAY="$DELAY" JITTER="$JITTER" EXECUTE=true bash scripts/run_fault_experiment.sh
else
  echo "EXECUTE_FAULTS=false; skipping recovery delay injection"
fi
record_recovery recovery_curve adaptive_p2c &
recovery_pid=$!
run_latency recovery_curve adaptive_p2c "$URL_ADAPTIVE"
wait "$recovery_pid" || true
reset_faults

python experiments/notebooks/plot_latency.py --results "$RESULTS_DIR" --out "$RESULTS_DIR/figures"
python experiments/scripts/check_results.py --results "$RESULTS_DIR"
