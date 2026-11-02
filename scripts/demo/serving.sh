#!/usr/bin/env bash
# Serving fault variants: many end users submit the photo effect serving template
# (backend/test/fixtures/definitions/serving_template.json) through the asynchronous image
# task path of the Mock Provider, while the script injects one fault. Every check reads the
# Backend's public API and the Mock Provider's control routes; ledger-vs-record.sh then
# compares the Runs with the Provider's record task by task.
#
# Usage: scripts/demo/serving.sh STORY
#
#   burst           EMBERLING_DEMO_SERVING_USERS (20) users submit at once; all complete.
#   crash-held      the Provider's barrier holds the image dispatches before acceptance;
#                   once every claimed dispatch is held and no other node is running,
#                   the Backend is SIGKILLed. The Provider records the held
#                   requests as abandoned, the barrier is released, the Backend restarts,
#                   times the held Attempts out and retries them under the same
#                   idempotency key. All complete with one task each.
#   crash-waiting   every task is accepted with a callback delayed by
#                   EMBERLING_DEMO_CALLBACK_DELAY_MS (20000); the Backend is SIGKILLed
#                   while all of them wait and kept down until the Provider has attempted
#                   every callback. After the restart half the callbacks are redelivered on
#                   command and Provider polling completes the rest; which path wins a
#                   given task is not scripted, only that each completes once.
#   duplicate       the Provider delivers every callback twice at once, and the script
#                   redelivers each one again after the Runs completed; the late
#                   deliveries add no Event.
#   version-switch  half the users submit on version 1, whose callbacks are delayed; while
#                   they wait, version 2 (image width 768) is saved, and the other half
#                   submit on the latest version. Each Run finishes on the version it was
#                   bound to, with that version's width.
#   client-retry    EMBERLING_DEMO_RETRY_USERS (5) users each send the same submission
#                   EMBERLING_DEMO_RETRY_SENDS (4) times at once under one
#                   Idempotency-Key; each user gets one Run, every other answer is a 200
#                   replay of it.
#   response-lost   the Provider accepts each of 3 tasks and drops its answer; Attempt 1
#                   fails as uncertain, the keyed retry is answered with the original task
#                   and that task's callback completes the retry.
#
# Writes RUN_ID (the Runs, comma-separated), WORKFLOW_ID, RECORD_AFTER, RUN_STATUS,
# SUBMISSIONS and SUMMARY to $DEMO_STATE when it is set, for ledger-vs-record.sh and the
# demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

story=${1:-}
case "$story" in
burst | crash-held | crash-waiting | duplicate | version-switch | client-retry | response-lost) ;;
*) die "usage: serving.sh burst|crash-held|crash-waiting|duplicate|version-switch|client-retry|response-lost" ;;
esac

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/serving_template.json"
USERS=${EMBERLING_DEMO_SERVING_USERS:-20}
RETRY_USERS=${EMBERLING_DEMO_RETRY_USERS:-5}
RETRY_SENDS=${EMBERLING_DEMO_RETRY_SENDS:-4}
CALLBACK_DELAY_MS=${EMBERLING_DEMO_CALLBACK_DELAY_MS:-20000}
# The held Attempts' deadline: long enough for every worker's dispatch to be held and
# the kill to land before the live Backend times them out (the dispatch request itself is
# cut at 30s), short enough that the restarted Backend retries them well inside the
# terminal deadline below.
HELD_TIMEOUT_MS=${EMBERLING_DEMO_IMAGE_TIMEOUT_MS:-30000}
for value in "$USERS" "$RETRY_USERS" "$RETRY_SENDS" "$CALLBACK_DELAY_MS" "$HELD_TIMEOUT_MS"; do
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || die "serving settings must be positive integers, got: $value"
done
STEP_DEADLINE_S=120
TERMINAL_DEADLINE_S=$((CALLBACK_DELAY_MS / 1000 + 240))

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

