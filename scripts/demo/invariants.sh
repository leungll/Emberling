#!/usr/bin/env bash
# Runs invariants.sql for each given Run against the Compose stack's PostgreSQL, inside
# the postgres container, and exits non-zero when any check of any Run reports FAIL. Each
# Run is reported in its own read-only snapshot; a serving variant lists all its Runs.
#
# Usage: scripts/demo/invariants.sh RUN_ID[,RUN_ID...] [RUN_ID...]
#
# EMBERLING_PROVIDER_CAPACITY, when set to the value the Backend was started with, is
# passed through unchanged so the report also checks every Provider's held slots against
# its limit; without it that check is listed as not run.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

(($# > 0)) || die "usage: invariants.sh RUN_ID[,RUN_ID...] [RUN_ID...]"
require_tools docker

provider_capacity=${EMBERLING_PROVIDER_CAPACITY:-}
if [[ -n "$provider_capacity" ]]; then
  [[ "$provider_capacity" =~ ^[A-Za-z0-9_.-]+=[1-9][0-9]*(,[A-Za-z0-9_.-]+=[1-9][0-9]*)*$ ]] ||
    die "EMBERLING_PROVIDER_CAPACITY must be <provider id>=<limit>[,...]"
fi

run_ids=()
for arg in "$@"; do
  IFS=, read -r -a ids <<<"$arg"
  run_ids+=("${ids[@]}")
done

failed=0
for run_id in "${run_ids[@]}"; do
  [[ "$run_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $run_id"
  report=$(compose exec -T postgres \
    psql -X -q -U emberling -d emberling -v ON_ERROR_STOP=1 -v "run_id=$run_id" \
    -v "provider_capacity=$provider_capacity" -f - \
    <"$DEMO_ROOT/scripts/demo/invariants.sql")
  printf '%s\n' "$report"
  grep -q '^PASS ' <<<"$report" || die "invariant report for run $run_id produced no PASS/FAIL lines"
  if grep -q '^FAIL ' <<<"$report"; then
    failed=$((failed + 1))
  fi
done

if ((${#run_ids[@]} > 1)); then
  printf 'invariant reports: %d run(s), %d with a FAIL\n' "${#run_ids[@]}" "$failed"
fi
((failed == 0))
