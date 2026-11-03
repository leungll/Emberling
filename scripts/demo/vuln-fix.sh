#!/usr/bin/env bash
# Vulnerability-fix stories: an Agent bound to a workspace of the example repository
# repairs a dependency advisory with the workspace Tools and verifies its patch through the
# sandbox runner's verification, which answers by callback. Every check reads the Backend's
# public API and the sandbox runner's request record; no live credential is involved.
#
# Usage: scripts/demo/vuln-fix.sh verify|scope-denied|stale-evidence|restart
#
#   verify          the scripted Agent reads the rules and the advisory, upgrades go.mod,
#                   verifies, adapts the calling code after the first verification fails
#                   and verifies again. Expects a COMPLETED Run, two verify_patch Attempts
#                   bound to the runner, one failed then one passed patch_verified fact
#                   whose subject is the latest workspace patch digest, a report naming
#                   that digest, two verifications at the runner and eight committed
#                   Decisions.
#   scope-denied    the Agent first writes under tests/, outside the allowed paths. Expects
#                   the Action FAILED with SCOPE_DENIED from AUTHORIZATION before any
#                   Attempt, nothing in the runner's record, the next Turn asked right after
#                   the refusal, one patch touching go.mod and a SCOPE_RESPECTED report.
#   stale-evidence  the Agent verifies the first patch, writes a second one and then names
#                   the first digest again. Expects that call FAILED with STALE_PATCH before
#                   any Attempt, two verifications at the runner for the first and the
#                   latest digest in that order, and a report of the latest digest.
#   restart         the verify story with the runner's callback held at its barrier while
#                   the first verification is DISPATCHED and the Agent waits; the Backend is
#                   killed with SIGKILL and started again, then the callback is released.
#                   Expects the original Attempt to complete once, the runner to have
#                   started that verification once and received exactly one more, the
#                   committed Decisions to grow by exactly the Turns after the held one
#                   (no Decision asked again) and the Run COMPLETED.
#
# EMBERLING_DEMO_MODEL=<model id> binds the Agent to a real decision model the Backend
# registers through EMBERLING_MODELS instead of the scripted mock model. Only the verify
# story runs with a real model, and its checks are the ones a real model must still meet:
# the Run completes, the report names the latest patch, and every patch_verified fact is
# about a patch the workspace recorded. No key is read or written here; the Backend's own
# environment supplies it. The scripted verify story asks the mock runner for one failed
# verification by its mock mode, which the docker runner refuses, so the scripted stories
# run against the mock runner only.
#
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

story=${1:-}
case "$story" in
verify | restart) script=vuln-fix-main-verify ;;
scope-denied) script=vuln-fix-scope-denied ;;
stale-evidence) script=vuln-fix-stale-evidence ;;
*) die "usage: vuln-fix.sh verify|scope-denied|stale-evidence|restart" ;;
esac
real_model=${EMBERLING_DEMO_MODEL:-}
if [[ -n "$real_model" && "$story" != verify ]]; then
  die "EMBERLING_DEMO_MODEL runs the verify story only; the other stories are scripted"
fi

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/vuln_fix.json"
HELD_DEADLINE_S=60
RESTART_DEADLINE_S=120
TERMINAL_DEADLINE_S=${EMBERLING_DEMO_VULN_TERMINAL_DEADLINE_S:-300}
ADVISORY=GO-2025-3553
SERVICE=token-service

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

paused=0
release_barrier() {
  if ((paused)); then
    curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null 2>&1 || true
  fi
}
trap release_barrier EXIT

log "checking that the Backend and the sandbox runner are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "sandbox runner /control/barrier" 60 runner_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null

