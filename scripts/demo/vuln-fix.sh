#!/usr/bin/env bash
# Vulnerability-fix stories: an Agent bound to a workspace of the example repository
# repairs a dependency advisory with the workspace Tools and verifies its patch through the
# sandbox runner's verification, which answers by callback. Every check reads the Backend's
# public API and the sandbox runner's request record; no live credential is involved.
#
# Usage: scripts/demo/vuln-fix.sh [definition] STORY
#   STORY is verify, scope-denied, stale-evidence or restart (the repair and its
#   verification), or one of the release stories: main, restart-during-approval,
#   duplicate-approval, reject-resubmit, release-crash-deployed, release-crash-voided,
#   void-exhausted, target-occupied, release-refused, unhealthy, prepare-only.
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
#
# The release stories continue the repair to production: the Agent describes the verified
# patch in a change request and asks to release it to the frozen deploy target with
# deploy_change; a person decides the release request through the approval API; the
# approval occupies the target and starts one release operation, which the mock production
# service deploys asynchronously while the Runtime polls it. Every release story but
# prepare-only decides an approval, so it needs EMBERLING_OPERATOR_TOKEN (the approval
# routes' credential, at least 32 bytes) in the shell environment: the token is sent in a
# header and never written anywhere. Every fault is one of the mock production service's
# own (the barrier hold, a dropped answer, refusal, an unhealthy target) or a SIGKILL of
# the Backend; each story clears its faults when it ends.
#
#   main            the repair, the failed and the adapted verification, the change
#                   request, the release request, the approval, the release and the
#                   target check. Expects a COMPLETED Run judged DEPLOYED, one deploy_change
#                   Attempt SUCCEEDED under its operation key with a release Binding, the
#                   occupation RELEASED as DEPLOYED, one healthy release_confirmed fact
#                   about the latest patch, the approval APPROVED, production deployed once,
#                   a RELEASED report and eleven committed Decisions.
#   restart-during-approval
#                   the Backend is killed with SIGKILL and started again while the release
#                   request waits for its approval. Expects the approval still PENDING and
#                   the request still WAITING_APPROVAL after the restart, the Agent clock
#                   still paused at the same instant with the same deadline, no Decision
#                   asked again, and after the approval the main story's release once, the
#                   clock resuming with exactly the time that remained when it paused.
#   duplicate-approval
#                   the same approval is submitted twice at once and once more after the
#                   Run ended. Expects one submission to decide it and the others to be
#                   answered as repeated, one APPROVAL_DECIDED, one Attempt and production
#                   deployed once.
#   reject-resubmit the first release request is rejected with a reason; the Agent receives
#                   it, requires an expiry on every token, verifies the new patch, records a
#                   second change request and asks again; the second request is approved.
#                   Expects two approvals bound to two Actions, the first FAILED as
#                   APPROVAL_REJECTED from HUMAN without an Attempt, the release Binding
#                   naming only the second Action, two change requests about different
#                   patches, the second about the latest, and the main story's release of
#                   the second request.
#   release-crash-deployed
#                   production accepts the release but drops its answer and holds the
#                   deployment; the Backend is killed and started again before it learns
#                   the outcome. Expects one RELEASE_OUTCOME_UNKNOWN, the restarted Backend
#                   to query instead of resending, the operation deployed once under its
#                   first key, no void and no Decision asked again.
#   release-crash-voided
#                   the release request is held at production's barrier and the Backend is
#                   killed while it is held, so production abandons it. Expects one
#                   RELEASE_OUTCOME_UNKNOWN, the unknown operation voided after a short
#                   grace (one RELEASE_VOIDED naming the next key), the same approved
#                   request released again under that key without a new approval and
#                   deployed once, the first key never deployed.
#   void-exhausted  production holds every release request until the Backend's request
#                   times out, so each outcome is unknown; the first operation is voided and
#                   released again, the second is voided too and no re-release is left.
#                   Expects the target RELEASED as NOT_DEPLOYED, the release Action FAILED as
#                   RELEASE_NOT_DEPLOYED from PROVIDER_REFUSAL, two voided operations, the
#                   Agent answering FINAL right after, and the task judged INCOMPLETE, never
#                   as an unknown release outcome.
#   target-occupied a first task's approved release holds the target while production's
#                   completion is paused; a second task asks to release to the same target.
#                   Expects the second request FAILED as TARGET_OCCUPIED from AUTHORIZATION
#                   with no approval and no Attempt, that task judged INCOMPLETE, and the
#                   first task's release deployed once after completion resumes.
#   release-refused production refuses the approved release (OUTSIDE_RELEASE_WINDOW).
#                   Expects the target RELEASED as REFUSED, the Action FAILED as
#                   RELEASE_REFUSED from PROVIDER_REFUSAL with production's code in the
#                   Attempt, the Agent answering FINAL (not a Tool error) and the task
#                   judged INCOMPLETE.
#   unhealthy       the release deploys but production leaves the target unhealthy.
#                   Expects a COMPLETED Run judged DEPLOYED_CHECK_FAILED, the target
#                   RELEASED as DEPLOYED, an unhealthy release_confirmed fact and a report
#                   that leaves the rollback to a person.
#   prepare-only    a PREPARE_ONLY task whose Agent may call deploy_change asks to release
#                   anyway. Expects the request FAILED as GOAL_DENIED from AUTHORIZATION
#                   before any approval or Attempt, nothing at production and the task
#                   judged READY_FOR_APPROVAL.
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
# is set, for ledger-vs-record.sh and the demo summary; target-occupied lists both Runs.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools jq

