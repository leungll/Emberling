#!/usr/bin/env bash
# Entry point for `make demo` and `make demo-down`.
#
#   demo.sh run   build and start postgres, the Mock Provider, the mock production service,
#                 the sandbox runner and the Backend when needed,
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
#                                       limit is rejected before any dispatch;
#                   photo-sync-crash    photo-sync-crash.sh: SIGKILL while the first
#                                       synchronous generation is held at the Provider; the
#                                       restarted Backend reports RESULT_UNCERTAIN to the
#                                       Agent, which generates the photo again;
#                   photo-video-restart photo-video-restart.sh: SIGKILL while the Agent waits
#                                       for the video task's callback; the callback reaches
#                                       the restarted Backend and completes the original
#                                       Action once, with nothing re-sent;
#                   delivery            delivery.sh main: an Agent tests a patch in the
#                                       sandbox runner, answered by callback, then deploys
#                                       it once to the mock production service;
#                   delivery-restart-during-test
#                                       delivery.sh restart: SIGKILL while the test Attempt
#                                       waits for its callback; the callback, delivered
#                                       twice, completes the original Attempt once;
#                   delivery-deploy-lost
#                                       delivery.sh deploy-lost: SIGKILL while production
#                                       holds the deployment; the restarted Backend hands
#                                       the call back to the Agent as RESULT_UNCERTAIN and
#                                       the Agent reports instead of deploying again;
#                   delivery-runner-redelivery
#                                       delivery.sh redelivery: the restarted runner
#                                       delivers a finished test again and the ledger gains
#                                       no Event.
#                 Each photo and delivery variant is followed by the invariant report as well.
#                 EMBERLING_DEMO_VARIANT=<one of the names above> runs only that variant.
#                 The serving variants run only when named, or all eight with
#                 EMBERLING_DEMO_VARIANT=serving. Each is serving.sh STORY, many users of
#                 the photo effect serving template, followed by the invariant report of
#                 every Run and the per-task ledger of all of them:
#                   serving-burst           twenty users submit at once;
#                   serving-crash-held      SIGKILL while dispatches are held before
#                                           acceptance, then restart;
#                   serving-crash-waiting   SIGKILL while every task waits for its
#                                           callback; callbacks and polls complete them;
#                   serving-duplicate       every callback delivered three times;
#                   serving-version-switch  a new version saved while Runs are in flight;
#                   serving-client-retry    concurrent resubmissions under one
#                                           Idempotency-Key per user;
#                   serving-response-lost   the Provider drops each acceptance answer and
#                                           the keyed retry takes over the original task;
#                   serving-rollout         keyed requests split 10% to a slower version 2,
#                                           then the ratio goes back to 0: replays return
#                                           the original Runs, new requests go to version 1,
#                                           in-flight version 2 Runs finish as version 2.
#                 scripts/demo/build-v1.sh builds the serving template from a tuning Run's
#                 delivered settings, and scripts/demo/evidence-report.sh summarises the
#                 fault-injection and real-flow evidence; both are run by hand.
#                 EMBERLING_DEMO_STUDIO=1 also builds and starts Studio, prints a Studio link
#                 to every Run and pauses for Enter at the moments worth watching; the
#                 stack and Studio keep running afterwards. EMBERLING_DEMO_PAUSE=0 keeps
#                 Studio and the links but skips the pauses.
#   demo.sh down  stop the stack, Studio included, and remove the request-record volumes of the Mock
#                 Provider, the mock production service and the sandbox runner, so the next
#                 demo starts from empty records. The PostgreSQL and asset
#                 volumes are kept: they also hold the developer's own `make dev` data.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

