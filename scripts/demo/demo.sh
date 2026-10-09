#!/usr/bin/env bash
# Entry point for `make demo` and `make demo-down`.
#
#   demo.sh run   build and start postgres, the Mock Provider and the Backend when needed,
#                 run fault-kill.sh, then ledger-vs-record.sh, then print a summary.
#   demo.sh down  stop the stack and remove the Mock Provider dispatch-record volume, so
#                 the next demo starts from an empty record. The PostgreSQL and asset
#                 volumes are kept: they also hold the developer's own `make dev` data.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

case "${1:-}" in
run)
  log "building the backend and mockprovider images (cached when sources are unchanged)"
  compose build --quiet mockprovider backend
  log "starting postgres, mockprovider and backend"
  compose up -d --wait postgres mockprovider backend

  state=$(mktemp "${TMPDIR:-/tmp}/emberling-demo.XXXXXX")
  trap 'rm -f "$state"' EXIT

  fault_status=0
  DEMO_STATE=$state "$DEMO_ROOT/scripts/demo/fault-kill.sh" || fault_status=$?
  [[ -s "$state" ]] || die "fault-kill.sh did not reach a terminal Run (exit $fault_status)"
  run_id=$(sed -n 's/^RUN_ID=//p' "$state")
  record_after=$(sed -n 's/^RECORD_AFTER=//p' "$state")
  run_status=$(sed -n 's/^RUN_STATUS=//p' "$state")

  ledger_status=0
  "$DEMO_ROOT/scripts/demo/ledger-vs-record.sh" "$run_id" "$record_after" || ledger_status=$?

  verdict() { if [[ "$1" -eq 0 ]]; then echo PASS; else echo "FAIL (exit $1)"; fi; }
  printf '\n==== demo summary ====\n'
  printf 'run                 %s\n' "$run_id"
  printf 'run status          %s\n' "$run_status"
  printf 'invariant report    %s\n' "$(verdict "$fault_status")"
  printf 'ledger vs record    %s\n' "$(verdict "$ledger_status")"
  [[ "$fault_status" -eq 0 && "$ledger_status" -eq 0 ]]
  ;;
down)
  compose down --remove-orphans
  record_volume="$(compose config --format json | jq -r '.volumes["emberling-mockprovider-record"].name')"
  if docker volume inspect "$record_volume" >/dev/null 2>&1; then
    docker volume rm "$record_volume" >/dev/null
    log "removed volume $record_volume"
  fi
  ;;
*)
  die "usage: demo.sh run|down"
  ;;
esac
