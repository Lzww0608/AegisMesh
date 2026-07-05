#!/usr/bin/env bash
set -euo pipefail

# Start the demo Docker Compose stack, optionally with observability services.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODE="${MODE:-demo}"

if [[ "$MODE" == "observability" ]]; then
  docker compose -f docker-compose.demo.yml -f docker-compose.observability.yml up -d --build
else
  docker compose -f docker-compose.demo.yml up -d --build
fi

echo "frontend:   http://127.0.0.1:8080/checkout"
echo "controller: http://127.0.0.1:9100/metrics"
if [[ "$MODE" == "observability" ]]; then
  echo "prometheus: http://127.0.0.1:9090"
  echo "grafana:    http://127.0.0.1:3000"
fi
