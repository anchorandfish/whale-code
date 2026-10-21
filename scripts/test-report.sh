#!/usr/bin/env bash

# Shared logging and end-of-command failure summaries for release test gates.

new_test_log() {
  local name="$1"
  local log_dir="${WHALE_TEST_LOG_DIR:-${TMPDIR:-/tmp}/whale-test-logs}"
  mkdir -p "$log_dir"
  printf '%s/%s-%s-%s.log\n' "$log_dir" "$name" "$(date '+%Y%m%d-%H%M%S')" "$$"
}

print_limited_matches() {
  local max_lines="$1"
  shift

  set +o pipefail
  grep "$@" | head -n "$max_lines" || true
  set -o pipefail
}

summarize_test_failure() {
  local log_path="$1"

  printf '\n===== TEST FAILURE SUMMARY =====\n'

  if grep -q -E '^[[:space:]]*--- FAIL:' "$log_path"; then
    printf '\n-- Failed tests and diagnostics --\n'
    print_limited_matches 240 -n -A 14 -E '^[[:space:]]*--- FAIL:' "$log_path"
  fi

  if grep -q 'WARNING: DATA RACE' "$log_path"; then
    printf '\n-- Data race reports --\n'
    print_limited_matches 320 -n -A 55 'WARNING: DATA RACE' "$log_path"
  fi

  if grep -q -E '^(panic:|fatal error:)' "$log_path"; then
    printf '\n-- Panic or fatal error --\n'
    print_limited_matches 200 -n -A 40 -E '^(panic:|fatal error:)' "$log_path"
  fi

  if grep -q -E '\[build failed\]' "$log_path"; then
    printf '\n-- Build failures --\n'
    print_limited_matches 160 -n -B 12 -A 4 -E '\[build failed\]' "$log_path"
  fi

  printf '\n-- Failed packages --\n'
  grep -n -E '^FAIL([[:space:]]|$)' "$log_path" || true

  printf '\n-- Last 80 log lines --\n'
  tail -n 80 "$log_path"

  # Repeat the primary failure immediately before the log path. This keeps the
  # bottom of long terminal output useful when only the final lines are copied.
  if grep -q -E '^[[:space:]]*--- FAIL:' "$log_path"; then
    printf '\n-- Copy-ready failed tests --\n'
    print_limited_matches 80 -n -A 3 -E '^[[:space:]]*--- FAIL:' "$log_path"
  elif grep -q 'WARNING: DATA RACE' "$log_path"; then
    printf '\n-- Copy-ready race report --\n'
    print_limited_matches 80 -n -A 12 'WARNING: DATA RACE' "$log_path"
  elif grep -q -E '^(panic:|fatal error:)' "$log_path"; then
    printf '\n-- Copy-ready fatal error --\n'
    print_limited_matches 80 -n -A 12 -E '^(panic:|fatal error:)' "$log_path"
  elif grep -q -E '\[build failed\]' "$log_path"; then
    printf '\n-- Copy-ready build failure --\n'
    print_limited_matches 80 -n -B 8 -A 2 -E '\[build failed\]' "$log_path"
  fi

  printf '\nFull test log: %s\n' "$log_path"
  printf '===== END TEST FAILURE SUMMARY =====\n'
}

run_logged_test_command() {
  local log_path="$1"
  shift

  set +e
  "$@" 2>&1 | tee -a "$log_path"
  local command_status=${PIPESTATUS[0]}
  set -e
  if (( command_status != 0 )); then
    summarize_test_failure "$log_path"
  fi
  return "$command_status"
}