work=$(mktemp -d "${TMPDIR:-/tmp}/emberling-serving.XXXXXX")
barrier_paused=false
backend_down=false
cleanup() {
  if [[ "$barrier_paused" == true ]]; then
    curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null 2>&1 || true
  fi
  if [[ "$backend_down" == true ]]; then
    compose start backend >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

log "checking that the Backend and the Mock Provider test controls are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "Mock Provider /control/barrier" 60 mock_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null

log "uploading the user photo every submission carries"
# Distinct bytes per invocation; the Mock Provider does not decode images.
printf '\x89PNG\r\n\x1a\nserving photo %s\n' "$(date +%s)$RANDOM" >"$work/photo.png"
photo=$(curl -fsS -F "file=@$work/photo.png;type=image/png" "$BACKEND_URL/api/assets")

# serving_definition TEMPLATE TIMEOUT_MS WIDTH prints the serving template as a Definition
# request. An empty TEMPLATE keeps the fixture's effect prompt; a `mock:` directive makes
# every dispatch select that Mock Provider scenario.
serving_definition() {
  jq --arg template "$1" --argjson timeout "$2" --argjson width "$3" --arg story "$story" '
    del(.workflowId)
    | .name = "Photo Effect Serving Template (\($story))"
    | .nodes |= map(
        if .id == "node_prompt" and $template != "" then .config.template = $template
        elif .id == "node_image" then .executionPolicy.timeoutMs = $timeout | .config.width = $width
        else . end)' "$FIXTURE"
}

create_definition() {
  local created
  created=$(curl -fsS -X POST -H 'Content-Type: application/json' --data-binary "$1" "$BACKEND_URL/api/definitions")
  workflow_id=$(jq -r '.workflowId' <<<"$created")
  version=$(jq -r '.version' <<<"$created")
  log "definition $workflow_id version $version"
}

# submit_users VERSION COUNT FIRST_USER [SENDS] sends one submission per user, SENDS
# times each (1 by default), all as background requests started together. Users numbered
# from FIRST_USER. With SENDS > 1 every send of a user carries the same Idempotency-Key.
# Each answer is kept as $work/submit.<user>.<send>.{code,body,headers}.
submit_users() {
  local run_version=$1 count=$2 first=$3 sends=${4:-1} user send request key
  for ((user = first; user < first + count; user++)); do
    request=$(jq -n --arg wf "$workflow_id" --argjson v "$run_version" --argjson photo "$photo" \
      --arg text "Portrait of user $user." \
      '{workflowId: $wf, definitionVersion: $v, input: {photo: $photo, request: $text}}')
    key=()
    if ((sends > 1)); then
      key=(-H "Idempotency-Key: serving-$workflow_id-user-$user")
    fi
    for ((send = 1; send <= sends; send++)); do
      curl -sS -o "$work/submit.$user.$send.body" -D "$work/submit.$user.$send.headers" \
        -w '%{http_code}' -X POST -H 'Content-Type: application/json' ${key[@]+"${key[@]}"} \
        --data-binary "$request" "$BACKEND_URL/api/runs" >"$work/submit.$user.$send.code" &
    done
  done
  wait
}

# lines_into NAME COMMAND... reads COMMAND's output lines into the array NAME. The host's
# bash may be 3.2, which has no mapfile.
lines_into() {
  local name=$1
  shift
  IFS=$'\n' read -r -d '' -a "$name" < <("$@") || true
}

# submitted_runs FIRST COUNT prints the distinct Run ids the submissions of those users
# were answered with, one per line.
submitted_runs() {
  local user
  for ((user = $1; user < $1 + $2; user++)); do
    cat "$work"/submit."$user".*.body
  done | jq -r '.id // empty' | sort -u
}

run_status() {
  curl -fsS "$BACKEND_URL/api/runs/$1" 2>/dev/null | jq -r '.run.status'
}

all_terminal() {
  local run_id status
  for run_id in "${run_ids[@]}"; do
    status=$(run_status "$run_id") || return 1
    [[ "$status" == COMPLETED || "$status" == FAILED ]] || return 1
  done
}

image_detail() {
  local node_run
  node_run=$(curl -fsS "$BACKEND_URL/api/runs/$1" | jq -r '.nodeRuns[] | select(.nodeId == "node_image") | .id')
  [[ -n "$node_run" ]] || return 1
  curl -fsS "$BACKEND_URL/api/runs/$1/nodes/$node_run"
}

# all_images_dispatched RUN... succeeds when every Run's image Attempt is DISPATCHED and
# bound to an external task.
all_images_dispatched() {
  local run_id detail
  for run_id in "$@"; do
    detail=$(image_detail "$run_id" 2>/dev/null) || return 1
    jq -e 'any(.attempts[]; .status == "DISPATCHED" and .callbackBinding.externalTaskId != null)' <<<"$detail" >/dev/null || return 1
  done
}

bound_task() {
  image_detail "$1" | jq -r '[.attempts[] | .callbackBinding.externalTaskId // empty] | first // empty'
}

record_count() {
  record_after "$record_after_seq" | jq -s "[.[] | select($1)] | length"
}

record_after_seq=$(record_last_seq)
log "dispatch record baseline seq=$record_after_seq"
submissions=0
run_ids=()

case "$story" in
burst | duplicate | response-lost)
  template="" users=$USERS
  [[ "$story" == duplicate ]] && template="mock:duplicate:{{text}}"
  [[ "$story" == response-lost ]] && template="mock:noResponse:{{text}}" users=3
  create_definition "$(serving_definition "$template" 60000 1024)"
  log "$users users submit at once"
  submit_users "$version" "$users" 1
  submissions=$users
  lines_into run_ids submitted_runs 1 "$users"
  ;;
