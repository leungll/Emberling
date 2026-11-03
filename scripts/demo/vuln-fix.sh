#!/usr/bin/env bash
# Vulnerability-fix stories: an Agent bound to a workspace of the example repository
# repairs a dependency advisory with the workspace Tools and verifies its patch through the
# sandbox runner's verification, which answers by callback. Every check reads the Backend's
# public API and the sandbox runner's request record; no live credential is involved.
#
# Usage: scripts/demo/vuln-fix.sh [definition] STORY
#   STORY is verify, scope-denied, stale-evidence, restart or incomplete-final (the repair
#   and its verification), or one of the release stories: main, restart-during-approval,
#   duplicate-approval, reject-resubmit, release-crash-deployed, release-crash-voided,
#   void-exhausted, target-occupied, release-refused, unhealthy, prepare-only,
#   release-unreachable-stop, stop-then-deployed.
#
# "definition" prints the Definition the story would save, as JSON, and touches nothing:
# it needs jq only, and the contract tests read it to check what the environment changes.
#
# The repair stories and prepare-only run a PREPARE_ONLY task, so the Runtime's completion
# check accepts the Agent's answer only once the latest patch is verified and a change
# request names it: the Run then COMPLETED with the verdict READY_FOR_APPROVAL, and the
# result node receives that verdict with the Agent's own report inside it. The other
# release stories run a FIX_AND_DEPLOY task, which completes only once the release of the
# latest patch is confirmed at the target.
#
#   verify          the scripted Agent reads the rules and the advisory, upgrades go.mod
#                   and the import path without adapting the code, verifies, adapts the
#                   calling code after that verification fails (the upgrade does not
#                   compile), verifies again and records the change request. Expects a
#                   COMPLETED Run judged READY_FOR_APPROVAL, two verify_patch Attempts bound
#                   to the runner, one failed then one passed patch_verified fact whose
#                   subject is the latest workspace patch digest, exactly one change
#                   request about that digest, a report naming it, two verifications at the
#                   runner and ten committed Decisions.
#   scope-denied    the Agent first writes under tests/, outside the allowed paths. Expects
#                   the Action FAILED with SCOPE_DENIED from AUTHORIZATION before any
#                   Attempt, the next Turn asked right after the refusal, nothing at the
#                   runner but the one verification the Agent made afterwards, every patch
#                   inside go.mod and internal/auth/, a SCOPE_RESPECTED report and the task
#                   READY_FOR_APPROVAL.
#   stale-evidence  the Agent verifies the first patch, writes a second one and then names
#                   the first digest again. Expects that call FAILED with STALE_PATCH before
#                   any Attempt, two verifications at the runner for the first and the
#                   latest digest in that order, a report of the latest digest and the task
#                   READY_FOR_APPROVAL.
#   restart         the verify story with the runner's callback held at its barrier while
#                   the first verification is DISPATCHED and the Agent waits; the Backend is
#                   killed with SIGKILL and started again, then the callback is released.
#                   Expects the original Attempt to complete once, the runner to have
#                   started that verification once and received exactly one more, the
#                   committed Decisions to grow by exactly the Turns after the held one
#                   (no Decision asked again) and the Run COMPLETED, READY_FOR_APPROVAL.
#   incomplete-final
#                   the Agent fails the verification of the unadapted upgrade, adapts the
#                   code and declares completion at once, without verifying the adapted
#                   patch or recording a change request. Expects a FAILED Run whose Agent
#                   answered FINAL and was judged INCOMPLETE with exactly
#                   PATCH_VERIFICATION_MISSING and CHANGE_REQUEST_MISSING, the latest-patch
#                   and change-request checks failed, one verification at the runner, of an
#                   earlier patch, no change request and eight committed Decisions.
#
# The release stories, main to stop-then-deployed, are described in vuln-fix-release.sh.
#
# EMBERLING_DEMO_MODEL=<model id> binds the Agent to a real decision model the Backend
# registers through EMBERLING_MODELS instead of the scripted mock model. Only the verify
# and the main story run with a real model, and their checks are the ones a real model
# must still meet: verify completes READY_FOR_APPROVAL with exactly one change request
# about the latest patch; main completes DEPLOYED with a healthy release_confirmed fact
# about the latest patch, every approved release request deployed at most once and every
# patch_verified fact about a patch the workspace recorded. A real model needs more time
# than the scripted one, so the Agent's timeoutMs becomes EMBERLING_DEMO_MODEL_TIMEOUT_MS
# (default 1800000, thirty minutes) and the wait for the terminal Run grows to that budget
# plus a minute; the scripted stories keep the fixture's budget. No key is read or written
# here; the Backend's own environment supplies it (EMBERLING_MODELS,
# EMBERLING_MODEL_BASE_URL, EMBERLING_MODEL_API_KEY and, for one model call, the optional
# EMBERLING_MODEL_TIMEOUT). The scripted stories pass no mock mode: the failed verification
# is the real verdict on an upgrade that does not compile, which the mock runner judges
# from the patch the same way the docker runner's build does, so every story holds against
# either runner.
#
# EMBERLING_DEMO_APPROVAL=manual (main story only) leaves the approval to a person: each
# time the Agent asks for a release, the script prints the Studio approval page and waits,
# at most EMBERLING_DEMO_APPROVAL_WAIT_S seconds (default 1800), until someone approves or
# rejects it there; a rejected request goes back to the Agent, which may ask again. The
# default, scripted, approves through the operator API. Either way the approval deadline
# the Definition carries covers the wait.
#
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary; target-occupied lists both Runs and
# release-unreachable-stop all three, the stopped Run first.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools jq

