#!/usr/bin/env bash
# Fault injection: SIGKILL the Backend while its external dispatch is held in flight, then
# restart it and prove that recovery comes from PostgreSQL, not from process memory.
#
# Sequence:
#   1. create a Definition from the AIGC fixture with a deadline and a retry on its
#      Image Generation node (without a deadline a dispatch cut off by a crash has no
#      recovery trigger, by design);
#   2. pause the Mock Provider's task barrier, then start a Run;
#   3. wait until the Image Generation dispatch is held at the Provider. At that point
#      the Attempt is committed as STARTED, because the external call only begins after
#      the claiming transaction commits;
#   4. SIGKILL the Backend container, release the barrier, start the Backend again;
#   5. wait for the Run to reach a terminal status and run the invariant report.
#
# Who recovers the Attempt is deliberately not scripted. The restarted process and its
# Reconciler both discover the expired STARTED Attempt from PostgreSQL; whichever claims
# it first wins the conditional update and the other sees zero affected rows. The script
# only waits on the persisted terminal status, and the invariant report checks that no
# work was completed twice.
#
# Usage: scripts/demo/fault-kill.sh
# Writes RUN_ID, WORKFLOW_ID and RECORD_AFTER to $DEMO_STATE when it is set, for
# ledger-vs-record.sh.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/aigc_media.json"
# Long enough for the dispatch to be held and the kill to land, short enough that the
# expired Attempt is retried well inside the terminal-status deadline below.
IMAGE_TIMEOUT_MS=${EMBERLING_DEMO_IMAGE_TIMEOUT_MS:-10000}
HELD_DEADLINE_S=60
RESTART_DEADLINE_S=120
TERMINAL_DEADLINE_S=180

barrier_paused=false
release_barrier() {
  if [[ "$barrier_paused" == true ]]; then
    curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null 2>&1 || true
  fi
}
trap release_barrier EXIT

log "checking that the Backend and the Mock Provider test controls are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "Mock Provider /control/barrier" 60 mock_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null

log "creating a Definition from $(basename "$FIXTURE") with a ${IMAGE_TIMEOUT_MS}ms deadline and 2 attempts on node_image"
definition=$(jq --argjson timeout "$IMAGE_TIMEOUT_MS" '
  del(.workflowId)
  | .name = "AIGC Media Generation (fault demo)"
  | .nodes |= map(if .id == "node_image"
      then .executionPolicy = {timeoutMs: $timeout, maxAttempts: 2, backoff: "FIXED"}
      else . end)' "$FIXTURE")
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
log "dispatch record baseline seq=$record_after_seq"

log "pausing the Mock Provider barrier for task requests"
curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"kinds":["task"]}' "$MOCK_URL/control/pause" >/dev/null
barrier_paused=true

run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" \
  '{workflowId: $wf, definitionVersion: $v, input: {brief: "A lighthouse at dusk, fault demo"}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"

task_held() {
  curl -fsS "$MOCK_URL/control/barrier" | jq -e '[.held[] | select(.kind == "task")] | length >= 1' >/dev/null
}
wait_until "the image dispatch to be held by the Mock Provider" "$HELD_DEADLINE_S" task_held
held=$(curl -fsS "$MOCK_URL/control/barrier")
log "held at the Provider: $(jq -c '[.held[] | {kind, externalTaskId, arrivalSeq}]' <<<"$held")"

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
image_node_run=$(jq -r '.nodeRuns[] | select(.nodeId == "node_image") | .id' <<<"$snapshot")
detail=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$image_node_run")
log "before the kill: node_image $(jq -c '{status: .nodeRun.status, attempts: [.attempts[] | {attemptNo, status, bound: (.callbackBinding.externalTaskId // null)}]}' <<<"$detail")"

log "SIGKILL backend"
compose kill -s SIGKILL backend >/dev/null

log "releasing the barrier"
released=$(curl -fsS -X POST "$MOCK_URL/control/release" | jq -r '.released // 0')
barrier_paused=false
log "released $released held request(s); a request whose caller already disconnected is recorded as abandoned instead"

log "starting backend again"
compose start backend >/dev/null
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
record_after "$record_after_seq" | jq -c '{seq, event, kind, externalTaskId, status, replayed, delivery, error} | with_entries(select(.value != null))' >&2

if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$final_status" >"$DEMO_STATE"
fi

log "invariant report for run $run_id"
"$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id"
