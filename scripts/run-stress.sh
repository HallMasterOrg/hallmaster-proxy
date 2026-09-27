#!/usr/bin/env bash
# run-stress.sh — orchestrate one or all stress scenarios.
#
# Usage:
#   ./scripts/run-stress.sh                      # runs every scenario sequentially
#   ./scripts/run-stress.sh steady_1kmps         # runs the named scenario
#
# What it does:
#   1. Ensures the Root CA exists (via certificate-manager.sh).
#   2. Brings up the stress-tagged stack from docker-compose.test.yml +
#      docker-compose.stress.yml.
#   3. Invokes the driver once per scenario, with results landing in
#      tests/stress/results/<scenario>_<utc-timestamp>/.
#   4. Tears the stack down on completion (or on Ctrl-C).
#
# Each run's summary.md reports "Proxy `tamper` log records". If that is
# 0 on a non-idle scenario, traffic did not go through the proxy and the
# numbers are meaningless.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ ! -f "certs/hallmaster-rootca.crt" ]]; then
  echo "[run-stress] generating Root CA"
  ./certificate-manager.sh
fi

declare -a SCENARIOS
if [[ $# -gt 0 ]]; then
  SCENARIOS=("$@")
else
  SCENARIOS=(idle steady_100mps steady_1kmps burst_10k rest_200rps)
fi

COMPOSE=(docker compose -f docker-compose.test.yml -f docker-compose.stress.yml)

cleanup() {
  echo "[run-stress] tearing down"
  "${COMPOSE[@]}" down -v --remove-orphans || true
}
trap cleanup EXIT INT TERM

echo "[run-stress] building images"
"${COMPOSE[@]}" build hallmaster-proxy hallmaster-robojs-mock stress-driver

echo "[run-stress] starting proxy + mock"
if ! "${COMPOSE[@]}" up -d --wait --wait-timeout 60 hallmaster-robojs-mock hallmaster-proxy; then
  echo "[run-stress] proxy never became healthy"
  "${COMPOSE[@]}" logs hallmaster-proxy | tail -50
  exit 1
fi
echo "[run-stress] proxy healthy"

mkdir -p tests/stress/results

for scenario in "${SCENARIOS[@]}"; do
  echo "[run-stress] === scenario: $scenario ==="
  "${COMPOSE[@]}" run --rm stress-driver "$scenario"
done

echo "[run-stress] all scenarios complete; results in tests/stress/results/"
