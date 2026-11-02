#!/usr/bin/env bash
# Fault injection on an asynchronous Tool call, the counterpart of photo-sync-crash.sh:
# SIGKILL the Backend while the photo Agent's generate_video Action waits for the Mock
# Provider's callback, restart it, and show that the callback completes the original
# Action once. Nothing is re-sent and nothing is uncertain: the task was accepted before
# the kill, its Attempt was committed DISPATCHED, and the Provider reports to whichever
# Backend is up when it calls back.
#
# Sequence:
#   1. upload three photos and create a Definition from the photo effect-template fixture
#      bound to the scripted trajectory photo-set-video-restart: the photo-set story whose
#      generate_video call asks the Provider to deliver its callback 30s after acceptance;
#   2. start a Run and wait until the Agent is in WAITING_CALLBACK with the video Attempt
#      DISPATCHED and bound to the Provider's task;
#   3. SIGKILL the Backend inside the callback delay, start it again, and read the Attempt
#      the restarted process finds in PostgreSQL;
#   4. the Provider's callback reaches the restarted Backend and resumes the same Attempt
#      through the idempotent resume path; the scripted Agent finishes from its committed
#      Context. Should the callback have been attempted while the Backend was down, it is
#      delivered again on command, as a real Provider would retry; the script says which;
#   5. check the outcome and print the evidence lines.
#
# Expects a COMPLETED Run whose media result is accepted with 3 of 3 passed; exactly one
# generate_video Attempt, the one bound before the kill, SUCCEEDED; one video task in the
# Provider's record, so the restart re-dispatched nothing; exactly one
# AGENT_ACTION_COMPLETED with completionSource CALLBACK and no failed Action; the Events
# AGENT_ACTION_WAITING, RUN_PAUSED, AGENT_ACTION_COMPLETED, RUN_RESUMED and RUN_COMPLETED
# once each and in that order; a generation budget of 6 used; and the Provider's generated
# images matching the ledger.
#
# The kill and the restart must both land inside the 30s callback delay the trajectory
# fixes, so the Studio pause before the kill names that bound.
#
# Ends with the invariant report, like the other fault scripts.
#
# Usage: scripts/demo/photo-video-restart.sh
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/photo_effect_template.json"
SCRIPT=photo-set-video-restart
# The trajectory's generate_video settings carry mock "delay:30000", so the Provider
# delivers the callback this many milliseconds after it accepts the task.
VIDEO_CALLBACK_DELAY_MS=30000
WAITING_DEADLINE_S=90
RESTART_DEADLINE_S=120
CALLBACK_DEADLINE_S=$((VIDEO_CALLBACK_DELAY_MS / 1000 + 60))
TERMINAL_DEADLINE_S=120

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

backend_down=false
photos_dir=$(mktemp -d "${TMPDIR:-/tmp}/emberling-photos.XXXXXX")
cleanup() {
  if [[ "$backend_down" == true ]]; then
    compose start backend >/dev/null 2>&1 || true
  fi
  rm -rf "$photos_dir"
}
trap cleanup EXIT

log "checking that the Backend and the Mock Provider test controls are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "Mock Provider /control/barrier" 60 mock_ready
# A barrier left paused by an interrupted earlier run would hold this story's requests.
curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null

log "uploading three photos"
photos='[]'
for name in harbour market orchard; do
  # Distinct bytes per photo and per invocation; the Mock Provider does not decode images.
  printf '\x89PNG\r\n\x1a\nphoto %s %s\n' "$name" "$(date +%s)$RANDOM" >"$photos_dir/$name.png"
  ref=$(curl -fsS -F "file=@$photos_dir/$name.png;type=image/png" "$BACKEND_URL/api/assets")
  photos=$(jq -c --argjson ref "$ref" '. + [$ref]' <<<"$photos")
done
log "photos $(jq -c 'map(.assetId)' <<<"$photos")"