RELEASE_STORIES="main restart-during-approval duplicate-approval reject-resubmit release-crash-deployed release-crash-voided void-exhausted target-occupied release-refused unhealthy prepare-only release-unreachable-stop stop-then-deployed"
STORIES="verify scope-denied stale-evidence restart incomplete-final $RELEASE_STORIES"
usage="usage: vuln-fix.sh [definition] ${STORIES// /|}"

mode=run
if [[ "${1:-}" == definition ]]; then
  mode=definition
  shift
fi
story=${1:-}
case "$story" in
verify | restart) script=vuln-fix-main-verify ;;
scope-denied) script=vuln-fix-scope-denied ;;
incomplete-final) script=vuln-fix-incomplete-final ;;
stale-evidence) script=vuln-fix-stale-evidence ;;
reject-resubmit) script=vuln-fix-rejected-resubmit ;;
main | restart-during-approval | duplicate-approval | release-crash-deployed | release-crash-voided | \
  void-exhausted | target-occupied | release-refused | unhealthy | prepare-only | release-unreachable-stop | \
  stop-then-deployed) script=vuln-fix-release ;;
*) die "$usage" ;;
esac
real_model=${EMBERLING_DEMO_MODEL:-}
if [[ -n "$real_model" && "$story" != verify && "$story" != main ]]; then
  die "EMBERLING_DEMO_MODEL runs the verify and the main story only; the other stories are scripted"
fi
release_story=0
[[ " $RELEASE_STORIES " == *" $story "* ]] && release_story=1
# Every release story but prepare-only decides an approval.
decides_approval=$release_story
[[ "$story" == prepare-only ]] && decides_approval=0
approval_mode=${EMBERLING_DEMO_APPROVAL:-scripted}
case "$approval_mode" in
scripted) ;;
manual) [[ "$story" == main ]] || die "EMBERLING_DEMO_APPROVAL=manual runs the main story only" ;;
*) die "EMBERLING_DEMO_APPROVAL must be scripted or manual" ;;
esac
APPROVAL_WAIT_S=${EMBERLING_DEMO_APPROVAL_WAIT_S:-1800}
[[ "$APPROVAL_WAIT_S" =~ ^[1-9][0-9]*$ ]] || die "EMBERLING_DEMO_APPROVAL_WAIT_S must be a positive number of seconds"
# The approval routes' credential, which the Backend reads from its own environment. It
# is read here from the shell, sent in a header and never printed or stored.
OPERATOR_TOKEN=${EMBERLING_OPERATOR_TOKEN:-}
OPERATOR_HEADER="X-Emberling-Operator-Token: $OPERATOR_TOKEN"
# The release target the release stories bind the Agent to, and how long each release wait
# is allowed: a short void grace where an unknown operation is meant to be voided while the
# audience watches; a long confirm deadline, so no human wait opens by itself, except in
# the two stop stories, where the release is meant to reach a person while the audience
# watches. A person deciding by hand gets the whole manual wait as the approval deadline.
RELEASE_SERVICE=token-service
RELEASE_ENVIRONMENT=staging
RELEASE_APPROVAL_TIMEOUT_MS=600000
if [[ "$approval_mode" == manual ]] && ((APPROVAL_WAIT_S * 1000 + 60000 > RELEASE_APPROVAL_TIMEOUT_MS)); then
  RELEASE_APPROVAL_TIMEOUT_MS=$((APPROVAL_WAIT_S * 1000 + 60000))