if [[ -n "$real_model" ]]; then
  log "creating a Definition from $(basename "$FIXTURE") bound to model $real_model"
  definition=$(jq --arg model "$real_model" '
    del(.workflowId)
    | .name = "Vulnerability Fix (\($model))"
    | .nodes |= map(if .id == "node_agent"
        then .config.modelId = $model | del(.config.modelConfig)
        else . end)' "$FIXTURE")
else
  log "creating a Definition from $(basename "$FIXTURE") with script $script"
  definition=$(jq --arg script "$script" '
    del(.workflowId)
    | .name = "Vulnerability Fix (\($script))"
    | .nodes |= map(if .id == "node_agent" then .config.modelConfig.script = $script else . end)' "$FIXTURE")
fi
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
runner_after=$(record_last_seq "$RUNNER_URL")
log "record baselines: mock provider seq=$record_after_seq runner seq=$runner_after"

if [[ "$story" == restart ]]; then
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["callback"]}' "$RUNNER_URL/control/pause" >/dev/null
  paused=1
fi

task=$(jq -cn --arg advisory "$ADVISORY" --arg service "$SERVICE" \
  '{advisory: $advisory, goal: "PREPARE_ONLY", service: $service}')
run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson task "$task" \
  '{workflowId: $wf, definitionVersion: $v, input: {task: $task}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"
studio_run_link "$run_id"

agent_deadline_s=$(jq '.nodes[] | select(.id == "node_agent") | .config.timeoutMs / 1000 | floor' "$FIXTURE")
demo_pause "run $run_id ($story story) started; watch the Repair Agent in Studio. The Agent deadline is ${agent_deadline_s}s from its start; keep every look well under it"

agent_node_run() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id // empty'
}
agent_trace() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$(agent_node_run)/agent"
}
decisions_committed() {
  run_events "$run_id" | jq '[.[] | select(.type == "AGENT_DECISION_COMMITTED")] | length'
}
kill_and_restart_backend() {
  log "SIGKILL backend"
  compose kill -s SIGKILL backend >/dev/null
  log "starting backend again"
  compose start backend >/dev/null
  wait_until "restarted Backend /ready" "$RESTART_DEADLINE_S" backend_ready
}

held_attempt_before=""
decisions_before=0
arrivals_before=0
if [[ "$story" == restart ]]; then
  agent_waiting() {
    local node trace
    node=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .status') || return 1
    [[ "$node" == WAITING_CALLBACK ]] || return 1
    trace=$(agent_trace) || return 1
    jq -e '[.turns[].toolAttempts[] | select(.toolName == "verify_patch" and .status == "DISPATCHED")] | length == 1' <<<"$trace" >/dev/null
  }
  callback_held() {
    curl -fsS "$RUNNER_URL/control/barrier" | jq -e '[.held[] | select(.kind == "callback")] | length >= 1' >/dev/null
  }
  wait_until "the verify_patch Attempt to be DISPATCHED while the Agent waits for its callback" "$HELD_DEADLINE_S" agent_waiting
  bound=$(agent_trace | jq -c '[.turns[].toolAttempts[] | select(.toolName == "verify_patch")] | first | {id, verificationId: .callbackBinding.externalTaskId}')
  held_attempt_before=$(jq -r '.id' <<<"$bound")
  held_verification=$(jq -r '.verificationId' <<<"$bound")
  wait_until "the runner callback for verification $held_verification to be held" "$HELD_DEADLINE_S" callback_held
  decisions_before=$(decisions_committed)
  arrivals_before=$(record_after "$runner_after" "$RUNNER_URL" | jq -s '[.[] | select(.event == "arrived" and .kind == "verification")] | length')
  log "verify_patch Attempt $held_attempt_before DISPATCHED for verification $held_verification, its callback held at the runner; $decisions_before Decisions committed, $arrivals_before verification at the runner"
  demo_pause "verification callback for $held_verification held at the runner while the Agent waits in WAITING_CALLBACK; next: SIGKILL the backend and start it again"
  kill_and_restart_backend
  demo_pause "backend back; verify_patch Attempt $held_attempt_before is still DISPATCHED in Studio. Next: release the held callback and watch the Agent adapt the code and verify again"
  log "releasing the runner's callback"
  curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null
  paused=0
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
runner=$(record_after "$runner_after" "$RUNNER_URL" | jq -s '.')

