#!/usr/bin/env bash
# Fault injection on a synchronous Tool call: SIGKILL the Backend while the photo Agent's
# first generate_image request is held at the Mock Provider, restart it, and show that
# the restarted process tells the Agent the outcome is unknown instead of resending the
# call or waiting for the Agent deadline.
#
# Sequence:
#   1. upload three photos and create a Definition from the photo effect-template fixture
#      bound to the scripted trajectory photo-set-sync-crash: one generation and one
#      review per photo, and, when a tool message reports RESULT_UNCERTAIN, one inserted
#      decision to generate that photo again;
#   2. pause the Mock Provider's barrier for synchronous image requests, then start a Run;
#   3. wait until the first generation is held at the Provider. Its Tool Attempt is
#      committed as STARTED, because the call only begins after the claim commits;
#   4. SIGKILL the Backend, release the barrier (the Provider records the request as
#      abandoned: its caller is gone), start the Backend again;
#   5. the restarted Backend's Reconciler finds the STARTED Attempt written by the killed
#      process and hands it back to the Agent: the Action fails with RESULT_UNCERTAIN and
#      failureSource RUNNER_LOST, and the Agent's next Turn receives a bounded tool message
#      saying the outcome is unknown and counted. The scripted Agent generates the photo
#      again and finishes the set;
#   6. check the outcome and print the evidence lines.
#
# Expects a COMPLETED Run whose media result is accepted with 3 of 3 passed; a first Turn
# whose Action FAILED with RESULT_UNCERTAIN and RUNNER_LOST; a second Turn whose Decision
# is a new generate_image that SUCCEEDED; exactly one RESULT_UNCERTAIN notice; a generation
# budget of 4 used (three photos plus the uncertain call); three image_generated facts
# matching the Provider's generated images; and four image requests arrived at the
# Provider, one of them abandoned: each generation call reached it exactly once.
#
# The held request must be released within the Backend's 30s image request timeout, so
# the Studio pause before the kill names that bound.
#
# Ends with the invariant report, like the other fault scripts.
#
# Usage: scripts/demo/photo-sync-crash.sh
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/photo_effect_template.json"
SCRIPT=photo-set-sync-crash
HELD_DEADLINE_S=60
RESTART_DEADLINE_S=120
TERMINAL_DEADLINE_S=120

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

barrier_paused=false
photos_dir=$(mktemp -d "${TMPDIR:-/tmp}/emberling-photos.XXXXXX")
cleanup() {
  if [[ "$barrier_paused" == true ]]; then
    curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null 2>&1 || true
  fi
  rm -rf "$photos_dir"
}
trap cleanup EXIT

log "checking that the Backend and the Mock Provider test controls are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "Mock Provider /control/barrier" 60 mock_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
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

log "pausing the Mock Provider barrier for synchronous image requests"
curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"kinds":["image"]}' "$MOCK_URL/control/pause" >/dev/null
barrier_paused=true

run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson photos "$photos" \
  '{workflowId: $wf, definitionVersion: $v, input: {brief: "One warm film look for the whole set", photos: $photos}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"
studio_run_link "$run_id"