fi
RELEASE_CONFIRM_TIMEOUT_MS=120000
RELEASE_HUMAN_WAIT_TIMEOUT_MS=600000
RELEASE_MAX_RETRIES=1
case "$story" in
release-crash-voided | void-exhausted) RELEASE_VOID_GRACE_MS=10000 ;;
release-unreachable-stop | stop-then-deployed)
  RELEASE_VOID_GRACE_MS=10000
  RELEASE_CONFIRM_TIMEOUT_MS=15000
  ;;
*) RELEASE_VOID_GRACE_MS=60000 ;;
esac
# The resubmitting Agent makes four more Decisions than the main story; a real model gets
# room for its own pace.
max_turns=""
[[ "$story" == reject-resubmit ]] && max_turns=16
[[ -n "$real_model" && "$story" == main ]] && max_turns=24
REFUSAL_CODE=OUTSIDE_RELEASE_WINDOW
REJECT_REASON="Require an expiry on every token through the v5 validator before this goes to staging."
RELEASE_INSTRUCTIONS='Once the latest patch has passed verification, describe it with create_change_request, citing as its decision basis the workspace files it relied on, each by its path and the number of a turn whose request already carried its contents, then release it with deploy_change naming that change request; both calls return REJECTED tool messages for a definite refusal. A release needs a person to approve it; wait for the result and finish with {"status","advisory","patchDigest","changeRequestId","operationKey","version","healthy"}. If the person rejects the release, address the reason, verify again, describe the new patch in a new change request and ask again.'

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/vuln_fix.json"
HELD_DEADLINE_S=60
RESTART_DEADLINE_S=120
ADVISORY=GO-2025-3553
SERVICE=token-service
# The Agent budget the saved Definition carries: the fixture's for a scripted story, the
# real-model budget otherwise. The wait for the terminal Run covers that budget and a
# minute of Tool and runner work after it.
agent_timeout_ms=$(jq '.nodes[] | select(.id == "node_agent") | .config.timeoutMs' "$FIXTURE")
if [[ -n "$real_model" ]]; then
  agent_timeout_ms=${EMBERLING_DEMO_MODEL_TIMEOUT_MS:-1800000}
  [[ "$agent_timeout_ms" =~ ^[1-9][0-9]*$ ]] || die "EMBERLING_DEMO_MODEL_TIMEOUT_MS must be a positive integer of milliseconds"
fi
TERMINAL_DEADLINE_S=${EMBERLING_DEMO_VULN_TERMINAL_DEADLINE_S:-$((agent_timeout_ms / 1000 + 60))}
if ((TERMINAL_DEADLINE_S < agent_timeout_ms / 1000 + 60)); then
  TERMINAL_DEADLINE_S=$((agent_timeout_ms / 1000 + 60))
fi

# build_definition prints the Definition this story saves: the fixture with its Agent bound
# to the scripted trajectory, or to the real model with the real-model budget, and for a
# release story the release Tools, the frozen deploy target and the release bounds.
build_definition() {
  jq --arg model "$real_model" --argjson timeout "$agent_timeout_ms" --arg script "$script" \
    --argjson release "$release_story" --arg maxTurns "$max_turns" \
    --arg service "$RELEASE_SERVICE" --arg environment "$RELEASE_ENVIRONMENT" --arg instructions "$RELEASE_INSTRUCTIONS" \
    --argjson approval "$RELEASE_APPROVAL_TIMEOUT_MS" --argjson confirm "$RELEASE_CONFIRM_TIMEOUT_MS" \
    --argjson grace "$RELEASE_VOID_GRACE_MS" --argjson retries "$RELEASE_MAX_RETRIES" --argjson human "$RELEASE_HUMAN_WAIT_TIMEOUT_MS" '
    del(.workflowId)
    | .name = (if $model != "" then "Vulnerability Fix (\($model))" else "Vulnerability Fix (\($script))" end)
    | if $release == 1 then
        .description = "An Agent repairs a dependency vulnerability inside a bound workspace, verifies the patch, describes it in a change request and releases it to a frozen deploy target once a person approves"
      else . end
    | .nodes |= map(if .id == "node_agent" then
        (if $model != "" then .config.modelId = $model | del(.config.modelConfig) | .config.timeoutMs = $timeout
         else .config.modelConfig.script = $script end)
        | (if $maxTurns != "" then .config.maxTurns = ($maxTurns | tonumber) else . end)
        | (if $release == 1 then
            .config.instructions += " " + $instructions
            | .config.allowedTools |= (. + (["create_change_request", "deploy_change"] - .))
            | .config.workspace.deployTarget = {service: $service, environment: $environment}
            | .config.approvalTimeoutMs = $approval
            | .config.releaseConfirmTimeoutMs = $confirm
            | .config.voidGraceMs = $grace
            | .config.maxReleaseRetries = $retries
            | .config.humanWaitTimeoutMs = $human
          else . end)
      else . end)' "$FIXTURE"
}
if [[ "$mode" == definition ]]; then
  build_definition
  exit 0