log "creating a Definition from $(basename "$FIXTURE") with script $SCRIPT"
definition=$(jq --arg script "$SCRIPT" '
  del(.workflowId)
  | .name = "Photo Effect Template (\($script))"
  | .nodes |= map(if .id == "node_agent" then .config.modelConfig.script = $script else . end)' "$FIXTURE")
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
log "dispatch record baseline seq=$record_after_seq"

run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson photos "$photos" \
  '{workflowId: $wf, definitionVersion: $v, input: {brief: "One warm film look for the whole set", photos: $photos}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"
studio_run_link "$run_id"
demo_pause "run $run_id is running: six generations and six reviews, then one video task whose callback comes ${VIDEO_CALLBACK_DELAY_MS}ms after acceptance; watch the Agent's turns in Studio. Next: wait until the Agent waits for that callback"

agent_node_run() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id // empty'
}
agent_trace() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$(agent_node_run)/agent"
}
# The video call is the trajectory's last Tool call, so WAITING_CALLBACK with one bound
# generate_video Attempt identifies the moment the story is about.
agent_waiting_for_video() {
  local node trace
  node=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .status') || return 1
  [[ "$node" == WAITING_CALLBACK ]] || return 1
  trace=$(agent_trace) || return 1
  jq -e '[.turns[].toolAttempts[] | select(.toolName == "generate_video" and .status == "DISPATCHED" and .callbackBinding.externalTaskId != null)] | length == 1' <<<"$trace" >/dev/null
}
wait_until "the Agent to wait for the video callback with the Attempt DISPATCHED" "$WAITING_DEADLINE_S" agent_waiting_for_video
trace=$(agent_trace)
video_before=$(jq -c '[.turns[] | . as $t | .toolAttempts[] | select(.toolName == "generate_video")
    | {turnNo: $t.turnNo, id, status, task: .callbackBinding.externalTaskId, action: $t.action.status}] | first' <<<"$trace")
video_attempt=$(jq -r '.id' <<<"$video_before")
task_id=$(jq -r '.task' <<<"$video_before")
turns_before=$(jq '.turns | length' <<<"$trace")
log "before the kill: generate_video $video_before; $turns_before Turn(s) so far"

callback_lines() {
  record_after "$record_after_seq" | jq -s --arg id "$task_id" '[.[] | select(.event == "callback" and .externalTaskId == $id)]'
}
if [[ $(callback_lines | jq 'length') -ne 0 ]]; then
  die "the callback for $task_id was delivered before the kill; this stack reached WAITING_CALLBACK too late for the trajectory's ${VIDEO_CALLBACK_DELAY_MS}ms delay"
fi
# The callback delay runs from the Provider's acceptance, and the kill and the restart must
# both fit inside it for the callback to meet a live Backend.
demo_pause "run $run_id: the Agent is in WAITING_CALLBACK, video Attempt $video_attempt is DISPATCHED to task $task_id and its callback is due ${VIDEO_CALLBACK_DELAY_MS}ms after acceptance; next: SIGKILL the backend. Continue within ~$((VIDEO_CALLBACK_DELAY_MS / 1000 - 15))s"

log "SIGKILL backend"
killed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
backend_down=true
compose kill -s SIGKILL backend >/dev/null
demo_pause "backend killed while the Agent waits; the task is the Provider's to finish and the Attempt is DISPATCHED in PostgreSQL. Next: start the backend; it must be up before the callback is due (continue within a few seconds)"

log "starting backend again"
compose start backend >/dev/null
backend_down=false
wait_until "restarted Backend /ready" "$RESTART_DEADLINE_S" backend_ready
restarted_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
video_after_restart=$(agent_trace | jq -c '[.turns[] | .action as $a | .toolAttempts[] | select(.toolName == "generate_video")
    | {id, status, task: .callbackBinding.externalTaskId, action: $a.status}] | first')
log "after the restart: generate_video $video_after_restart (read from the restarted Backend, which found it in PostgreSQL)"

