#!/usr/bin/env bash
set -euo pipefail

# Run the demo stack, inject a delay fault, collect load, and clean up.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KIND="${KIND:-delay}"
TARGET="${TARGET:-aegis-user-b}"
DEVICE="${DEVICE:-eth0}"
DELAY="${DELAY:-200ms}"
JITTER="${JITTER:-50ms}"
LOSS="${LOSS:-2}"
CPUS="${CPUS:-0.25}"
EXECUTE="${EXECUTE:-true}"

args=(go run ./cmd/fault-injector --kind "$KIND" --container "$TARGET" --device "$DEVICE")
case "$KIND" in
  delay)
    args+=(--delay "$DELAY" --jitter "$JITTER")
    ;;
  loss)
    args+=(--loss-percent "$LOSS")
    ;;
  cpu)
    args+=(--cpus "$CPUS")
    ;;
  reset)
    ;;
  *)
    echo "unsupported KIND=$KIND" >&2
    exit 2
    ;;
esac

if [[ "$EXECUTE" == "true" ]]; then
  args+=(--execute)
fi

"${args[@]}"