fi
require_tools docker curl
if ((decides_approval)) && [[ ${#OPERATOR_TOKEN} -lt 32 ]]; then
  die "the $story story decides an approval: set EMBERLING_OPERATOR_TOKEN (at least 32 bytes) in the shell, the same value the Backend was started with"
fi

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

# Production's fault switches, completion pause and barrier are cleared whenever the script
# ends, so an interrupted story never leaves a fault for the next one.
paused=0
production_touched=0
production_reset() {
  curl -fsS -X POST "$PRODUCTION_URL/control/release" >/dev/null 2>&1 || true
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{}' "$PRODUCTION_URL/control/v2/faults" >/dev/null 2>&1 || true
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"paused":false}' "$PRODUCTION_URL/control/v2/completion" >/dev/null 2>&1 || true
}
cleanup() {
  if ((paused)); then
    curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null 2>&1 || true
  fi
  if ((production_touched)); then
    production_reset
  fi
}
trap cleanup EXIT
production_faults() {
  curl -fsS -X POST -H 'Content-Type: application/json' --data "$1" "$PRODUCTION_URL/control/v2/faults" >/dev/null
}
production_completion() {
  curl -fsS -X POST -H 'Content-Type: application/json' --data "{\"paused\":$1}" "$PRODUCTION_URL/control/v2/completion" >/dev/null
}

log "checking that the Backend and the sandbox runner are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "sandbox runner /control/barrier" 60 runner_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null
if ((release_story)); then
  wait_until "mock production /control/barrier" 60 production_ready
  production_touched=1
  production_reset
fi
if ((decides_approval)); then
  # The approval routes are served only when the Backend has the operator credential.
  curl -fsS -o /dev/null -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals?status=PENDING" ||
    die "the Backend does not serve the approval routes for this operator credential; start the stack with the same EMBERLING_OPERATOR_TOKEN"
fi

if [[ -n "$real_model" ]]; then
  log "creating a Definition from $(basename "$FIXTURE") bound to model $real_model with a ${agent_timeout_ms}ms Agent budget"
else
  log "creating a Definition from $(basename "$FIXTURE") with script $script"
fi
definition=$(build_definition)
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
runner_after=$(record_last_seq "$RUNNER_URL")
production_after=0
if ((release_story)); then
  production_after=$(record_last_seq "$PRODUCTION_URL")
fi
log "record baselines: mock provider seq=$record_after_seq runner seq=$runner_after production seq=$production_after"

if [[ "$story" == restart ]]; then
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["callback"]}' "$RUNNER_URL/control/pause" >/dev/null
  paused=1
fi

goal=PREPARE_ONLY
((release_story)) && [[ "$story" != prepare-only ]] && goal=FIX_AND_DEPLOY
# create_run starts one Run of the saved Definition over the repair task and prints its id.
create_run() {
  local task run_request id
  task=$(jq -cn --arg advisory "$ADVISORY" --arg service "$SERVICE" --arg goal "$goal" \
    '{advisory: $advisory, goal: $goal, service: $service}')
  run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson task "$task" \
    '{workflowId: $wf, definitionVersion: $v, input: {task: $task}}')
  id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
    --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
  log "run $id created"
  studio_run_link "$id"
  printf '%s\n' "$id"
}

