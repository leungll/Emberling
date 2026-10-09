#!/usr/bin/env bash
# Fault injection: SIGKILL the Backend after the Provider has accepted the Image Generation
# task and the Attempt is committed DISPATCHED, so the Provider's single callback delivery
# finds no Backend. Restart it and prove that Provider polling, driven from PostgreSQL,
# completes the original Attempt through the same resume path a callback would use.
#
# How the callback is missed deterministically. The Mock Provider's barrier only holds
# inbound task and generate requests; it cannot hold an outbound callback. The task is
# therefore dispatched with the Mock Provider's `mock:delay:<ms>` scenario, which schedules
# the one callback delivery that many milliseconds after acceptance and never retries it.
# The script kills the Backend inside that window and keeps it down until the dispatch
# record shows the delivery attempt for that task with no 2xx answer. Only then is the
# Backend started again. The restart waits on that recorded fact, not on elapsed time, so
# the callback cannot reach a live Backend. If the delivery had already happened before
# the kill, the script stops and says so instead of continuing on a different scenario.
#
# The directive reaches the task through the prompt port of node_image: the Definition
# rewires that port to the Prompt Template, whose fixed template is the directive. The
# Prompt Rewrite and Caption nodes still run and see the same text as ordinary input.
#
# Sequence:
#   1. create a Definition from the AIGC fixture with a deadline on node_image long
#      enough to survive the outage, the restart and one Reconciler interval, and a single
#      attempt, so a retry cannot stand in for the polled completion;
#   2. start a Run with the barrier released;
#   3. wait until the dispatch record shows the task accepted (202) and the Node detail
#      shows that Attempt DISPATCHED and bound to the same external task id;
#   4. SIGKILL the Backend, wait for the failed callback delivery in the record, start the
#      Backend again;
#   5. wait for the Run to reach a terminal status, run the invariant report, then check
#      the outcome this scenario expects.
#
# Who polls is deliberately not scripted: the restarted process discovers the due poll
# from the Attempt's persisted schedule, and the claim is a conditional update.
#
# Usage: scripts/demo/fault-kill-after-accept.sh
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER and RUN_STATUS to $DEMO_STATE when it is set,
# for ledger-vs-record.sh.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/aigc_media.json"
# The Attempt deadline runs from its claim. It must outlast the callback delay, the
# outage, the restart and at least one Reconciler interval before a poll can complete it.
IMAGE_TIMEOUT_MS=${EMBERLING_DEMO_AFTER_ACCEPT_TIMEOUT_MS:-90000}
# The window between acceptance and the callback delivery in which the kill must land.
CALLBACK_DELAY_MS=${EMBERLING_DEMO_CALLBACK_DELAY_MS:-15000}
[[ "$IMAGE_TIMEOUT_MS" =~ ^[0-9]+$ ]] || die "EMBERLING_DEMO_AFTER_ACCEPT_TIMEOUT_MS must be an integer"
[[ "$CALLBACK_DELAY_MS" =~ ^[0-9]+$ ]] || die "EMBERLING_DEMO_CALLBACK_DELAY_MS must be an integer"
ACCEPT_DEADLINE_S=60
CALLBACK_DEADLINE_S=$((CALLBACK_DELAY_MS / 1000 + 30))
RESTART_DEADLINE_S=120
TERMINAL_DEADLINE_S=$((IMAGE_TIMEOUT_MS / 1000 + 120))

backend_down=false
restart_backend() {
  if [[ "$backend_down" == true ]]; then
    compose start backend >/dev/null 2>&1 || true
  fi
}
trap restart_backend EXIT

log "checking that the Backend and the Mock Provider test controls are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "Mock Provider /control/barrier" 60 mock_ready
# A barrier left paused by an interrupted earlier run would hold this dispatch.
curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null

log "creating a Definition from $(basename "$FIXTURE") with a ${IMAGE_TIMEOUT_MS}ms deadline, 1 attempt and a ${CALLBACK_DELAY_MS}ms callback delay on node_image"
definition=$(jq --argjson timeout "$IMAGE_TIMEOUT_MS" --arg directive "mock:delay:$CALLBACK_DELAY_MS" '
  del(.workflowId)
  | .name = "AIGC Media Generation (fault demo, kill after accept)"
  | .nodes |= map(
      if .id == "node_image" then .executionPolicy = {timeoutMs: $timeout, maxAttempts: 1, backoff: "FIXED"}
      elif .id == "node_prompt" then .config.template = $directive
      else . end)
  | .edges |= map(if .id == "edge_rewrite_image" then .source = "node_prompt" else . end)' "$FIXTURE")
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
log "dispatch record baseline seq=$record_after_seq"

run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" \
  '{workflowId: $wf, definitionVersion: $v, input: {brief: "A lighthouse at dusk, fault demo after accept"}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"

task_record() {
  record_after "$record_after_seq" | jq -s '[.[] | select(.kind == "task" or .kind == "poll")]'
}
accepted_task_id() {
  task_record | jq -r '[.[] | select(.event == "responded" and .status == 202 and (.replayed | not))
      | .externalTaskId] | first // empty'
}
task_accepted() {
  [[ -n "$(accepted_task_id)" ]]
}
wait_until "the Mock Provider to accept the image task" "$ACCEPT_DEADLINE_S" task_accepted
task_id=$(accepted_task_id)
log "Provider accepted task $task_id"

