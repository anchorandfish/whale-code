#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
source "$repo_dir/scripts/test-report.sh"

export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/whale-code-gocache}"
export WHALE_RUN_LIVE_TESTS=0
release_tmp="$(mktemp -d "${TMPDIR:-/tmp}/whale-release-core.XXXXXX")"
trap 'rm -rf "$release_tmp"' EXIT
export HOME="$release_tmp/home"
export USERPROFILE="$HOME"
mkdir -p "$HOME"
unset ANTHROPIC_API_KEY OPENAI_API_KEY OPENAI_BASE_URL WHALE_MODEL WHALE_FALLBACK_MODEL
test_log="$(new_test_log "test-release-core")"
printf 'Full test log: %s\n' "$test_log"

run_gate() {
  local name="$1"
  shift
  printf '\n[%s]\n' "$name" | tee -a "$test_log"
  run_logged_test_command "$test_log" "$@"
}

run_gate "build" go build ./...
run_gate "cli-contract" go test -count=1 -run 'Test(Version_Flag|Help_Flag|CheckAPI_Flag|Plain_Mode|CI_NoArgs|Unknown_Argument|CI_ExitCodes|CheckAPI_InvalidKey|ParseArgs_)' .
run_gate "wiring" go test -count=1 ./internal
run_gate "state-and-recovery" go test -count=1 ./internal/config ./internal/onboarding ./internal/session ./internal/filehistory
run_gate "security" go test -count=1 ./internal/permission ./internal/approval ./internal/sandbox/... ./internal/secret
run_gate "ai-and-tools" go test -count=1 ./internal/ai ./internal/context ./internal/tools ./internal/mcp
run_gate "user-runtime" go test -count=1 ./internal/tui ./internal/daemon
run_gate "orchestration" go test -count=1 ./internal/orchestrator ./internal/goal

printf '\nRelease core gate passed. Live provider tests were not run.\n'
