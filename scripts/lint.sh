#!/bin/sh
# Lints every Go target with golangci-lint. Used by the pre-commit hook and CI.
# Quiet on success (one summary line); issues print under their target's name.
set -u

root=$(cd "$(dirname "$0")/.." && pwd)
cfg="$root/proxy/.golangci.yml"
failed=""

lint() { # <name> <dir> [golangci-lint args...]
  name=$1 dir=$2
  shift 2
  if ! out=$(cd "$dir" && golangci-lint run --show-stats=false --config "$cfg" "$@" ./... 2>&1); then
    printf '\n── %s ──\n%s\n' "$name" "$out" >&2
    failed="$failed $name"
  fi
}

lint proxy          "$root/proxy"
lint proxy/stress   "$root/proxy" --build-tags stress
lint stress-driver  "$root/tests/stress/driver"

if [ -n "$failed" ]; then
  echo "lint: FAILED:$failed" >&2
  exit 1
fi
echo "lint: ok (proxy, proxy/stress, stress-driver)"
