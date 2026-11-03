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
#                                       no Event;
#                   vuln-verify         vuln-fix.sh verify: an Agent bound to a workspace of
#                                       the example repository upgrades the vulnerable
#                                       dependency without adapting the code, fails that
#                                       verification, adapts the code and passes the second;
#                   vuln-scope-denied   vuln-fix.sh scope-denied: a write outside the
#                                       allowed paths is refused before any Attempt and the
#                                       Agent continues inside them;
#                   vuln-stale-evidence vuln-fix.sh stale-evidence: a verification of a
#                                       digest the workspace moved past is refused and the
#                                       latest digest is verified instead;
#                   vuln-restart-during-verify
#                                       vuln-fix.sh restart: SIGKILL while the verification
#                                       callback is held; after the restart no Decision is
#                                       asked again and no verification runs twice.
#                 Each photo, delivery and vuln variant is followed by the invariant report as well.
#                 The vuln variants bind the Agent to the example repository, which this
#                 script generates under deploy/.demo/repositories before the stack starts.
#                 EMBERLING_DEMO_MODEL=<model id> runs vuln-verify against a real decision
#                 model: the Backend registers it from EMBERLING_MODELS,
#                 EMBERLING_MODEL_BASE_URL and EMBERLING_MODEL_API_KEY (optionally
#                 EMBERLING_MODEL_TIMEOUT for one model call) in the shell environment;
#                 nothing else is needed, and an image key without image models makes
#                 the Backend refuse to start. vuln-fix.sh gives the Agent the
#                 EMBERLING_DEMO_MODEL_TIMEOUT_MS budget (default thirty minutes).
#                 EMBERLING_DEMO_RUNNER=docker starts the sandbox runner with its docker
#                 backend (compose profile sandbox-docker, which needs
#                 SANDBOXRUNNER_VERIFY_IMAGE) instead of the mock runner, so the vuln
#                 stories verify patches with a real `go test`; the delivery variants'
#                 mock test modes are not available then.
#                 EMBERLING_DEMO_VARIANT=<one of the names above> runs only that variant.
#                 The serving variants run only when named, or all nine with
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
#                                           in-flight version 2 Runs finish as version 2;
#                   serving-capacity        the backend restarted with two slots on the
#                                           image Provider: at most two image tasks are in
#                                           flight, the rest queue as READY steps and all
#                                           complete as the slots cycle.
#                 scripts/demo/build-v1.sh builds the serving template from a tuning Run's
#                 delivered settings, scripts/demo/traffic-split.sh splits keyed requests
#                 between two versions (serving-rollout runs it; it can also be run by
#                 hand), and scripts/demo/evidence-report.sh summarises the
#                 fault-injection and real-flow evidence; build-v1.sh and
#                 evidence-report.sh are run by hand.
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

# The example repository the vuln variants bind their Agent workspace to. It is generated
# from the checked-in source tree into a gitignored directory the compose file mounts into
# the backend (and the docker sandbox runner) read-only; the same tree always yields the
# same baseline commit the fixture Definition names.
REPOSITORIES_DIR="$DEMO_ROOT/deploy/.demo/repositories"

case "${1:-}" in
run)
  require_tools git
  # The sandbox runner the stack starts: the mock runner answers tests and verifications
  # from its mock backends; the docker runner runs them in containers and takes the same
  # service address, so the backend needs no other setting.
  runner_service=sandboxrunner
  runner_profile=()
  case "${EMBERLING_DEMO_RUNNER:-mock}" in
  mock) ;;
  docker)
    [[ -n "${SANDBOXRUNNER_VERIFY_IMAGE:-}" ]] ||
      die "EMBERLING_DEMO_RUNNER=docker needs SANDBOXRUNNER_VERIFY_IMAGE, the digest-pinned verification image built by deploy/sandboxrunner-verify/build.sh"
    runner_service=sandboxrunner-docker
    runner_profile=(--profile sandbox-docker)
    ;;
  *) die "EMBERLING_DEMO_RUNNER must be mock or docker" ;;
  esac
  log "generating the example repository under $REPOSITORIES_DIR"
  sh "$DEMO_ROOT/backend/internal/fixture/vulnrepo/generate.sh" "$REPOSITORIES_DIR/vuln-demo"
  log "building the backend, mockprovider, mockproduction and $runner_service images (cached when sources are unchanged)"
  compose "${runner_profile[@]+"${runner_profile[@]}"}" build --quiet mockprovider mockproduction "$runner_service" backend
  log "starting postgres, mockprovider, mockproduction, $runner_service and backend"
  compose "${runner_profile[@]+"${runner_profile[@]}"}" up -d --wait postgres mockprovider mockproduction "$runner_service" backend
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
      photo-video-restart delivery delivery-restart-during-test delivery-deploy-lost delivery-runner-redelivery
      vuln-verify vuln-scope-denied vuln-stale-evidence vuln-restart-during-verify)
    ;;
  held-before-accept | after-accept | photo-set | photo-gate | photo-limit | photo-sync-crash | photo-video-restart | \
    delivery | delivery-restart-during-test | delivery-deploy-lost | delivery-runner-redelivery | \
    vuln-verify | vuln-scope-denied | vuln-stale-evidence | vuln-restart-during-verify)
    variants=("$EMBERLING_DEMO_VARIANT")
    ;;
  serving)
    variants=(serving-burst serving-crash-held serving-crash-waiting serving-duplicate
      serving-version-switch serving-client-retry serving-response-lost serving-rollout serving-capacity)
    ;;
  serving-burst | serving-crash-held | serving-crash-waiting | serving-duplicate | \
    serving-version-switch | serving-client-retry | serving-response-lost | serving-rollout | serving-capacity)
    variants=("$EMBERLING_DEMO_VARIANT")
    ;;
  *) die "EMBERLING_DEMO_VARIANT must be held-before-accept, after-accept, photo-set, photo-gate, photo-limit, photo-sync-crash, photo-video-restart, delivery, delivery-restart-during-test, delivery-deploy-lost, delivery-runner-redelivery, vuln-verify, vuln-scope-denied, vuln-stale-evidence, vuln-restart-during-verify, serving or one serving-* variant" ;;
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
    vuln-verify) script=vuln-fix.sh story=verify ;;
    vuln-scope-denied) script=vuln-fix.sh story=scope-denied ;;
    vuln-stale-evidence) script=vuln-fix.sh story=stale-evidence ;;
    vuln-restart-during-verify) script=vuln-fix.sh story=restart ;;
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
    # The limit a capacity story ran with, so the invariant report checks the held
    # slots against it; empty for every other variant.
    provider_capacity=$(sed -n 's/^PROVIDER_CAPACITY=//p' "$state")
    require_completed=0
    [[ "$variant" == serving-* ]] && require_completed=1

    # The fault scripts end with the invariant report; a photo, delivery or vuln story ends with
    # its own checks, so the invariant report runs here.
    invariant_status=$script_status
    story_status=""
    if [[ -n "$story" ]]; then
      story_status=$script_status
      invariant_status=0
      EMBERLING_PROVIDER_CAPACITY=$provider_capacity "$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id" || invariant_status=$?
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
  # The sandbox-docker profile is named so a docker runner started by EMBERLING_DEMO_RUNNER
  # stops too.
  compose --profile sandbox-docker down --remove-orphans
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