image_held() {
  curl -fsS "$MOCK_URL/control/barrier" | jq -e '[.held[] | select(.kind == "image")] | length >= 1' >/dev/null
}
wait_until "the first generation to be held by the Mock Provider" "$HELD_DEADLINE_S" image_held

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
agent_node_run=$(jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id' <<<"$snapshot")
trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$agent_node_run/agent")
log "before the kill: $(jq -c '[.turns[] | {turnNo, action: .action.status, attempts: [.toolAttempts[] | {toolName, status}]}]' <<<"$trace")"

demo_pause "run $run_id: the first generate_image call is in flight, held at the Mock Provider, and its Tool Attempt is STARTED; next: SIGKILL the backend. Continue within ~30s (the Backend's image request timeout)"

log "SIGKILL backend"
compose kill -s SIGKILL backend >/dev/null
log "releasing the barrier"
released=$(curl -fsS -X POST "$MOCK_URL/control/release" | jq -r '.released // 0')
barrier_paused=false
log "released $released held request(s); a request whose caller already disconnected is recorded as abandoned"
demo_pause "backend killed with the generation's outcome unknown. Next: start the backend; its Reconciler hands the call back to the Agent as RESULT_UNCERTAIN"

log "starting backend again"
compose start backend >/dev/null
wait_until "restarted Backend /ready" "$RESTART_DEADLINE_S" backend_ready

run_terminal() {
  local status
  status=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" 2>/dev/null | jq -r '.run.status') || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}
wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
run_status=$(jq -r '.run.status' <<<"$snapshot")
trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$agent_node_run/agent")
events=$(run_events "$run_id")
window=$(record_after "$record_after_seq" | jq -s '.')

first_turn=$(jq -c '.turns[0] | {turnNo, decision: .decision.tool, action: .action.status,
    failureSource: .action.failureSource, code: .action.error.code,
    attempt: (.toolAttempts[0] | {status, code: (.error.code // null)})}' <<<"$trace")
next_turn=$(jq -c '.turns[1] | {turnNo, decision: .decision.tool, action: .action.status}' <<<"$trace")
failed_event=$(jq -c '[.[] | select(.type == "AGENT_ACTION_FAILED")] | first
    | {seq, type, failureSource: .payload.failureSource, code: .payload.error.code}' <<<"$events")
following=$(jq -c '(map(select(.type == "AGENT_ACTION_FAILED")) | first | .seq) as $s
    | [.[] | select(.seq > $s and (.type == "AGENT_TURN_READY" or .type == "AGENT_DECISION_COMMITTED"))][0:2]
    | map({seq, type})' <<<"$events")
notices=$(jq '[.turns[] | select(.action.error.code == "RESULT_UNCERTAIN")] | length' <<<"$trace")
used=$(jq '.generationBudget.generationCallsUsed' <<<"$trace")
max_calls=$(jq '.generationBudget.maxGenerationCalls' <<<"$trace")
generated_ledger=$(jq -c '[.facts.items[] | select(.factType == "image_generated") | .subject] | sort' <<<"$trace")
generated_record=$(jq -c '[.[] | select(.event == "generated" and .kind == "image") | "artifact_" + .sha256[0:32]] | sort' <<<"$window")
image_arrived=$(jq '[.[] | select(.event == "arrived" and .kind == "image")] | length' <<<"$window")
image_abandoned=$(jq '[.[] | select(.event == "abandoned" and .kind == "image")] | length' <<<"$window")
caption=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.caption // "{}"' <<<"$snapshot" | jq -c '.')

printf '\nphoto sync-crash story, run %s (record lines after seq %s)\n' "$run_id" "$record_after_seq"
printf 'evidence: orphaned round   %s\n' "$first_turn"
printf 'evidence: failed event     %s\n' "$failed_event"
printf 'evidence: then             %s\n' "$following"
printf 'evidence: Agent decision   %s\n' "$next_turn"
printf 'evidence: budget %s of %s, ledger generated %s, record image arrived %s abandoned %s generated %s\n\n' \
  "$used" "$max_calls" "$(jq 'length' <<<"$generated_ledger")" "$image_arrived" "$image_abandoned" "$(jq 'length' <<<"$generated_record")"

check run_completed "status=$run_status" test "$run_status" = COMPLETED
caption_accepted() {
  jq -e '.accepted == true and .passedCount == 3 and .photoCount == 3' <<<"$caption" >/dev/null
}
check media_result_accepted "$(jq -c '{accepted, passedCount, photoCount}' <<<"$caption")" caption_accepted
uncertain_round() {
  jq -e '.action == "FAILED" and .failureSource == "RUNNER_LOST" and .code == "RESULT_UNCERTAIN"
    and .attempt.status == "FAILED" and .attempt.code == "RESULT_UNCERTAIN"' <<<"$first_turn" >/dev/null
}
check uncertain_result_reported "$first_turn" uncertain_round
next_decision() {
  jq -e '.decision == "generate_image" and .action == "SUCCEEDED"' <<<"$next_turn" >/dev/null &&
    jq -e 'map(.type) == ["AGENT_TURN_READY", "AGENT_DECISION_COMMITTED"]' <<<"$following" >/dev/null
}
check agent_decided_to_regenerate "$next_turn" next_decision
check one_uncertain_notice "notices=$notices" test "$notices" -eq 1
check generation_budget "used=$used limit=$max_calls" test "$used" -eq 4
check record_generated_matches_ledger "record=$generated_record ledger=$generated_ledger" \
  test "$generated_record" = "$generated_ledger" -a "$(jq 'length' <<<"$generated_ledger")" -eq 3
check each_generation_reached_provider_once "arrived=$image_arrived abandoned=$image_abandoned used=$used" \
  test "$image_arrived" -eq "$used" -a "$image_abandoned" -eq 1

summary="first=$(jq -r '.code' <<<"$first_turn")/$(jq -r '.failureSource' <<<"$first_turn") next=$(jq -r '.decision' <<<"$next_turn"):$(jq -r '.action' <<<"$next_turn") budget=$used/$max_calls generated=$(jq 'length' <<<"$generated_record") image_arrived=$image_arrived abandoned=$image_abandoned"
if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'photo sync-crash story: %d check(s) FAILED\n' "$failures"
else
  printf 'photo sync-crash story: all checks passed\n'
fi

# Like the other fault scripts, this one ends with the invariant report.
log "invariant report for run $run_id"
invariant_status=0
"$DEMO_ROOT/scripts/demo/invariants.sh" "$run_id" || invariant_status=$?
((failures == 0 && invariant_status == 0))
