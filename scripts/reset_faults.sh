#!/usr/bin/env bash
set -euo pipefail

# Best-effort reset of tc network faults and Docker CPU limits for one target.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGET="${TARGET:-aegis-user-b}"
DEVICE="${DEVICE:-eth0}"

go run ./cmd/fault-injector --kind reset --container "$TARGET" --device "$DEVICE" --execute || true
docker update --cpus 0 "$TARGET" >/dev/null