image_node_run_id() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_image") | .id'
}
image_node_run=$(image_node_run_id)
[[ -n "$image_node_run" ]] || die "run $run_id has no node_image NodeRun after its task was accepted"
attempt_dispatched() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$image_node_run" |
    jq -e --arg id "$task_id" 'any(.attempts[]; .status == "DISPATCHED" and .callbackBinding.externalTaskId == $id)' >/dev/null
}
wait_until "node_image's Attempt to be committed DISPATCHED and bound to $task_id" "$ACCEPT_DEADLINE_S" attempt_dispatched
detail=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$image_node_run")
log "before the kill: node_image $(jq -c '{status: .nodeRun.status, attempts: [.attempts[] | {attemptNo, status, bound: (.callbackBinding.externalTaskId // null)}]}' <<<"$detail")"

callback_lines() {
  task_record | jq --arg id "$task_id" '[.[] | select(.event == "callback" and .externalTaskId == $id)]'
}
if [[ $(callback_lines | jq 'length') -ne 0 ]]; then
  die "the callback for $task_id was delivered before the kill; raise EMBERLING_DEMO_CALLBACK_DELAY_MS"
fi

log "SIGKILL backend"
killed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
backend_down=true
compose kill -s SIGKILL backend >/dev/null

callback_attempted() {
  [[ $(callback_lines | jq 'length') -ge 1 ]]
}
log "keeping the Backend down until the Provider has attempted its callback for $task_id"
wait_until "the Provider's callback delivery for $task_id" "$CALLBACK_DEADLINE_S" callback_attempted
delivery=$(callback_lines | jq -c 'first | {seq, event, externalTaskId, status, delivery, error} | with_entries(select(.value != null))')
log "callback delivery while the Backend was down: $delivery"
if jq -e '(.status // 0) >= 200 and (.status // 0) < 300' <<<"$delivery" >/dev/null; then
  die "the callback for $task_id was accepted although the Backend was killed"
fi

log "starting backend again"
compose start backend >/dev/null
restarted_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
backend_down=false
wait_until "restarted Backend /ready" "$RESTART_DEADLINE_S" backend_ready

run_status() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.run.status'
}
run_terminal() {
  local status
  status=$(run_status 2>/dev/null) || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}
log "waiting for run $run_id to reach a terminal status (deadline ${TERMINAL_DEADLINE_S}s)"
wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal
final_status=$(run_status)
log "run $run_id is $final_status"

detail=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$image_node_run")
log "after recovery: node_image $(jq -c '{status: .nodeRun.status, attempts: [.attempts[] | {attemptNo, status, error: (.error.code // null), bound: (.callbackBinding.externalTaskId // null)}]}' <<<"$detail")"

log "dispatch record since seq $record_after_seq"
record_after "$record_after_seq" | jq -c '{seq, at, event, kind, externalTaskId, status, taskStatus, replayed, delivery, error} | with_entries(select(.value != null))' >&2

if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$final_status" >"$DEMO_STATE"
fi

log "invariant report for run $run_id"
invariant_status=0
"$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id" || invariant_status=$?

# The outcome this scenario exists to show: the callback was lost with the Backend, and
# polling completed the one Attempt bound to the one task the Provider created.
events=$(run_events "$run_id")
record=$(task_record)
expectations=0
expect() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then
    printf 'PASS %-34s %s\n' "$name" "$detail"
  else
    printf 'FAIL %-34s %s\n' "$name" "$detail"
    expectations=$((expectations + 1))
  fi
}
succeeded_attempts=$(jq '[.attempts[] | select(.status == "SUCCEEDED")] | length' <<<"$detail")
created_tasks=$(jq '[.[] | select(.event == "responded" and .status == 202 and (.replayed | not))] | length' <<<"$record")
completion_source=$(jq -r --arg nr "$image_node_run" '[.[] | select(.type == "NODE_COMPLETED" and .nodeRunId == $nr)
    | .payload.completionSource] | join(",")' <<<"$events")
polled_lines=$(jq --arg id "$task_id" '[.[] | select(.event == "polled" and .externalTaskId == $id)] | length' <<<"$record")
polled_statuses=$(jq -c --arg id "$task_id" '[.[] | select(.event == "polled" and .externalTaskId == $id) | .taskStatus // "unknown"]' <<<"$record")

# Timeline: kill and restart are the script's own wall-clock marks (second resolution);
# every other line is a persisted fact, from the Run's ledger or the Provider's record.
event_at() {
  jq -r --arg nr "$image_node_run" --arg type "$1" \
    '[.[] | select(.nodeRunId == $nr and .type == $type) | .timestamp] | first // "-"' <<<"$events"
}
record_at() {
  jq -r --arg id "$task_id" --arg event "$1" \
    '[.[] | select(.externalTaskId == $id and .event == $event) | .at] | first // "-"' <<<"$record"
}
printf '\ntimeline of node_image (task %s):\n' "$task_id"
printf '  %-32s %s\n' \
  "NODE_DISPATCHED (ledger)" "$(event_at NODE_DISPATCHED)" \
  "task accepted 202 (record)" "$(record_at responded)" \
  "Backend SIGKILL (script)" "$killed_at" \
  "callback delivery (record)" "$(record_at callback)" \
  "Backend restart (script)" "$restarted_at" \
  "first poll answered (record)" "$(record_at polled)" \
  "NODE_COMPLETED (ledger)" "$(event_at NODE_COMPLETED)"

printf '\nexpected outcome of the kill-after-accept scenario:\n'
expect run_completed "status=$final_status" test "$final_status" = COMPLETED
expect one_succeeded_image_attempt "succeeded=$succeeded_attempts" test "$succeeded_attempts" -eq 1
expect one_external_task_created "created=$created_tasks" test "$created_tasks" -eq 1
expect completed_by_provider_poll "completionSource=${completion_source:-none}" test "$completion_source" = PROVIDER_POLL
expect provider_was_polled "polled=$polled_lines taskStatus=$polled_statuses" test "$polled_lines" -ge 1

((invariant_status == 0 && expectations == 0))