RELEASE_STORIES="main restart-during-approval duplicate-approval reject-resubmit release-crash-deployed release-crash-voided void-exhausted target-occupied release-refused unhealthy prepare-only"
STORIES="verify scope-denied stale-evidence restart $RELEASE_STORIES"
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
stale-evidence) script=vuln-fix-stale-evidence ;;
reject-resubmit) script=vuln-fix-rejected-resubmit ;;
main | restart-during-approval | duplicate-approval | release-crash-deployed | release-crash-voided | \
  void-exhausted | target-occupied | release-refused | unhealthy | prepare-only) script=vuln-fix-release ;;
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
# audience watches; a long confirm deadline, so no human wait opens by itself. A person
# deciding by hand gets the whole manual wait as the approval deadline.
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

# --- The release stories: wait for each release request, decide it, inject the fault.

# pending_approval_of RUN [EXCLUDED_ID] sets approval_id to a PENDING deploy_change approval
# of RUN other than EXCLUDED_ID, and fails while there is none.
approval_id=""
pending_approval_of() {
  local items
  items=$(curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals?status=PENDING") || return 1
  approval_id=$(jq -r --arg run "$1" --arg not "${2:-}" \
    '[.items[] | select(.runId == $run and .toolName == "deploy_change" and .id != $not)] | first | .id // empty' <<<"$items")
  [[ -n "$approval_id" ]]
}
approval_of() {
  curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals/$1"
}
# decide ID DECISION [REASON] posts an operator decision and prints the response.
decide() {
  local body
  body=$(jq -cn --arg d "$2" --arg r "${3:-}" '{decision: $d} + (if $r == "" then {} else {reason: $r} end)')
  curl -fsS -X POST -H "$OPERATOR_HEADER" -H 'Content-Type: application/json' \
    --data "$body" "$BACKEND_URL/api/approvals/$1/decision"
}
# await_request RUN [EXCLUDED_ID] waits for the Run's next release request and sets
# request_approval and request_action to its approval id and Action id.
request_approval=""
request_action=""
await_request() {
  wait_until "run $1 to ask for a release and its approval to be PENDING" "$TERMINAL_DEADLINE_S" pending_approval_of "$1" "${2:-}"
  request_approval=$approval_id
  request_action=$(approval_of "$approval_id" | jq -r '.actionId')
}
approve() {
  local decided
  decided=$(decide "$1" APPROVE "Verified patch; release to staging.")
  log "approval $1 decided: $(jq -c '{status: .approval.status, decidedBy: .approval.decidedBy, repeated}' <<<"$decided")"
}
release_held() {
  curl -fsS "$PRODUCTION_URL/control/barrier" | jq -e '[.held[] | select(.kind == "release")] | length >= 1' >/dev/null
}
production_line() {
  record_after "$production_after" "$PRODUCTION_URL" |
    jq -e -s --arg event "$1" --arg op "$2" 'any(.[]; .kind == "release" and .event == $event and .operationId == $op)' >/dev/null
}

release_approval_id=""
release_action_id=""
second_approval_id=""
second_action_id=""
release_decisions_before=0
clock_before='{}'
clock_after='{}'
approval_after_restart=""
action_after_restart=""
decisions_after_restart=0
duplicate_answers='[]'
manual_decisions='[]'
contender_snapshot=""
if ((decides_approval)); then
  request_run=$run_id
  [[ "$story" == target-occupied ]] && request_run=$holder_run_id
  case "$story" in
  release-crash-deployed)
    production_faults '{"dropResponse":true}'
    production_completion true
    ;;
  release-crash-voided | void-exhausted)
    curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["release"]}' "$PRODUCTION_URL/control/pause" >/dev/null
    ;;
  release-refused) production_faults "{\"reject\":true,\"rejectCode\":\"$REFUSAL_CODE\"}" ;;
  unhealthy) production_faults '{"unhealthy":true}' ;;
  esac
  await_request "$request_run"
  release_approval_id=$request_approval
  release_action_id=$request_action
  release_decisions_before=$(run_events "$request_run" | jq '[.[] | select(.type == "AGENT_DECISION_COMMITTED")] | length')
  log "approval $release_approval_id PENDING for the release Action $release_action_id; $release_decisions_before Decisions committed"
  first_key="op_${release_action_id}_1"
  second_key="op_${release_action_id}_2"

  if [[ "$approval_mode" == manual ]]; then
    # A person decides every release request in Studio until the Run ends.
    manual_pending=$release_approval_id
    while [[ -n "$manual_pending" ]]; do
      printf '\n==> Decide release request %s in Studio: %s/approvals/%s\n' "$manual_pending" "$STUDIO_URL" "$manual_pending" >&2
      studio_open "$STUDIO_URL/approvals/$manual_pending"
      decided_by_person() {
        [[ "$(approval_of "$manual_pending" | jq -r '.status')" != PENDING ]]
      }
      wait_until "a person to approve or reject $manual_pending in Studio" "$APPROVAL_WAIT_S" decided_by_person
      manual_decisions=$(jq -c --argjson d "$(approval_of "$manual_pending" | jq -c '{id, status, decidedBy}')" '. + [$d]' <<<"$manual_decisions")
      log "approval $manual_pending decided by a person: $(jq -c 'last' <<<"$manual_decisions")"
      next_request_or_end() {
        run_terminal_of "$run_id" && return 0
        pending_approval_of "$run_id" "$manual_pending"
      }
      approval_id=""
      wait_until "the next release request or the end of run $run_id" "$TERMINAL_DEADLINE_S" next_request_or_end
      manual_pending=$approval_id
    done
  fi

  case "$story" in
  main)
    if [[ "$approval_mode" == scripted ]]; then
      demo_pause "the Agent asked to release the change and waits for a person (approval $release_approval_id, agent clock paused for APPROVAL); open the approval in Studio. Next: approve it as the operator"
      approve "$release_approval_id"
      demo_pause "approved: the approval occupied the deploy target and started release operation $first_key; the agent clock is paused for RELEASE while production deploys and the Runtime polls the operation"
    fi
    ;;
  restart-during-approval)
    clock_before=$(agent_trace | jq -c '.agentRun | {deadline, pausedAt, pauseReason}')
    demo_pause "the release request waits for approval $release_approval_id with the agent clock paused ($clock_before); next: SIGKILL the backend and start it again"
    kill_and_restart_backend
    clock_after=$(agent_trace | jq -c '.agentRun | {deadline, pausedAt, pauseReason}')
    approval_after_restart=$(approval_of "$release_approval_id" | jq -r '.status')
    action_after_restart=$(agent_trace | jq -r --arg id "$release_action_id" '[.turns[].action | select(. != null and .id == $id)] | first | .status')
    decisions_after_restart=$(decisions_committed)
    log "after the restart: approval $approval_after_restart, action $action_after_restart, clock $clock_after, $decisions_after_restart Decisions"
    demo_pause "backend back; the approval is still $approval_after_restart and the agent clock still paused. Next: approve it"
    approve "$release_approval_id"
    ;;
  duplicate-approval)
    demo_pause "next: submit the same approval twice at once"
    answers_dir=$(mktemp -d "${TMPDIR:-/tmp}/emberling-approvals.XXXXXX")
    for n in 1 2; do
      decide "$release_approval_id" APPROVE "Verified patch; release to staging." >"$answers_dir/$n.json" &
    done
    wait
    duplicate_answers=$(jq -s -c 'map({status: .approval.status, repeated})' "$answers_dir"/1.json "$answers_dir"/2.json)
    rm -rf "$answers_dir"
    log "two concurrent approvals answered: $duplicate_answers"
    ;;
  reject-resubmit)
    demo_pause "the Agent asked to release; next: reject it with a reason"
    decided=$(decide "$release_approval_id" REJECT "$REJECT_REASON")
    log "approval $release_approval_id decided: $(jq -c '{status: .approval.status, reason: .approval.reason}' <<<"$decided")"
    await_request "$run_id" "$release_approval_id"
    second_approval_id=$request_approval
    second_action_id=$request_action
    log "the Agent asked again: approval $second_approval_id for Action $second_action_id"
    demo_pause "the Agent changed the code, verified the new patch, recorded a new change request and asks again (approval $second_approval_id); next: approve it"
    approve "$second_approval_id"
    ;;
  release-crash-deployed)
    approve "$release_approval_id"
    wait_until "production to drop its answer to $first_key" "$HELD_DEADLINE_S" production_line responseDropped "$first_key"
    demo_pause "production accepted $first_key, dropped its answer and holds the deployment; next: SIGKILL the backend and start it again before it learns the outcome"
    kill_and_restart_backend
    production_faults '{}'
    production_completion false
    demo_pause "backend back; it queries $first_key instead of sending it again, and production now finishes the deployment"
    ;;
  release-crash-voided)
    approve "$release_approval_id"
    wait_until "the release request to be held by production" "$HELD_DEADLINE_S" release_held
    demo_pause "the approval occupied the deploy target and the release request is held at production while its Attempt is STARTED; next: SIGKILL the backend and start it again, so production abandons the request and the answer is never learned"
    kill_and_restart_backend
    # The held request's caller died with the old process, so production abandoned it and
    # nothing was deployed under the first key. The barrier opens for the re-release.
    curl -fsS -X POST "$PRODUCTION_URL/control/release" >/dev/null
    demo_pause "backend back; the Reconciler finds the orphan release Attempt and queries production instead of sending it again (RELEASE_OUTCOME_UNKNOWN). Production does not know the operation, so after the $((RELEASE_VOID_GRACE_MS / 1000))s void grace it is voided (RELEASE_VOIDED) and the release is sent again under the next operation key"
    ;;
  void-exhausted)
    approve "$release_approval_id"
    demo_pause "production holds every release request until the Backend's request times out, so each outcome is unknown: $first_key is voided after the $((RELEASE_VOID_GRACE_MS / 1000))s grace and released again as $second_key, which is held and voided too, with no re-release left"
    ;;
  target-occupied)
    approve "$release_approval_id"
    wait_until "production to receive the holder's release $first_key" "$HELD_DEADLINE_S" production_line arrived "$first_key"
    demo_pause "run $holder_run_id holds $RELEASE_SERVICE/$RELEASE_ENVIRONMENT while production deploys $first_key; next: a second task asks to release to the same target"
    run_id=$(create_run)
    wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal_of "$run_id"
    contender_snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
    demo_pause "the second task's release request was refused as TARGET_OCCUPIED before any approval; next: production finishes the holder's deployment"
    production_completion false
    wait_until "run $holder_run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal_of "$holder_run_id"
    ;;
  release-refused | unhealthy)
    approve "$release_approval_id"
    ;;
  esac
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
      occupation: ($a.release.occupation | if . == null then null else {status, releaseReason, operationKey} end),
      humanWait: ($a.release.humanWait | if . == null then null else {status, outcome} end),
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

