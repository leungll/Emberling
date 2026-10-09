#!/usr/bin/env bash
# Entry point for `make demo` and `make demo-down`.
#
#   demo.sh run   build and start postgres, the Mock Provider and the Backend when needed,
#                 then run each fault variant in turn, each followed by
#                 ledger-vs-record.sh, then print a summary per variant:
#                   held-before-accept  fault-kill.sh: SIGKILL while the image dispatch is
#                                       held before the Provider accepts it;
#                   after-accept        fault-kill-after-accept.sh: SIGKILL after the
#                                       Provider accepted the task and the Attempt is
#                                       DISPATCHED, so only Provider polling can complete it;
#                   photo-set           photo-set.sh main: an Agent applies one effect
#                                       template to three photos in two reviewed rounds,
#                                       renders one video by callback and delivers one
#                                       reviewed example per photo;
#                   photo-gate          photo-set.sh gate: a video request for an unreviewed
#                                       asset is rejected before any dispatch;
#                   photo-limit         photo-set.sh limit: a generation past the frozen
#                                       limit is rejected before any dispatch.
#                 Each photo variant is followed by the invariant report as well.
#                 EMBERLING_DEMO_VARIANT=<one of the names above> runs only that variant.
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

  case "${EMBERLING_DEMO_VARIANT:-all}" in
  all) variants=(held-before-accept after-accept photo-set photo-gate photo-limit) ;;
  held-before-accept | after-accept | photo-set | photo-gate | photo-limit) variants=("$EMBERLING_DEMO_VARIANT") ;;
  *) die "EMBERLING_DEMO_VARIANT must be held-before-accept, after-accept, photo-set, photo-gate or photo-limit" ;;
  esac

  state=$(mktemp "${TMPDIR:-/tmp}/emberling-demo.XXXXXX")
  trap 'rm -f "$state"' EXIT

  verdict() { if [[ "$1" -eq 0 ]]; then echo PASS; else echo "FAIL (exit $1)"; fi; }
  summary=""
  overall=0
  for variant in "${variants[@]}"; do
    story=""
    case "$variant" in
    held-before-accept) script=fault-kill.sh ;;
    after-accept) script=fault-kill-after-accept.sh ;;
    photo-set) script=photo-set.sh story=main ;;
    photo-gate) script=photo-set.sh story=gate ;;
    photo-limit) script=photo-set.sh story=limit ;;
    esac
    printf '\n==== variant %s (%s%s) ====\n' "$variant" "$script" "${story:+ $story}"
    : >"$state"
    script_status=0
    DEMO_STATE=$state "$DEMO_ROOT/scripts/demo/$script" ${story:+"$story"} || script_status=$?
    [[ -s "$state" ]] || die "$script did not reach a terminal Run (exit $script_status)"
    run_id=$(sed -n 's/^RUN_ID=//p' "$state")
    record_after=$(sed -n 's/^RECORD_AFTER=//p' "$state")
    run_status=$(sed -n 's/^RUN_STATUS=//p' "$state")
    story_summary=$(sed -n 's/^SUMMARY=//p' "$state")

    # The fault scripts end with the invariant report; a photo story ends with its own
    # checks, so the invariant report runs here.
    invariant_status=$script_status
    story_status=""
    if [[ -n "$story" ]]; then
      story_status=$script_status
      invariant_status=0
      "$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id" || invariant_status=$?
    fi

    ledger_status=0
    "$DEMO_ROOT/scripts/demo/ledger-vs-record.sh" "$run_id" "$record_after" || ledger_status=$?

    summary+=$(printf '\nvariant             %s\nrun                 %s\nrun status          %s\n' \
      "$variant" "$run_id" "$run_status")
    if [[ -n "$story" ]]; then
      summary+=$(printf '\nstory checks        %s\nstory               %s' "$(verdict "$story_status")" "$story_summary")
    fi
    summary+=$(printf '\ninvariant report    %s\nledger vs record    %s\n' \
      "$(verdict "$invariant_status")" "$(verdict "$ledger_status")")
    summary+=$'\n'
    if [[ "$script_status" -ne 0 || "$invariant_status" -ne 0 || "$ledger_status" -ne 0 ]]; then
      overall=1
    fi
  done

  printf '\n==== demo summary ====\n%s' "$summary"
  [[ "$overall" -eq 0 ]]
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