crash-held)
  create_definition "$(serving_definition "" "$HELD_TIMEOUT_MS" 1024)"
  log "pausing the Mock Provider barrier for task requests"
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["task"]}' "$MOCK_URL/control/pause" >/dev/null
  barrier_paused=true
  log "$USERS users submit at once"
  submit_users "$version" "$USERS" 1
  submissions=$USERS
  lines_into run_ids submitted_runs 1 "$USERS"

  # A synchronous node of this template has no deadline, so a kill inside one would leave
  # its Attempt with no recovery trigger. The kill therefore waits for a moment when no
  # other node is RUNNING and every claimed image dispatch is held at the Provider. While
  # the dispatches are held they occupy the workers, so the remaining Runs wait as READY
  # work; the Reconciler could still claim one between this check and the kill, a window
  # the script narrows but cannot close (the contract test closes it with a hook).
  quiescent() {
    local run_id counts images=0 others=0 held
    for run_id in "${run_ids[@]}"; do
      counts=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '[
          ([.nodeRuns[] | select(.nodeId == "node_image" and .status == "RUNNING" and .nextAttemptAt == null)] | length),
          ([.nodeRuns[] | select(.nodeId != "node_image" and .status == "RUNNING")] | length)] | @tsv') || return 1
      images=$((images + ${counts%%$'\t'*}))
      others=$((others + ${counts##*$'\t'}))
    done
    held=$(curl -fsS "$MOCK_URL/control/barrier" | jq '[.held[] | select(.kind == "task")] | length')
    ((images >= 1 && others == 0 && held == images))
  }
  wait_until "every claimed image dispatch to be held and no other node running" $((HELD_TIMEOUT_MS / 1000)) quiescent
  held_count=$(curl -fsS "$MOCK_URL/control/barrier" | jq '[.held[] | select(.kind == "task")] | length')
  log "$held_count image dispatch(es) held at the Provider"
  demo_pause "$held_count image dispatches are held before the Provider accepts them; next: SIGKILL the backend. Continue within ~$((HELD_TIMEOUT_MS / 1000))s (the Attempt deadline)"

  log "SIGKILL backend"
  backend_down=true
  compose kill -s SIGKILL backend >/dev/null
  abandoned() { (($(record_count '.kind == "task" and .event == "abandoned"') >= held_count)); }
  wait_until "the Provider to record the held dispatches as abandoned" "$STEP_DEADLINE_S" abandoned
  log "releasing the barrier"
  curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null
  barrier_paused=false
  log "starting backend again"
  compose start backend >/dev/null
  backend_down=false
  wait_until "restarted Backend /ready" "$STEP_DEADLINE_S" backend_ready
  ;;
crash-waiting)
  create_definition "$(serving_definition "mock:delay:$CALLBACK_DELAY_MS" $((CALLBACK_DELAY_MS + 100000)) 1024)"
  log "$USERS users submit at once; every callback comes ${CALLBACK_DELAY_MS}ms after acceptance"
  submit_users "$version" "$USERS" 1
  submissions=$USERS
  lines_into run_ids submitted_runs 1 "$USERS"
  wait_until "every image Attempt to be DISPATCHED" "$STEP_DEADLINE_S" all_images_dispatched "${run_ids[@]}"
  if (($(record_count '.kind == "task" and .event == "callback"') != 0)); then
    die "a callback was delivered before the kill; raise EMBERLING_DEMO_CALLBACK_DELAY_MS"
  fi
  demo_pause "all $USERS image tasks wait for their callbacks; next: SIGKILL the backend so it misses every one. Continue within ~$((CALLBACK_DELAY_MS / 1000 - 5))s"

  log "SIGKILL backend"
  backend_down=true
  compose kill -s SIGKILL backend >/dev/null
  callbacks_attempted() { (($(record_count '.kind == "task" and .event == "callback"') >= USERS)); }
  wait_until "the Provider to attempt every callback" "$TERMINAL_DEADLINE_S" callbacks_attempted
  log "starting backend again"
  compose start backend >/dev/null
  backend_down=false
  wait_until "restarted Backend /ready" "$STEP_DEADLINE_S" backend_ready
  half=$((USERS / 2))
  log "redelivering the callbacks of $half task(s); Provider polling completes the rest"
  for run_id in "${run_ids[@]:0:half}"; do
    curl -fsS -X POST "$MOCK_URL/control/tasks/$(bound_task "$run_id")/callback" >/dev/null
  done
  ;;
version-switch)
  half=$((USERS / 2))
  create_definition "$(serving_definition "mock:delay:$CALLBACK_DELAY_MS" $((CALLBACK_DELAY_MS + 60000)) 1024)"
  v1=$version
  log "$half users submit on version $v1, whose callbacks come ${CALLBACK_DELAY_MS}ms after acceptance"
  submit_users "$v1" "$half" 1
  lines_into in_flight submitted_runs 1 "$half"
  wait_until "the version $v1 image Attempts to be DISPATCHED" "$STEP_DEADLINE_S" all_images_dispatched "${in_flight[@]}"

  log "saving version 2 with image width 768 while version $v1 is in flight"
  next=$(serving_definition "" 60000 768 | jq --argjson base "$v1" '. + {baseVersion: $base}')
  curl -fsS -X PUT -H 'Content-Type: application/json' --data-binary "$next" "$BACKEND_URL/api/definitions/$workflow_id" >/dev/null
  v2=$(curl -fsS "$BACKEND_URL/api/definitions/$workflow_id" | jq -r '.version')
  log "$((USERS - half)) users submit on the latest version $v2"
  submit_users "$v2" "$((USERS - half))" $((half + 1))
  lines_into new_runs submitted_runs $((half + 1)) $((USERS - half))
  submissions=$USERS
  run_ids=("${in_flight[@]}" "${new_runs[@]}")
  ;;