# check_deployed_once ACTION: the task COMPLETED judged DEPLOYED, ACTION's release
# SUCCEEDED once under its first key with the target RELEASED as DEPLOYED, one healthy
# release_confirmed fact about the latest patch, production deployed that key once and the
# Agent reported the release.
check_deployed_once() {
  local action key release
  action=$1
  key="op_${action}_1"
  release=$(release_of "$action")
  check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
  check deployed "completion=$completion" \
    jq -e '.outcome == "DEPLOYED" and (.missing | length) == 0 and all(.checks[]; .passed)' <<<"$completion"
  check one_operation_deployed "release=$release" \
    jq -e --arg k "$key" '.actionStatus == "SUCCEEDED" and (.attempts | length == 1) and .attempts[0].status == "SUCCEEDED"
      and .attempts[0].provider == "release" and .attempts[0].operationKey == $k
      and (.operations | length == 1) and .operations[0].outcome == "DEPLOYED"
      and .occupation.status == "RELEASED" and .occupation.releaseReason == "DEPLOYED" and .occupation.operationKey == $k' <<<"$release"
  check release_fact_healthy_about_latest "latest=$latest_digest release_facts=$release_facts" \
    jq -e --arg d "$latest_digest" 'length == 1 and .[0].subject == $d and .[0].verdict == true' <<<"$release_facts"
  check production_deployed_once "arrived=$(production_of "$key" arrived) deployed=$(production_of "$key" deployed) under_action=$(deployed_under "$action")" \
    test "$(production_of "$key" arrived)" -eq 1 -a "$(production_of "$key" deployed)" -eq 1 -a "$(deployed_under "$action")" -eq 1
  check report_released "report=$report" \
    jq -e --arg d "$latest_digest" --arg k "$key" '.status == "RELEASED" and .patchDigest == $d and .healthy == true and .operationKey == $k' <<<"$report"
}