# The target-occupied story starts the Run that holds the target first; the story's own
# Run is the second task, created once the first holds the target.
holder_run_id=""
if [[ "$story" == target-occupied ]]; then
  production_completion true
  holder_run_id=$(create_run)
  run_id=""
else
  run_id=$(create_run)
fi

agent_deadline_s=$((agent_timeout_ms / 1000))
demo_pause "run ${run_id:-$holder_run_id} ($story story) started; watch the Repair Agent in Studio. The Agent deadline is ${agent_deadline_s}s from its start; keep every look well under it"

agent_node_run_of() {
  curl -fsS "$BACKEND_URL/api/runs/$1" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id // empty'
}
agent_trace_of() {
  curl -fsS "$BACKEND_URL/api/runs/$1/nodes/$(agent_node_run_of "$1")/agent"
}
agent_trace() { agent_trace_of "$run_id"; }
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
run_status_of() {
  curl -fsS "$BACKEND_URL/api/runs/$1" 2>/dev/null | jq -r '.run.status'
}
run_terminal_of() {
  local status
  status=$(run_status_of "$1") || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}

# write_demo_state records the story's Runs, status and summary for make demo.
write_demo_state() {
  local state_run_ids=${run_id:-$holder_run_id}
  [[ -n "$run_id" && -n "$holder_run_id" ]] && state_run_ids="$run_id,$holder_run_id"
  [[ -n "${later_run_ids:-}" ]] && state_run_ids+=",$later_run_ids"
  if [[ -n "${DEMO_STATE:-}" ]]; then
    printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
      "$state_run_ids" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
  fi
}
# A Run that ends while the story still waits for something only a running Run can do
# fails the story at once, whatever end the story expects: the story's path needed that
# step before the end. make demo still gets the Run for its invariant report and ledger.
run_ended_hook() {
  run_status="$2 (ended while waiting for: $4)"
  summary="run $1 ended $2 with error $3 while waiting for: $4"
  write_demo_state
  printf '\nvulnerability fix %s story: FAILED, run %s ended %s (error %s) while waiting for: %s\n' "$story" "$1" "$2" "$3" "$4"
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
  await_running "$run_id" "the verify_patch Attempt to be DISPATCHED while the Agent waits for its callback" "$HELD_DEADLINE_S" agent_waiting
  bound=$(agent_trace | jq -c '[.turns[].toolAttempts[] | select(.toolName == "verify_patch")] | first | {id, verificationId: .callbackBinding.externalTaskId}')
  held_attempt_before=$(jq -r '.id' <<<"$bound")
  held_verification=$(jq -r '.verificationId' <<<"$bound")
  await_running "$run_id" "the runner callback for verification $held_verification to be held" "$HELD_DEADLINE_S" callback_held
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

# --- The release stories: their helpers, the faults they inject and their checks.
# shellcheck source=scripts/demo/vuln-fix-release.sh
source "$DEMO_ROOT/scripts/demo/vuln-fix-release.sh"
if ((decides_approval)); then
  drive_release_story
fi

wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal_of "$run_id"
if [[ "$story" == duplicate-approval ]]; then
  # A later submission of the decided approval is answered as repeated and writes nothing.
  events_before_late=$(run_events "$run_id" | jq 'length')
  late_answer=$(decide "$release_approval_id" APPROVE "Verified patch; release to staging." | jq -c '{status: .approval.status, repeated}')
  events_after_late=$(run_events "$run_id" | jq 'length')
fi

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
completion=$(jq -c '.agentRun.completion // {}' <<<"$trace")
outcome=$(jq -r '.outcome // "none"' <<<"$completion")
change_requests=$(jq -c '[.changeRequests.items[]? | {id, patchDigest}]' <<<"$trace")
# The result node receives the completion verdict with the Agent's FINAL report inside it;
# the fixture Agent answers in text form, so agentReport is a JSON string holding the report.
# A FAILED Agent NodeRun publishes nothing, so a task judged INCOMPLETE has no report here.
result=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.text // "{}"' <<<"$snapshot" | jq -c '.' 2>/dev/null || echo '{}')
[[ -n "$result" ]] || result='{}'
report=$(jq -c '.agentReport // "{}" | if type == "string" then (fromjson? // {}) else . end' <<<"$result" 2>/dev/null || echo '{}')
agent_node_error=$(jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .error.code // "none"' <<<"$snapshot")
decisions=$(jq '[.[] | select(.type == "AGENT_DECISION_COMMITTED")] | length' <<<"$events")
callback_completions=$(jq '[.[] | select(.type == "AGENT_ACTION_COMPLETED" and .payload.completionSource == "CALLBACK")] | length' <<<"$events")
runner_arrivals=$(jq -c '[.[] | select(.event == "arrived" and .kind == "verification") | .patchDigest]' <<<"$runner")
runner_arrival_count=$(jq 'length' <<<"$runner_arrivals")
# The release facts: every release Turn with its approval, Attempts, operations and
# occupation, the release_confirmed facts and production's release lines since the
# baseline. Empty for the other stories.
releases='[]'
release_facts='[]'
production='[]'
last_turn='{}'
if ((release_story)); then
  releases=$(jq -c '[.turns[] | select(.decision.tool == "deploy_change" and .action != null) | .turnNo as $turn | .action as $a | {
      turnNo: $turn, actionId: $a.id, actionStatus: $a.status, code: ($a.error.code // null), failureSource: ($a.failureSource // null),
      approval: ($a.approval | if . == null then null else {id, status, reason} end),
      operations: [($a.release.operations // [])[] | {operationKey, outcome, status}],
      occupation: ($a.release.occupation | if . == null then null else {status, releaseReason, operationKey, nextQueryAt, deployedUncheckedVersion} end),
      humanWait: ($a.release.humanWait | if . == null then null else {status, outcome, closedBy} end),
      attempts: [.toolAttempts[] | select(.toolName == "deploy_change")
        | {attemptNo, status, code: (.error.code // null), provider: (.callbackBinding.providerId // null), operationKey: (.callbackBinding.externalTaskId // null)}]}]' <<<"$trace")
  release_facts=$(jq -c '[.facts.items[] | select(.factType == "release_confirmed") | {subject, verdict, toolAttemptId}]' <<<"$trace")
  production=$(record_after "$production_after" "$PRODUCTION_URL" | jq -s '[.[] | select(.kind == "release")]')
  last_turn=$(jq -c '.turns | last | {turnNo, kind: .decision.kind}' <<<"$trace")
fi
production_of() { jq --arg op "$1" --arg event "$2" '[.[] | select(.operationId == $op and .event == $event)] | length' <<<"$production"; }
deployed_under() { jq --arg a "op_$1_" '[.[] | select(.event == "deployed" and (.operationId | startswith($a)))] | length' <<<"$production"; }
release_of() { jq -c --arg id "$1" '[.[] | select(.actionId == $id)] | first // {}' <<<"$releases"; }

printf '\nvulnerability fix %s story, run %s\n' "$story" "$run_id"
printf 'run status: %s, agent termination: %s, completion: %s, verify_patch attempts: %s, workspace patches: %s, patch_verified facts: %s, change requests: %s, runner verifications: %s, decisions committed: %s\n\n' \
  "$run_status" "$termination" "$outcome" "$(jq 'length' <<<"$verifications")" "$(jq 'length' <<<"$patches")" "$(jq 'length' <<<"$facts")" "$(jq 'length' <<<"$change_requests")" "$runner_arrival_count" "$decisions"

facts_about_recorded_patches() {
  jq -e --argjson patches "$patches" 'all(.[]; .subject as $s | any($patches[]; .patchDigest == $s))' <<<"$facts" >/dev/null
}
last_fact_is_latest_passed() {
  [[ -n "$latest_digest" ]] && jq -e --arg d "$latest_digest" 'length >= 1 and (last | .subject == $d and .verdict == true)' <<<"$facts" >/dev/null
}
report_names_latest() {
  [[ -n "$latest_digest" ]] && jq -e '.outcome == "READY_FOR_APPROVAL"' <<<"$result" >/dev/null \
    && jq -e --arg d "$latest_digest" '.patchDigest == $d and .passed == true' <<<"$report" >/dev/null
}
ready_for_approval() {
  jq -e '.outcome == "READY_FOR_APPROVAL" and (.missing | length) == 0 and all(.checks[]; .passed)' <<<"$completion" >/dev/null
}
one_change_request_for_latest() {
  [[ -n "$latest_digest" ]] && jq -e --arg d "$latest_digest" 'length == 1 and .[0].patchDigest == $d' <<<"$change_requests" >/dev/null
}
all_verifications_succeeded() {
  jq -e 'all(.[]; .status == "SUCCEEDED" and .provider == "sandboxrunner")' <<<"$verifications" >/dev/null
}

if [[ -n "$real_model" && "$story" == verify ]]; then
  check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
  check verified_at_least_once "verifications=$verifications" test "$(jq 'length' <<<"$verifications")" -ge 1
  check facts_about_recorded_patches "facts=$facts patches=$(jq -c 'map(.patchDigest)' <<<"$patches")" facts_about_recorded_patches
  check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
  check ready_for_approval "completion=$completion" ready_for_approval
  check one_change_request_for_latest "change_requests=$change_requests latest=$latest_digest" one_change_request_for_latest
  check report_names_latest "result=$result" report_names_latest
  summary="model=$real_model verifications=$(jq 'length' <<<"$verifications") patches=$(jq 'length' <<<"$patches") latest=$latest_digest change_requests=$(jq 'length' <<<"$change_requests") runner_verifications=$runner_arrival_count completion=$outcome status=$run_status"
elif [[ -n "$real_model" || "$approval_mode" == manual ]]; then
  # The main story with a real model or a person deciding: the model chooses its own path
  # and a person may reject a request, so the checks are what any honest release meets.
  approved_actions=$(jq -c '[.[] | select(.approval.status == "APPROVED") | .actionId]' <<<"$releases")
  check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
  check deployed "completion=$completion" \
    jq -e '.outcome == "DEPLOYED" and (.missing | length) == 0 and all(.checks[]; .passed)' <<<"$completion"
  check facts_about_recorded_patches "facts=$facts patches=$(jq -c 'map(.patchDigest)' <<<"$patches")" facts_about_recorded_patches
  check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
  check release_fact_healthy_about_latest "latest=$latest_digest release_facts=$release_facts" \
    jq -e --arg d "$latest_digest" 'length >= 1 and (last | .subject == $d and .verdict == true)' <<<"$release_facts"
  check each_approved_request_deployed_once "approved=$approved_actions releases=$(jq -c 'map({actionId, actionStatus, approval: .approval.status})' <<<"$releases")" \
    jq -e --argjson p "$production" 'all(.[] | select(.approval.status == "APPROVED"); .actionId as $a
      | ([$p[] | select(.event == "deployed" and (.operationId | startswith("op_\($a)_")))] | length) <= 1)' <<<"$releases"
  check unapproved_requests_never_released "releases=$(jq -c 'map({approval: .approval.status, attempts: (.attempts | length)})' <<<"$releases")" \
    jq -e 'all(.[] | select(.approval.status != "APPROVED"); (.attempts | length) == 0)' <<<"$releases"
  summary="model=${real_model:-scripted} approval=$approval_mode decisions_by_person=$(jq -c 'map(.status)' <<<"$manual_decisions") requests=$(jq 'length' <<<"$releases") latest=$latest_digest completion=$outcome status=$run_status"
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
    check ready_for_approval "completion=$completion" ready_for_approval
    check one_change_request_for_latest "change_requests=$change_requests latest=$latest_digest" one_change_request_for_latest
    check report_names_latest "result=$result" report_names_latest
    check runner_verified_twice "arrivals=$runner_arrivals" test "$runner_arrival_count" -eq 2
    check ten_decisions_committed "decisions=$decisions" test "$decisions" -eq 10
    summary="verifications=$(jq 'length' <<<"$verifications") facts=$(jq -c 'map(.verdict)' <<<"$facts") latest=$latest_digest runner_verifications=$runner_arrival_count decisions=$decisions completion=$outcome status=$run_status"
    ;;
  scope-denied)
    refusal_seq=$(jq '[.[] | select(.type == "AGENT_ACTION_FAILED" and .payload.error.code == "SCOPE_DENIED") | .seq] | first // 0' <<<"$events")
    following=$(jq -c --argjson s "$refusal_seq" '[.[] | select(.seq > $s and (.type == "AGENT_TURN_READY" or .type == "AGENT_DECISION_COMMITTED"))][0:2] | map(.type)' <<<"$events")
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check refused_before_any_attempt "refusals=$refusals" \
      jq -e 'length == 1 and .[0].turnNo == 1 and .[0].code == "SCOPE_DENIED" and .[0].failureSource == "AUTHORIZATION" and .[0].attempts == 0' <<<"$refusals"
    check next_turn_received_refusal "refusal_seq=$refusal_seq then=$following" \
      jq -e '. == ["AGENT_TURN_READY", "AGENT_DECISION_COMMITTED"]' <<<"$following"
    check refusal_cost_no_external_call "runner_arrivals=$runner_arrivals verifications=$(jq 'length' <<<"$verifications")" \
      jq -e --arg d "$latest_digest" '. == [$d]' <<<"$runner_arrivals"
    check patches_inside_scope "patches=$patches" \
      jq -e 'length == 2 and .[0].touchedPaths == ["go.mod"] and all(.[].touchedPaths[]; . == "go.mod" or startswith("internal/auth/"))' <<<"$patches"
    check ready_for_approval "completion=$completion" ready_for_approval
    check one_change_request_for_latest "change_requests=$change_requests latest=$latest_digest" one_change_request_for_latest
    check report_respects_scope "report=$report" \
      jq -e --arg d "$latest_digest" '.status == "SCOPE_RESPECTED" and .patchDigest == $d and .passed == true' <<<"$report"
    summary="refusal=SCOPE_DENIED attempts=0 runner_verifications=$runner_arrival_count patches=$(jq 'length' <<<"$patches") decisions=$decisions completion=$outcome status=$run_status"
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
    check ready_for_approval "completion=$completion" ready_for_approval
    check one_change_request_for_latest "change_requests=$change_requests latest=$latest_digest" one_change_request_for_latest
    check report_names_latest "result=$result" report_names_latest
    summary="refusal=STALE_PATCH verifications=$(jq 'length' <<<"$verifications") runner_verifications=$runner_arrival_count first=$first_digest latest=$latest_digest completion=$outcome status=$run_status"
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
      test "$decisions_before" -eq 6 -a "$decisions" -eq 10
    check callbacks_completed_twice "callback_completions=$callback_completions" test "$callback_completions" -eq 2
    check ready_for_approval "completion=$completion" ready_for_approval
    check one_change_request_for_latest "change_requests=$change_requests latest=$latest_digest" one_change_request_for_latest
    check report_names_latest "result=$result" report_names_latest
    summary="attempt=$held_attempt_before verification=$held_verification decisions=$decisions_before/$decisions runner_verifications=$arrivals_before/$runner_arrival_count callback_completions=$callback_completions completion=$outcome status=$run_status"
    ;;
  incomplete-final)
    missing=$(jq -c '[.missing[]?.code]' <<<"$completion")
    check_states=$(jq -c '[.checks[]? | {(.name): .passed}] | add // {}' <<<"$completion")
    check run_failed_task_incomplete "status=$run_status node_error=$agent_node_error" \
      test "$run_status" = FAILED -a "$agent_node_error" = TASK_INCOMPLETE
    check agent_declared_completion "termination=$termination last=$(jq -c '.turns | last | .decision.kind' <<<"$trace")" \
      test "$termination" = FINAL_RESPONSE -a "$(jq -r '.turns | last | .decision.kind' <<<"$trace")" = FINAL
    check judged_incomplete_missing_listed "completion=$outcome missing=$missing" \
      jq -e --arg o "$outcome" '$o == "INCOMPLETE" and . == ["PATCH_VERIFICATION_MISSING", "CHANGE_REQUEST_MISSING"]' <<<"$missing"
    check failed_checks_named "checks=$check_states" \
      jq -e '.workspace_patch_recorded == true and .agent_declared_completion == true
        and .latest_patch_verified == false and .change_request_binds_latest_patch == false' <<<"$check_states"
    check only_an_earlier_patch_verified "latest=$latest_digest facts=$facts arrivals=$runner_arrivals" \
      jq -e --arg d "$latest_digest" --argjson a "$runner_arrivals" 'length == 1 and .[0].subject != $d and ($a | length) == 1 and $a[0] != $d' <<<"$facts"
    check no_change_request "change_requests=$change_requests" test "$(jq 'length' <<<"$change_requests")" -eq 0
    check eight_decisions_committed "decisions=$decisions" test "$decisions" -eq 8
    summary="missing=$(jq -r 'join(",")' <<<"$missing") verifications=$(jq 'length' <<<"$verifications") runner_verifications=$runner_arrival_count decisions=$decisions completion=$outcome status=$run_status"
    ;;
  *) check_release_story ;;
  esac
fi

write_demo_state

printf '\n'
if ((failures > 0)); then
  printf 'vulnerability fix %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'vulnerability fix %s story: all checks passed\n' "$story"