callback_attempted() { [[ $(callback_lines | jq 'length') -ge 1 ]]; }
wait_until "the Provider's callback delivery for $task_id" "$CALLBACK_DEADLINE_S" callback_attempted
first_delivery=$(callback_lines | jq -c 'first | {seq, status, delivery, error} | with_entries(select(.value != null))')
log "callback delivery: $first_delivery"
callback_path="the Provider's own delivery reached the restarted Backend"
if ! jq -e '(.status // 0) >= 200 and (.status // 0) < 300' <<<"$first_delivery" >/dev/null; then
  # The Mock Provider never retries on its own; redelivery on command stands in for the
  # retry a real Provider would make after an unanswered delivery.
  log "the delivery found no Backend; redelivering the callback for $task_id on command"
  redelivered=$(curl -fsS -X POST "$MOCK_URL/control/tasks/$task_id/callback")
  log "redelivery: $redelivered"
  callback_path="the first delivery found no Backend; the redelivery reached the restarted Backend"
fi

run_terminal() {
  local status
  status=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" 2>/dev/null | jq -r '.run.status') || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}
wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
run_status=$(jq -r '.run.status' <<<"$snapshot")
trace=$(agent_trace)
events=$(run_events "$run_id")
window=$(record_after "$record_after_seq" | jq -s '.')

video_attempts=$(jq -c '[.turns[] | .action as $a | .toolAttempts[] | select(.toolName == "generate_video")
    | {id, status, task: .callbackBinding.externalTaskId, action: $a.status}]' <<<"$trace")
video_record=$(jq -c '[.[] | select(.event == "video_dispatched") | .externalTaskId]' <<<"$window")
deliveries=$(callback_lines | jq -c 'map({delivery, status})')
accepted_deliveries=$(callback_lines | jq '[.[] | select(.status >= 200 and .status < 300)] | length')
callback_completions=$(jq '[.[] | select(.type == "AGENT_ACTION_COMPLETED" and .payload.completionSource == "CALLBACK")] | length' <<<"$events")
failed_actions=$(jq '[.[] | select(.type == "AGENT_ACTION_FAILED")] | length' <<<"$events")
# One seq per Event type the story turns on; a type that is absent or repeated breaks the
# order check, so the counts are checked too.
order=$(jq -c '[.[] | select(.type == "AGENT_ACTION_WAITING" or .type == "RUN_PAUSED" or .type == "RUN_RESUMED" or .type == "RUN_COMPLETED"
      or (.type == "AGENT_ACTION_COMPLETED" and .payload.completionSource == "CALLBACK"))
    | {seq, type}] | sort_by(.seq)' <<<"$events")
used=$(jq '.generationBudget.generationCallsUsed' <<<"$trace")
max_calls=$(jq '.generationBudget.maxGenerationCalls' <<<"$trace")
generated_ledger=$(jq -c '[.facts.items[] | select(.factType == "image_generated") | .subject] | sort' <<<"$trace")
generated_record=$(jq -c '[.[] | select(.event == "generated" and .kind == "image") | "artifact_" + .sha256[0:32]] | sort' <<<"$window")
caption=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.caption // "{}"' <<<"$snapshot" | jq -c '.')

# Timeline: kill and restart are the script's own wall-clock marks (second resolution);
# every other line is a persisted fact, from the Run's ledger or the Provider's record.
# event_at TYPE [COMPLETION_SOURCE] prints the timestamp of the first Event of TYPE, or of
# the first one with that completionSource: earlier synchronous Actions also complete.
event_at() {
  jq -r --arg type "$1" --arg source "${2:-}" '[.[] | select(.type == $type and ($source == "" or .payload.completionSource == $source)) | .timestamp] | first // "-"' <<<"$events"
}
record_at() {
  jq -r --arg id "$task_id" --arg event "$1" '[.[] | select(.externalTaskId == $id and .event == $event) | .at] | first // "-"' <<<"$window"
}
printf '\nphoto video-restart story, run %s (record lines after seq %s)\n' "$run_id" "$record_after_seq"
printf 'timeline of the video Action (task %s):\n' "$task_id"
printf '  %-36s %s\n' \
  "task accepted 202 (record)" "$(record_at responded)" \
  "AGENT_ACTION_WAITING (ledger)" "$(event_at AGENT_ACTION_WAITING)" \
  "RUN_PAUSED (ledger)" "$(event_at RUN_PAUSED)" \
  "Backend SIGKILL (script)" "$killed_at" \
  "Backend restart (script)" "$restarted_at" \
  "callback delivery (record)" "$(record_at callback)" \
  "AGENT_ACTION_COMPLETED (ledger)" "$(event_at AGENT_ACTION_COMPLETED CALLBACK)" \
  "RUN_RESUMED (ledger)" "$(event_at RUN_RESUMED)" \
  "RUN_COMPLETED (ledger)" "$(event_at RUN_COMPLETED)"