# check_returned ACTION CODE REASON: the release ended definitely without a deployment and
# went back to the Agent, which answered FINAL right after; the task, never deployed, is
# INCOMPLETE rather than of unknown outcome or ended by a Tool error.
check_returned() {
  local action=$1 code=$2 reason=$3 release
  release=$(release_of "$action")
  check run_failed_task_incomplete "status=$run_status node_error=$agent_node_error" \
    test "$run_status" = FAILED -a "$agent_node_error" = TASK_INCOMPLETE
  check agent_answered_not_tool_error "termination=$termination completion=$outcome" \
    test "$termination" = FINAL_RESPONSE -a "$outcome" = INCOMPLETE
  check release_returned_to_agent "release=$(jq -c '{actionStatus, code, failureSource}' <<<"$release")" \
    jq -e --arg c "$code" '.actionStatus == "FAILED" and .code == $c and .failureSource == "PROVIDER_REFUSAL"' <<<"$release"
  check target_released "occupation=$(jq -c '.occupation' <<<"$release")" \
    jq -e --arg r "$reason" '.occupation.status == "RELEASED" and .occupation.releaseReason == $r' <<<"$release"
  check final_right_after "release_turn=$(jq '.turnNo' <<<"$release") last=$last_turn" \
    jq -e --argjson t "$(jq '.turnNo' <<<"$release")" '.kind == "FINAL" and .turnNo == $t + 1' <<<"$last_turn"
  check nothing_deployed "deployed_under_action=$(deployed_under "$action")" test "$(deployed_under "$action")" -eq 0
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
  main)
    approval_status=$(approval_of "$release_approval_id" | jq -r '.status')
    check_deployed_once "$release_action_id"
    check approval_approved "approval=$release_approval_id status=$approval_status" test "$approval_status" = APPROVED
    check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
    check no_human_wait_opened "humanWait=$(release_of "$release_action_id" | jq -c '.humanWait')" \
      jq -e '.humanWait == null' <<<"$(release_of "$release_action_id")"
    check no_outcome_unknown "events=$(jq -c '[.[] | select(.type == "RELEASE_OUTCOME_UNKNOWN" or .type == "RELEASE_VOIDED") | .type]' <<<"$events")" \
      jq -e '[.[] | select(.type == "RELEASE_OUTCOME_UNKNOWN" or .type == "RELEASE_VOIDED")] | length == 0' <<<"$events"
    check eleven_decisions_committed "decisions=$decisions" test "$decisions" -eq 11
    summary="approval=$approval_status operation=$first_key deployed=$(production_of "$first_key" deployed) release_facts=$(jq -c 'map(.verdict)' <<<"$release_facts") latest=$latest_digest decisions=$decisions completion=$outcome status=$run_status"
    ;;
  restart-during-approval)
    resumed=$(jq -c '[.[] | select(.type == "AGENT_RESUMED" and .payload.reason == "APPROVAL") | .payload.remainingMs]' <<<"$events")
    expected_remaining_ms=$(jq -n --argjson c "$clock_before" '
      def us: capture("^(?<s>[^.Z]+)(\\.(?<f>[0-9]+))?Z$")
        | ((.s + "Z" | fromdateiso8601) * 1000000) + ((((.f // "") + "000000")[0:6]) | tonumber);
      (($c.deadline | us) - ($c.pausedAt | us)) / 1000 | floor')
    check_deployed_once "$release_action_id"
    check still_waiting_after_restart "approval=$approval_after_restart action=$action_after_restart" \
      test "$approval_after_restart" = PENDING -a "$action_after_restart" = WAITING_APPROVAL
    check agent_clock_unchanged_by_restart "before=$clock_before after=$clock_after" \
      jq -e --argjson b "$clock_before" '. == $b and .pausedAt != null and .pauseReason == "APPROVAL"' <<<"$clock_after"
    check no_decision_asked_again "decisions before=$release_decisions_before after_restart=$decisions_after_restart end=$decisions" \
      test "$release_decisions_before" -eq 10 -a "$decisions_after_restart" -eq 10 -a "$decisions" -eq 11
    check wait_consumed_no_agent_time "remainingMs=$resumed expected=$expected_remaining_ms" \
      jq -e --argjson want "$expected_remaining_ms" 'length == 1 and .[0] == $want' <<<"$resumed"
    summary="approval=$approval_after_restart->$(approval_of "$release_approval_id" | jq -r '.status') clock=$(jq -c '{pausedAt, pauseReason}' <<<"$clock_after") remainingMs=$resumed decisions=$release_decisions_before/$decisions completion=$outcome status=$run_status"
    ;;
  duplicate-approval)
    decided_events=$(jq '[.[] | select(.type == "APPROVAL_DECIDED")] | length' <<<"$events")
    check_deployed_once "$release_action_id"
    check one_decided_one_repeated "answers=$duplicate_answers" \
      jq -e 'length == 2 and all(.[]; .status == "APPROVED") and ([.[] | select(.repeated == true)] | length) == 1' <<<"$duplicate_answers"
    check one_approval_decided_event "APPROVAL_DECIDED=$decided_events" test "$decided_events" -eq 1
    check late_approval_repeated_no_write "answer=$late_answer events=$events_before_late/$events_after_late" \
      test "$(jq -r '.repeated' <<<"$late_answer")" = true -a "$events_before_late" -eq "$events_after_late"
    summary="answers=$duplicate_answers late=$late_answer approval_decided=$decided_events deployed=$(deployed_under "$release_action_id") completion=$outcome status=$run_status"
    ;;
  reject-resubmit)
    first_release=$(release_of "$release_action_id")
    second_release=$(release_of "$second_action_id")
    check_deployed_once "$second_action_id"
    check two_requests_two_approvals "releases=$(jq -c 'map({actionId, approval: .approval.id})' <<<"$releases")" \
      jq -e --arg a1 "$release_approval_id" --arg a2 "$second_approval_id" --arg x1 "$release_action_id" --arg x2 "$second_action_id" \
      'length == 2 and .[0].actionId == $x1 and .[0].approval.id == $a1 and .[1].actionId == $x2 and .[1].approval.id == $a2 and $x1 != $x2' <<<"$releases"
    check first_rejected_back_to_agent "first=$(jq -c '{actionStatus, code, failureSource, approval, attempts: (.attempts | length)}' <<<"$first_release")" \
      jq -e --arg r "$REJECT_REASON" '.actionStatus == "FAILED" and .code == "APPROVAL_REJECTED" and .failureSource == "HUMAN"
        and .approval.status == "REJECTED" and .approval.reason == $r and (.attempts | length) == 0' <<<"$first_release"
    check old_approval_not_in_new_release "binding=$(jq -c '.attempts[0].operationKey' <<<"$second_release")" \
      jq -e --arg a1 "$release_approval_id" --arg x1 "$release_action_id" --arg x2 "$second_action_id" \
      '.attempts[0].operationKey == "op_\($x2)_1" and (.attempts[0].operationKey | contains($a1) or contains($x1) | not)' <<<"$second_release"
    check two_change_requests_latest_last "change_requests=$change_requests latest=$latest_digest" \
      jq -e --arg d "$latest_digest" 'length == 2 and .[0].patchDigest != .[1].patchDigest and .[1].patchDigest == $d' <<<"$change_requests"
    check three_verifications "verifications=$(jq 'length' <<<"$verifications")" test "$(jq 'length' <<<"$verifications")" -eq 3
    check report_names_both_requests "report=$report" \
      jq -e --argjson cr "$change_requests" '.rejectedChangeRequestId == $cr[0].id and .changeRequestId == $cr[1].id' <<<"$report"
    summary="approvals=$release_approval_id:REJECTED,$second_approval_id:APPROVED change_requests=$(jq 'length' <<<"$change_requests") verifications=$(jq 'length' <<<"$verifications") deployed=$(deployed_under "$second_action_id") decisions=$decisions completion=$outcome status=$run_status"
    ;;
  release-crash-deployed)
    unknown_events=$(jq -c '[.[] | select(.type == "RELEASE_OUTCOME_UNKNOWN") | .payload.operationKey]' <<<"$events")
    voided_events=$(jq '[.[] | select(.type == "RELEASE_VOIDED")] | length' <<<"$events")
    check_deployed_once "$release_action_id"
    check answer_dropped_once "responseDropped=$(production_of "$first_key" responseDropped)" test "$(production_of "$first_key" responseDropped)" -eq 1
    check outcome_unknown_then_queried "unknown=$unknown_events voided=$voided_events" \
      jq -e --arg k "$first_key" --argjson v "$voided_events" '. == [$k] and $v == 0' <<<"$unknown_events"
    check no_decision_asked_again "decisions before=$release_decisions_before after=$decisions" \
      test "$release_decisions_before" -eq 10 -a "$decisions" -eq 11
    summary="operation=$first_key answer_dropped=$(production_of "$first_key" responseDropped) unknown=$unknown_events deployed=$(production_of "$first_key" deployed) decisions=$release_decisions_before/$decisions completion=$outcome status=$run_status"
    ;;
  release-crash-voided)
    release_action=$(release_of "$release_action_id")
    unknown_events=$(jq '[.[] | select(.type == "RELEASE_OUTCOME_UNKNOWN")] | length' <<<"$events")
    voided_events=$(jq -c '[.[] | select(.type == "RELEASE_VOIDED") | .payload | {operationKey, nextOperationKey}]' <<<"$events")
    approval_status=$(approval_of "$release_approval_id" | jq -r '.status')
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check deployed "completion=$completion" \
      jq -e '.outcome == "DEPLOYED" and (.missing | length) == 0 and all(.checks[]; .passed)' <<<"$completion"
    check approval_approved_once "approval=$release_approval_id status=$approval_status" test "$approval_status" = APPROVED
    check voided_then_released_again "attempts=$(jq -c '.attempts' <<<"$release_action") operations=$(jq -c '.operations' <<<"$release_action")" \
      jq -e --arg k1 "$first_key" --arg k2 "$second_key" '.actionStatus == "SUCCEEDED" and (.attempts | length == 2)
        and .attempts[0].status == "FAILED" and .attempts[0].code == "VOIDED" and .attempts[0].operationKey == $k1
        and .attempts[1].status == "SUCCEEDED" and .attempts[1].operationKey == $k2
        and (.operations | map(.outcome)) == ["VOIDED", "DEPLOYED"]
        and .occupation.status == "RELEASED" and .occupation.releaseReason == "DEPLOYED" and .occupation.operationKey == $k2' <<<"$release_action"
    check outcome_unknown_then_voided_events "unknown_events=$unknown_events voided=$voided_events" \
      test "$unknown_events" -eq 1 -a "$(jq -r --arg k1 "$first_key" --arg k2 "$second_key" 'length == 1 and .[0].operationKey == $k1 and .[0].nextOperationKey == $k2' <<<"$voided_events")" = true
    check first_operation_abandoned_never_deployed "arrived=$(production_of "$first_key" arrived) abandoned=$(production_of "$first_key" abandoned) voided=$(production_of "$first_key" voided) deployed=$(production_of "$first_key" deployed)" \
      test "$(production_of "$first_key" arrived)" -eq 1 -a "$(production_of "$first_key" abandoned)" -eq 1 -a "$(production_of "$first_key" voided)" -eq 1 -a "$(production_of "$first_key" deployed)" -eq 0
    check second_operation_deployed_once "arrived=$(production_of "$second_key" arrived) deployed=$(production_of "$second_key" deployed)" \
      test "$(production_of "$second_key" arrived)" -eq 1 -a "$(production_of "$second_key" deployed)" -eq 1
    check release_fact_healthy_about_latest "latest=$latest_digest release_facts=$release_facts" \
      jq -e --arg d "$latest_digest" 'length == 1 and .[0].subject == $d and .[0].verdict == true' <<<"$release_facts"
    check no_decision_asked_again "decisions before=$release_decisions_before after=$decisions" \
      test "$release_decisions_before" -eq 10 -a "$decisions" -eq 11
    summary="approval=$approval_status operations=$first_key:voided,$second_key:deployed unknown_events=$unknown_events decisions=$release_decisions_before/$decisions latest=$latest_digest completion=$outcome status=$run_status"
    ;;
  void-exhausted)
    release_action=$(release_of "$release_action_id")
    voided_events=$(jq -c '[.[] | select(.type == "RELEASE_VOIDED") | .payload | {operationKey, nextOperationKey}]' <<<"$events")
    check_returned "$release_action_id" RELEASE_NOT_DEPLOYED NOT_DEPLOYED
    check both_operations_voided "attempts=$(jq -c '.attempts' <<<"$release_action") operations=$(jq -c '.operations' <<<"$release_action")" \
      jq -e --arg k1 "$first_key" --arg k2 "$second_key" '(.attempts | length == 2) and all(.attempts[]; .status == "FAILED" and .code == "VOIDED")
        and (.operations | map(.operationKey)) == [$k1, $k2] and (.operations | map(.outcome)) == ["VOIDED", "VOIDED"]' <<<"$release_action"
    check voided_retried_then_exhausted "voided=$voided_events" \
      jq -e --arg k1 "$first_key" --arg k2 "$second_key" '. == [{operationKey: $k1, nextOperationKey: $k2}, {operationKey: $k2, nextOperationKey: null}]' <<<"$voided_events"
    check production_never_deployed "op1=$(production_of "$first_key" abandoned)/$(production_of "$first_key" voided) op2=$(production_of "$second_key" abandoned)/$(production_of "$second_key" voided)" \
      test "$(production_of "$first_key" voided)" -eq 1 -a "$(production_of "$second_key" voided)" -eq 1 -a "$(production_of "$first_key" deployed)" -eq 0 -a "$(production_of "$second_key" deployed)" -eq 0
    summary="operations=$first_key:voided,$second_key:voided code=$(jq -r '.code' <<<"$release_action") occupation=$(jq -r '.occupation.releaseReason' <<<"$release_action") completion=$outcome status=$run_status"
    ;;
  target-occupied)
    contender_release=$(jq -c 'first // {}' <<<"$releases")
    contender_approvals=$(for s in PENDING APPROVED REJECTED EXPIRED; do curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals?status=$s"; done |
      jq -s --arg run "$run_id" '[.[].items[] | select(.runId == $run)] | length')
    contender_status=$run_status
    # The holder's own facts come from its own Trace and snapshot.
    run_id_contender=$run_id
    run_id=$holder_run_id
    snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
    holder_status=$(jq -r '.run.status' <<<"$snapshot")
    holder_trace=$(agent_trace)
    holder_completion=$(jq -c '.agentRun.completion // {}' <<<"$holder_trace")
    holder_release=$(jq -c --arg id "$release_action_id" '[.turns[] | select(.action.id == $id) | .action | {status, occupation: .release.occupation.releaseReason}] | first // {}' <<<"$holder_trace")
    run_id=$run_id_contender
    check contender_refused_before_approval "release=$(jq -c '{actionStatus, code, failureSource, approval, attempts: (.attempts | length)}' <<<"$contender_release") approvals=$contender_approvals" \
      jq -e --argjson n "$contender_approvals" '.actionStatus == "FAILED" and .code == "TARGET_OCCUPIED" and .failureSource == "AUTHORIZATION"
        and .approval == null and (.attempts | length) == 0 and $n == 0' <<<"$contender_release"
    check contender_answered_after_refusal "release_turn=$(jq '.turnNo' <<<"$contender_release") last=$last_turn termination=$termination" \
      jq -e --argjson t "$(jq '.turnNo' <<<"$contender_release")" --arg term "$termination" '.kind == "FINAL" and .turnNo == $t + 1 and $term == "FINAL_RESPONSE"' <<<"$last_turn"
    check contender_task_incomplete "status=$contender_status completion=$outcome" test "$contender_status" = FAILED -a "$outcome" = INCOMPLETE
    check holder_deployed_once "status=$holder_status completion=$(jq -r '.outcome' <<<"$holder_completion") release=$holder_release deployed=$(deployed_under "$release_action_id")" \
      test "$holder_status" = COMPLETED -a "$(jq -r '.outcome' <<<"$holder_completion")" = DEPLOYED -a "$(deployed_under "$release_action_id")" -eq 1 \
      -a "$(jq -r '.status' <<<"$holder_release")" = SUCCEEDED -a "$(jq -r '.occupation' <<<"$holder_release")" = DEPLOYED
    check production_saw_only_holder "release_arrivals=$(jq '[.[] | select(.event == "arrived")] | length' <<<"$production")" \
      test "$(jq '[.[] | select(.event == "arrived")] | length' <<<"$production")" -eq 1
    summary="contender=$run_id:$contender_status refusal=$(jq -r '.code' <<<"$contender_release") approvals=$contender_approvals holder=$holder_run_id:$holder_status deployed=$(deployed_under "$release_action_id") completion=$outcome"
    run_status="$contender_status (holder $holder_status)"
    ;;
  release-refused)
    release_action=$(release_of "$release_action_id")
    check_returned "$release_action_id" RELEASE_REFUSED REFUSED
    check production_code_on_attempt "attempts=$(jq -c '.attempts' <<<"$release_action")" \
      jq -e --arg c "$REFUSAL_CODE" '(.attempts | length) == 1 and .attempts[0].status == "FAILED" and .attempts[0].code == $c' <<<"$release_action"
    check production_refused_once "arrived=$(production_of "$first_key" arrived)" test "$(production_of "$first_key" arrived)" -eq 1
    summary="operation=$first_key refused=$(jq -r '.attempts[0].code' <<<"$release_action") code=$(jq -r '.code' <<<"$release_action") occupation=$(jq -r '.occupation.releaseReason' <<<"$release_action") termination=$termination completion=$outcome status=$run_status"
    ;;
  unhealthy)
    release_action=$(release_of "$release_action_id")
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check deployed_check_failed "completion=$outcome" test "$outcome" = DEPLOYED_CHECK_FAILED
    check target_released_as_deployed "release=$(jq -c '{actionStatus, occupation}' <<<"$release_action")" \
      jq -e '.actionStatus == "SUCCEEDED" and .occupation.status == "RELEASED" and .occupation.releaseReason == "DEPLOYED"' <<<"$release_action"
    check release_fact_unhealthy_about_latest "latest=$latest_digest release_facts=$release_facts" \
      jq -e --arg d "$latest_digest" 'length == 1 and .[0].subject == $d and .[0].verdict == false' <<<"$release_facts"
    check report_check_failed_as_is "report=$report" \
      jq -e '.status == "DEPLOYED_CHECK_FAILED" and .healthy == false and (.nextStep | contains("rollback plan"))' <<<"$report"
    check production_deployed_once "deployed=$(deployed_under "$release_action_id") unhealthy=$(jq '[.[] | select(.event == "health" and .healthy == false)] | length' <<<"$production")" \
      test "$(deployed_under "$release_action_id")" -eq 1 -a "$(jq '[.[] | select(.event == "health" and .healthy == false)] | length' <<<"$production")" -ge 1
    summary="operation=$first_key deployed=$(deployed_under "$release_action_id") release_facts=$(jq -c 'map(.verdict)' <<<"$release_facts") report=$(jq -r '.status' <<<"$report") completion=$outcome status=$run_status"
    ;;
  prepare-only)
    goal_refusal=$(jq -c 'first // {}' <<<"$releases")
    prepared_approvals=$(for s in PENDING APPROVED REJECTED EXPIRED; do curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals?status=$s" 2>/dev/null || printf '{"items":[]}'; done |
      jq -s --arg run "$run_id" '[.[].items[]? | select(.runId == $run)] | length')
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check ready_for_approval "completion=$completion" ready_for_approval
    check release_denied_by_goal "release=$(jq -c '{actionStatus, code, failureSource, approval, attempts: (.attempts | length)}' <<<"$goal_refusal")" \
      jq -e '.actionStatus == "FAILED" and .code == "GOAL_DENIED" and .failureSource == "AUTHORIZATION" and .approval == null and (.attempts | length) == 0' <<<"$goal_refusal"
    check no_approval_created "approvals=$prepared_approvals" test "$prepared_approvals" -eq 0
    check nothing_at_production "release_lines=$(jq 'length' <<<"$production")" test "$(jq 'length' <<<"$production")" -eq 0
    check one_change_request_for_latest "change_requests=$change_requests latest=$latest_digest" one_change_request_for_latest
    check report_prepared_only "report=$report" \
      jq -e --arg d "$latest_digest" '.status == "PREPARED_ONLY" and .code == "GOAL_DENIED" and .patchDigest == $d' <<<"$report"
    summary="refusal=$(jq -r '.code' <<<"$goal_refusal") approvals=$prepared_approvals production_lines=$(jq 'length' <<<"$production") completion=$outcome status=$run_status"
    ;;
  esac
fi

state_run_ids=$run_id
[[ -n "$holder_run_id" ]] && state_run_ids="$run_id,$holder_run_id"
if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
    "$state_run_ids" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'vulnerability fix %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'vulnerability fix %s story: all checks passed\n' "$story"
