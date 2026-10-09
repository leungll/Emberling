#!/usr/bin/env bash
# Runs invariants.sql for one Run against the Compose stack's PostgreSQL, inside the
# postgres container, and exits non-zero when any check reports FAIL.
#
# Usage: scripts/demo/invariants.sh RUN_ID
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

run_id=${1:?usage: invariants.sh RUN_ID}
[[ "$run_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $run_id"
require_tools docker

report=$(compose exec -T postgres \
  psql -X -q -U emberling -d emberling -v ON_ERROR_STOP=1 -v "run_id=$run_id" -f - \
  <"$DEMO_ROOT/scripts/demo/invariants.sql")
printf '%s\n' "$report"

if grep -q '^FAIL ' <<<"$report"; then
  exit 1
fi
grep -q '^PASS ' <<<"$report" || die "invariant report produced no PASS/FAIL lines"