printf '\nevidence: before the kill     %s\n' "$video_before"
printf 'evidence: after the restart   %s\n' "$video_after_restart"
printf 'evidence: callback            %s (deliveries %s)\n' "$callback_path" "$deliveries"
printf 'evidence: at the end          attempts=%s\n' "$video_attempts"
printf 'evidence: event order         %s\n' "$(jq -r 'map(.type) | join(" -> ")' <<<"$order")"
printf 'evidence: budget %s of %s, ledger generated %s, record generated %s, record video tasks %s\n\n' \
  "$used" "$max_calls" "$(jq 'length' <<<"$generated_ledger")" "$(jq 'length' <<<"$generated_record")" "$video_record"

check run_completed "status=$run_status" test "$run_status" = COMPLETED
caption_accepted() {
  jq -e '.accepted == true and .passedCount == 3 and .photoCount == 3' <<<"$caption" >/dev/null
}
check media_result_accepted "$(jq -c '{accepted, passedCount, photoCount}' <<<"$caption")" caption_accepted
original_attempt_completed() {
  jq -e --arg id "$video_attempt" 'length == 1 and .[0].id == $id and .[0].status == "SUCCEEDED" and .[0].action == "SUCCEEDED"' <<<"$video_attempts" >/dev/null
}
check original_attempt_completed "before=$video_attempt after=$video_attempts" original_attempt_completed
attempt_survived_restart() {
  jq -e --arg id "$video_attempt" '.id == $id and (.status == "DISPATCHED" or .status == "SUCCEEDED")' <<<"$video_after_restart" >/dev/null
}
check attempt_survived_restart "right after the restart: $video_after_restart" attempt_survived_restart
video_not_redispatched() {
  jq -e --arg id "$task_id" '. == [$id]' <<<"$video_record" >/dev/null
}
check video_not_redispatched "record video tasks=$video_record bound=$task_id" video_not_redispatched
check callback_completed_once "accepted deliveries=$accepted_deliveries callback_completions=$callback_completions failed_actions=$failed_actions" \
  test "$accepted_deliveries" -ge 1 -a "$callback_completions" -eq 1 -a "$failed_actions" -eq 0
events_in_order() {
  jq -e 'map(.type) == ["AGENT_ACTION_WAITING", "RUN_PAUSED", "AGENT_ACTION_COMPLETED", "RUN_RESUMED", "RUN_COMPLETED"]' <<<"$order" >/dev/null
}
check events_in_order "$(jq -c 'map(.type)' <<<"$order")" events_in_order
check generation_budget "used=$used limit=$max_calls" test "$used" -eq 6
check record_generated_matches_ledger "record=$(jq 'length' <<<"$generated_record") ledger=$(jq 'length' <<<"$generated_ledger")" \
  test "$generated_record" = "$generated_ledger" -a "$(jq 'length' <<<"$generated_ledger")" -eq 6

summary="task=$task_id attempt=$video_attempt after_restart=$(jq -r '.status' <<<"$video_after_restart") deliveries=$(jq -c 'map("\(.delivery):\(.status)")' <<<"$deliveries") callback_completions=$callback_completions video_tasks=$(jq 'length' <<<"$video_record") budget=$used/$max_calls status=$run_status"
if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'photo video-restart story: %d check(s) FAILED\n' "$failures"
else
  printf 'photo video-restart story: all checks passed\n'
fi

# Like the other fault scripts, this one ends with the invariant report.
log "invariant report for run $run_id"
invariant_status=0
"$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id" || invariant_status=$?
((failures == 0 && invariant_status == 0))