case "${1:-}" in
run)
  log "building the backend, mockprovider, mockproduction and sandboxrunner images (cached when sources are unchanged)"
  compose build --quiet mockprovider mockproduction sandboxrunner backend
  log "starting postgres, mockprovider, mockproduction, sandboxrunner and backend"
  compose up -d --wait postgres mockprovider mockproduction sandboxrunner backend
  if studio_mode; then
    log "building and starting studio (EMBERLING_DEMO_STUDIO=1)"
    compose build --quiet studio
    compose up -d --wait studio
    wait_until "Studio at $STUDIO_URL" 120 studio_ready
    printf 'Studio: %s\n' "$STUDIO_URL"
  fi

  case "${EMBERLING_DEMO_VARIANT:-all}" in
  all)
    variants=(held-before-accept after-accept photo-set photo-gate photo-limit photo-sync-crash
      photo-video-restart delivery delivery-restart-during-test delivery-deploy-lost delivery-runner-redelivery)
    ;;
  held-before-accept | after-accept | photo-set | photo-gate | photo-limit | photo-sync-crash | photo-video-restart | \
    delivery | delivery-restart-during-test | delivery-deploy-lost | delivery-runner-redelivery)
    variants=("$EMBERLING_DEMO_VARIANT")
    ;;
  serving)
    variants=(serving-burst serving-crash-held serving-crash-waiting serving-duplicate
      serving-version-switch serving-client-retry serving-response-lost serving-rollout)
    ;;
  serving-burst | serving-crash-held | serving-crash-waiting | serving-duplicate | \
    serving-version-switch | serving-client-retry | serving-response-lost | serving-rollout)
    variants=("$EMBERLING_DEMO_VARIANT")
    ;;
  *) die "EMBERLING_DEMO_VARIANT must be held-before-accept, after-accept, photo-set, photo-gate, photo-limit, photo-sync-crash, photo-video-restart, delivery, delivery-restart-during-test, delivery-deploy-lost, delivery-runner-redelivery, serving or one serving-* variant" ;;
  esac

  state=$(mktemp "${TMPDIR:-/tmp}/emberling-demo.XXXXXX")
  trap 'rm -f "$state"' EXIT

  verdict() { if [[ "$1" -eq 0 ]]; then echo PASS; else echo "FAIL (exit $1)"; fi; }
  summary=""
  overall=0
  remaining=${#variants[@]}
  for variant in "${variants[@]}"; do
    remaining=$((remaining - 1))
    story=""
    case "$variant" in
    held-before-accept) script=fault-kill.sh ;;
    after-accept) script=fault-kill-after-accept.sh ;;
    photo-set) script=photo-set.sh story=main ;;
    photo-gate) script=photo-set.sh story=gate ;;
    photo-limit) script=photo-set.sh story=limit ;;
    photo-sync-crash) script=photo-sync-crash.sh ;;
    photo-video-restart) script=photo-video-restart.sh ;;
    delivery) script=delivery.sh story=main ;;
    delivery-restart-during-test) script=delivery.sh story=restart ;;
    delivery-deploy-lost) script=delivery.sh story=deploy-lost ;;
    delivery-runner-redelivery) script=delivery.sh story=redelivery ;;
    serving-*) script=serving.sh story=${variant#serving-} ;;
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
    # A serving story lists all its Runs, comma-separated, and how many submissions the
    # clients sent; its ledger also requires every Run to have completed.
    submissions=$(sed -n 's/^SUBMISSIONS=//p' "$state")
    require_completed=0
    [[ "$variant" == serving-* ]] && require_completed=1

    # The fault scripts end with the invariant report; a photo or delivery story ends with its own
    # checks, so the invariant report runs here.
    invariant_status=$script_status
    story_status=""
    if [[ -n "$story" ]]; then
      story_status=$script_status
      invariant_status=0
      "$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id" || invariant_status=$?
    fi

    ledger_status=0
    LEDGER_SUBMISSIONS=$submissions LEDGER_REQUIRE_COMPLETED=$require_completed \
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
    if ((remaining > 0)); then
      demo_pause "variant $variant checked, run $run_id is $run_status; next: the next variant (the finished Run stays in Studio)"
    fi
  done

  printf '\n==== demo summary ====\n%s' "$summary"
  if studio_mode; then
    printf '\nThe stack and Studio (%s) keep running; stop them with: make demo-down\n' "$STUDIO_URL"
  fi
  [[ "$overall" -eq 0 ]]
  ;;
down)
  compose down --remove-orphans
  volumes=$(compose config --format json | jq -r '.volumes')
  for key in emberling-mockprovider-record emberling-mockproduction-record emberling-sandboxrunner-record; do
    record_volume=$(jq -r --arg key "$key" '.[$key].name // empty' <<<"$volumes")
    [[ -n "$record_volume" ]] || continue
    if docker volume inspect "$record_volume" >/dev/null 2>&1; then
      docker volume rm "$record_volume" >/dev/null
      log "removed volume $record_volume"
    fi
  done
  ;;
*)
  die "usage: demo.sh run|down"
  ;;
esac