verifications=$(jq -c '[.turns[] | .action as $a | .toolAttempts[] | select(.toolName == "verify_patch")
    | {id, status, verificationId: (.callbackBinding.externalTaskId // null), provider: (.callbackBinding.providerId // null), actionStatus: $a.status}]' <<<"$trace")
patches=$(jq -c '[.workspace.patches[]? | {seq, patchDigest, touchedPaths}]' <<<"$trace")
latest_digest=$(jq -r 'last.patchDigest // ""' <<<"$patches")
facts=$(jq -c '[.facts.items[] | select(.factType == "patch_verified") | {subject, verdict, toolAttemptId}]' <<<"$trace")
refusals=$(jq -c '[.turns[] | select(.action.status == "FAILED")
    | {turnNo, code: (.action.error.code // null), failureSource: (.action.failureSource // null), attempts: (.toolAttempts | length)}]' <<<"$trace")
termination=$(jq -r '.agentRun.termination // "none"' <<<"$trace")
result=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.text // "{}"' <<<"$snapshot" | jq -c '.' 2>/dev/null || echo '{}')
decisions=$(jq '[.[] | select(.type == "AGENT_DECISION_COMMITTED")] | length' <<<"$events")
callback_completions=$(jq '[.[] | select(.type == "AGENT_ACTION_COMPLETED" and .payload.completionSource == "CALLBACK")] | length' <<<"$events")
runner_arrivals=$(jq -c '[.[] | select(.event == "arrived" and .kind == "verification") | .patchDigest]' <<<"$runner")
runner_arrival_count=$(jq 'length' <<<"$runner_arrivals")

printf '\nvulnerability fix %s story, run %s\n' "$story" "$run_id"
printf 'run status: %s, agent termination: %s, verify_patch attempts: %s, workspace patches: %s, patch_verified facts: %s, runner verifications: %s, decisions committed: %s\n\n' \
  "$run_status" "$termination" "$(jq 'length' <<<"$verifications")" "$(jq 'length' <<<"$patches")" "$(jq 'length' <<<"$facts")" "$runner_arrival_count" "$decisions"

facts_about_recorded_patches() {
  jq -e --argjson patches "$patches" 'all(.[]; .subject as $s | any($patches[]; .patchDigest == $s))' <<<"$facts" >/dev/null
}
last_fact_is_latest_passed() {
  [[ -n "$latest_digest" ]] && jq -e --arg d "$latest_digest" 'length >= 1 and (last | .subject == $d and .verdict == true)' <<<"$facts" >/dev/null
}
report_names_latest() {
  [[ -n "$latest_digest" ]] && jq -e --arg d "$latest_digest" '.patchDigest == $d and .passed == true' <<<"$result" >/dev/null
}
all_verifications_succeeded() {
  jq -e 'all(.[]; .status == "SUCCEEDED" and .provider == "sandboxrunner")' <<<"$verifications" >/dev/null
}

if [[ -n "$real_model" ]]; then
  check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
  check verified_at_least_once "verifications=$verifications" test "$(jq 'length' <<<"$verifications")" -ge 1
  check facts_about_recorded_patches "facts=$facts patches=$(jq -c 'map(.patchDigest)' <<<"$patches")" facts_about_recorded_patches
  check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
  check report_names_latest "result=$result" report_names_latest
  summary="model=$real_model verifications=$(jq 'length' <<<"$verifications") patches=$(jq 'length' <<<"$patches") latest=$latest_digest runner_verifications=$runner_arrival_count status=$run_status"
else
  case "$story" in
  verify)
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check two_verifications_succeeded "verifications=$verifications" \
      test "$(jq 'length' <<<"$verifications")" -eq 2 -a "$(all_verifications_succeeded && echo yes)" = yes
    check one_failed_then_passed_fact "facts=$facts" \
      jq -e 'length == 2 and .[0].verdict == false and .[1].verdict == true' <<<"$facts"
    check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
    check facts_about_recorded_patches "patches=$(jq -c 'map(.patchDigest)' <<<"$patches")" facts_about_recorded_patches
    check report_names_latest "result=$result" report_names_latest
    check runner_verified_twice "arrivals=$runner_arrivals" test "$runner_arrival_count" -eq 2
    check eight_decisions_committed "decisions=$decisions" test "$decisions" -eq 8
    summary="verifications=$(jq 'length' <<<"$verifications") facts=$(jq -c 'map(.verdict)' <<<"$facts") latest=$latest_digest runner_verifications=$runner_arrival_count decisions=$decisions status=$run_status"
    ;;
  scope-denied)
    refusal_seq=$(jq '[.[] | select(.type == "AGENT_ACTION_FAILED" and .payload.error.code == "SCOPE_DENIED") | .seq] | first // 0' <<<"$events")
    following=$(jq -c --argjson s "$refusal_seq" '[.[] | select(.seq > $s and (.type == "AGENT_TURN_READY" or .type == "AGENT_DECISION_COMMITTED"))][0:2] | map(.type)' <<<"$events")
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check refused_before_any_attempt "refusals=$refusals" \
      jq -e 'length == 1 and .[0].turnNo == 1 and .[0].code == "SCOPE_DENIED" and .[0].failureSource == "AUTHORIZATION" and .[0].attempts == 0' <<<"$refusals"
    check next_turn_received_refusal "refusal_seq=$refusal_seq then=$following" \
      jq -e '. == ["AGENT_TURN_READY", "AGENT_DECISION_COMMITTED"]' <<<"$following"
    check no_external_call "runner_lines=$(jq 'length' <<<"$runner") verifications=$(jq 'length' <<<"$verifications")" \
      test "$(jq 'length' <<<"$runner")" -eq 0 -a "$(jq 'length' <<<"$verifications")" -eq 0
    check one_patch_inside_scope "patches=$patches" \
      jq -e 'length == 1 and .[0].touchedPaths == ["go.mod"]' <<<"$patches"
    check report_respects_scope "result=$result" \
      jq -e --arg d "$latest_digest" '.status == "SCOPE_RESPECTED" and .patchDigest == $d' <<<"$result"
    summary="refusal=SCOPE_DENIED attempts=0 runner_lines=$(jq 'length' <<<"$runner") patches=$(jq 'length' <<<"$patches") decisions=$decisions status=$run_status"
    ;;
  stale-evidence)
    first_digest=$(jq -r 'first.patchDigest // ""' <<<"$patches")
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check stale_digest_refused "refusals=$refusals" \
      jq -e 'length == 1 and .[0].code == "STALE_PATCH" and .[0].failureSource == "AUTHORIZATION" and .[0].attempts == 0' <<<"$refusals"
    check two_verifications_succeeded "verifications=$verifications" \
      test "$(jq 'length' <<<"$verifications")" -eq 2 -a "$(all_verifications_succeeded && echo yes)" = yes
    check runner_asked_first_then_latest "arrivals=$runner_arrivals first=$first_digest latest=$latest_digest" \
      jq -e --arg f "$first_digest" --arg l "$latest_digest" '. == [$f, $l]' <<<"$runner_arrivals"
    check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
    check report_names_latest "result=$result" report_names_latest
    summary="refusal=STALE_PATCH verifications=$(jq 'length' <<<"$verifications") runner_verifications=$runner_arrival_count first=$first_digest latest=$latest_digest status=$run_status"
    ;;
  restart)
    held_after=$(jq -c --arg id "$held_attempt_before" '[.[] | select(.id == $id)] | first // {}' <<<"$verifications")
    held_started=$(jq --arg id "$held_verification" '[.[] | select(.event == "started" and .verificationId == $id)] | length' <<<"$runner")
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check original_attempt_completed "before=$held_attempt_before after=$held_after" \
      jq -e '.status == "SUCCEEDED" and .actionStatus == "SUCCEEDED"' <<<"$held_after"
    check two_verifications_succeeded "verifications=$verifications" \
      test "$(jq 'length' <<<"$verifications")" -eq 2 -a "$(all_verifications_succeeded && echo yes)" = yes
    check held_verification_ran_once "verification=$held_verification started=$held_started" test "$held_started" -eq 1
    check runner_asked_once_more "before=$arrivals_before after=$runner_arrival_count" \
      test "$arrivals_before" -eq 1 -a "$runner_arrival_count" -eq 2
    check no_decision_asked_again "decisions before=$decisions_before after=$decisions" \
      test "$decisions_before" -eq 5 -a "$decisions" -eq 8
    check callbacks_completed_twice "callback_completions=$callback_completions" test "$callback_completions" -eq 2
    check report_names_latest "result=$result" report_names_latest
    summary="attempt=$held_attempt_before verification=$held_verification decisions=$decisions_before/$decisions runner_verifications=$arrivals_before/$runner_arrival_count callback_completions=$callback_completions status=$run_status"
    ;;
  esac
fi

if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'vulnerability fix %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'vulnerability fix %s story: all checks passed\n' "$story"