client-retry)
  create_definition "$(serving_definition "" 60000 1024)"
  log "$RETRY_USERS users each send the same submission $RETRY_SENDS times at once under one Idempotency-Key"
  submit_users "$version" "$RETRY_USERS" 1 "$RETRY_SENDS"
  submissions=$((RETRY_USERS * RETRY_SENDS))
  lines_into run_ids submitted_runs 1 "$RETRY_USERS"
  ;;
esac

((${#run_ids[@]} > 0)) || die "no submission created a Run"
for run_id in "${run_ids[@]}"; do studio_run_link "$run_id"; done
log "waiting for ${#run_ids[@]} Run(s) to reach a terminal status (deadline ${TERMINAL_DEADLINE_S}s)"
wait_until "every Run to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" all_terminal

completed=0
for run_id in "${run_ids[@]}"; do
  [[ "$(run_status "$run_id")" == COMPLETED ]] && completed=$((completed + 1))
done
runs=${#run_ids[@]}
events_of_runs() {
  local run_id
  for run_id in "${run_ids[@]}"; do run_events "$run_id"; done | jq -s 'add'
}

printf '\nserving %s story: %d Run(s) from %d submission(s) (record lines after seq %s)\n\n' \
  "$story" "$runs" "$submissions" "$record_after_seq"
check all_runs_completed "completed=$completed of $runs" test "$completed" -eq "$runs"

case "$story" in
burst)
  check one_run_per_user "runs=$runs users=$USERS" test "$runs" -eq "$USERS"
  summary="users=$USERS completed=$completed"
  ;;
crash-held)
  retried=0
  for run_id in "${run_ids[@]}"; do
    if image_detail "$run_id" | jq -e '(.attempts | length) == 2 and .attempts[0].status == "FAILED"' >/dev/null; then
      retried=$((retried + 1))
    fi
  done
  abandoned_count=$(record_count '.kind == "task" and .event == "abandoned"')
  check held_dispatches_abandoned "held=$held_count abandoned=$abandoned_count" test "$abandoned_count" -ge "$held_count"
  check held_attempts_retried "held=$held_count retried=$retried" test "$retried" -eq "$held_count"
  summary="users=$USERS held=$held_count abandoned=$abandoned_count retried=$retried completed=$completed"
  ;;
crash-waiting)
  single=0
  for run_id in "${run_ids[@]}"; do
    image_detail "$run_id" | jq -e '(.attempts | length) == 1' >/dev/null && single=$((single + 1))
  done
  sources=$(events_of_runs | jq -c '[.[] | select(.type == "NODE_COMPLETED") | .payload.completionSource]
      | group_by(.) | map({key: (.[0] // "NONE"), value: length}) | from_entries')
  image_completions=$(jq '(.CALLBACK // 0) + (.PROVIDER_POLL // 0)' <<<"$sources")
  check original_attempts_resumed "single-attempt image NodeRuns=$single of $runs" test "$single" -eq "$runs"
  check completed_by_callback_or_poll "completions=$sources" test "$image_completions" -eq "$runs"
  summary="users=$USERS completions=$sources"
  ;;
duplicate)
  deliveries() { (($(record_count '.kind == "task" and .event == "callback"') >= 2 * runs)); }
  wait_until "two callback deliveries per task" "$STEP_DEADLINE_S" deliveries
  before=$(events_of_runs | jq 'length')
  log "redelivering every callback once more after completion"
  late_ok=0
  for run_id in "${run_ids[@]}"; do
    status=$(curl -fsS -X POST "$MOCK_URL/control/tasks/$(bound_task "$run_id")/callback" | jq -r '.status // 0')
    [[ "$status" == 200 ]] && late_ok=$((late_ok + 1))
  done
  events=$(events_of_runs)
  after=$(jq 'length' <<<"$events")
  received=$(jq '[.[] | select(.type == "NODE_CALLBACK_RECEIVED")] | length' <<<"$events")
  run_completed_events=$(jq '[.[] | select(.type == "RUN_COMPLETED")] | length' <<<"$events")
  check late_redelivery_answered "answered 200=$late_ok of $runs" test "$late_ok" -eq "$runs"
  check no_event_from_duplicates "events before=$before after=$after" test "$before" -eq "$after"
  check one_callback_per_image "NODE_CALLBACK_RECEIVED=$received runs=$runs" test "$received" -eq "$runs"
  check each_run_completed_once "RUN_COMPLETED=$run_completed_events runs=$runs" test "$run_completed_events" -eq "$runs"
  summary="users=$USERS deliveries_per_task=3 events_added_by_duplicates=$((after - before))"
  ;;
version-switch)
  bound_versions() {
    local run_id
    for run_id in "$@"; do
      curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -c '{v: .run.definitionVersion, width: .run.output.image.width}'
    done | jq -s -c 'group_by(.) | map(.[0] + {runs: length})'
  }
  old=$(bound_versions "${in_flight[@]}")
  new=$(bound_versions "${new_runs[@]}")
  check in_flight_kept_their_version "$old" jq -e --argjson v "$v1" 'length == 1 and .[0].v == $v and .[0].width == 1024' <<<"$old"
  check new_submissions_bound_latest "$new" jq -e --argjson v "$v2" 'length == 1 and .[0].v == $v and .[0].width == 768' <<<"$new"
  summary="v$v1 in flight=${#in_flight[@]} v$v2 new=${#new_runs[@]} completed=$completed"
  ;;
client-retry)
  # Each answer file holds one status code without a trailing newline, so files are
  # counted, not lines of their concatenation.
  created=$(grep -lx 201 "$work"/submit.*.code | wc -l | tr -d ' ')
  replayed=$(grep -lis '^idempotent-replayed: true' "$work"/submit.*.headers | wc -l | tr -d ' ')
  merged=$((submissions - runs))
  check one_run_per_user "runs=$runs users=$RETRY_USERS" test "$runs" -eq "$RETRY_USERS"
  check one_created_per_user "201 answers=$created" test "$created" -eq "$RETRY_USERS"
  check every_other_answer_replayed "200 replays=$replayed merged=$merged" test "$replayed" -eq "$merged"
  summary="submissions=$submissions runs=$runs merged=$merged"
  ;;
response-lost)
  taken_over=0
  for run_id in "${run_ids[@]}"; do
    if image_detail "$run_id" | jq -e '(.attempts | length) == 2 and .attempts[0].status == "FAILED"
        and .attempts[0].uncertain == true and .attempts[1].status == "SUCCEEDED"' >/dev/null; then
      taken_over=$((taken_over + 1))
    fi
  done
  dropped=$(record_count '.kind == "task" and .event == "responseDropped"')
  replays=$(record_count '.kind == "task" and .event == "responded" and .replayed == true')
  dispatched_replayed=$(events_of_runs | jq '[.[] | select(.type == "NODE_DISPATCHED" and .payload.replayed == true)] | length')
  check answers_dropped "responseDropped=$dropped runs=$runs" test "$dropped" -eq "$runs"
  check retry_took_over_original_task "replayed answers=$replays NODE_DISPATCHED replayed=$dispatched_replayed" \
    test "$replays" -eq "$runs" -a "$dispatched_replayed" -eq "$runs"
  check retry_attempt_completed "uncertain then succeeded=$taken_over of $runs" test "$taken_over" -eq "$runs"
  summary="users=$runs dropped=$dropped takeovers=$dispatched_replayed completed=$completed"
  ;;
esac

if [[ -n "${DEMO_STATE:-}" ]]; then
  ids=$(IFS=,; printf '%s' "${run_ids[*]}")
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUBMISSIONS=%s\nSUMMARY=%s\n' \
    "$ids" "$workflow_id" "$record_after_seq" "COMPLETED $completed/$runs" "$submissions" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'serving %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'serving %s story: all checks passed\n' "$story"
