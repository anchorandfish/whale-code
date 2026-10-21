#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
source "$repo_dir/scripts/test-report.sh"

export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/whale-code-gocache}"
export WHALE_RUN_LIVE_TESTS=0
test_tmp="$(mktemp -d "${TMPDIR:-/tmp}/whale-test-full.XXXXXX")"
trap 'rm -rf "$test_tmp"' EXIT
export HOME="$test_tmp/home"
export USERPROFILE="$HOME"
mkdir -p "$HOME"
unset ANTHROPIC_API_KEY OPENAI_API_KEY OPENAI_BASE_URL WHALE_MODEL WHALE_FALLBACK_MODEL

# Bound package-level concurrency so integration subprocesses and timing-sensitive
# tests are not starved on developer machines. Override explicitly when needed.
parallelism="${WHALE_TEST_PARALLELISM:-4}"
test_flags=(-count=1)
if [[ "${1:-}" == "--race" ]]; then
  test_flags+=(-race)
  test_name="test-race"
elif [[ -n "${1:-}" ]]; then
  printf 'unknown test-full option: %s\n' "$1" >&2
  exit 2
else
  test_name="test-full"
fi

test_log="$(new_test_log "$test_name")"
printf 'Full test log: %s\n' "$test_log"
run_logged_test_command "$test_log" go test "${test_flags[@]}" -p "$parallelism" ./...
