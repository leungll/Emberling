#!/usr/bin/env bash
# Vulnerability-fix stories: an Agent bound to a workspace of the example repository
# repairs a dependency advisory with the workspace Tools and verifies its patch through the
# sandbox runner's verification, which answers by callback. Every check reads the Backend's
# public API and the sandbox runner's request record; no live credential is involved.
#
# Usage: scripts/demo/vuln-fix.sh verify|scope-denied|stale-evidence|restart|release|release-restart
#        scripts/demo/vuln-fix.sh definition verify|scope-denied|stale-evidence|restart|release|release-restart
#
# "definition" prints the Definition the story would save, as JSON, and touches nothing:
# it needs jq only, and the contract tests read it to check what the environment changes.
#
#   verify          the scripted Agent reads the rules and the advisory, upgrades go.mod
#                   and the import path without adapting the code, verifies, adapts the
#                   calling code after that verification fails (the upgrade does not
#                   compile) and verifies again. Expects a COMPLETED Run, two verify_patch
#                   Attempts bound to the runner, one failed then one passed patch_verified
#                   fact whose subject is the latest workspace patch digest, a report
#                   naming that digest, two verifications at the runner and nine committed
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
#   release         the verify story continued to production: the Agent describes the
#                   verified patch in a change request and asks to release it to the frozen
#                   deploy target; a person approves through the approval API, the approval
#                   occupies the target and starts one release operation, the mock
#                   production service deploys it and the Runtime confirms the target.
#                   Expects a COMPLETED Run, one deploy_change Attempt SUCCEEDED under its
#                   operation key with a release Binding and no callback credential, the
#                   occupation RELEASED as DEPLOYED, one healthy release_confirmed fact about
#                   the latest patch, the approval APPROVED, production deployed once and a
#                   RELEASED report. Needs EMBERLING_OPERATOR_TOKEN (the approval routes'
#                   credential, at least 32 bytes) in the shell environment; the token is
#                   sent in a header and never written anywhere.
#   release-restart the release story with the release request held at production's
#                   barrier; the Backend is killed with SIGKILL while the request is held,
#                   so production abandons it and the restarted Backend never learns the
#                   answer. Expects the orphan Attempt to enter the query state (one
#                   RELEASE_OUTCOME_UNKNOWN), the unknown operation to be voided after the
#                   grace (one RELEASE_VOIDED naming the next key), the first operation
#                   abandoned, voided and never deployed at production, the second operation
#                   deployed exactly once, the occupation RELEASED as DEPLOYED under the
#                   second key, no Decision asked again and the Run COMPLETED.
#
# EMBERLING_DEMO_MODEL=<model id> binds the Agent to a real decision model the Backend
# registers through EMBERLING_MODELS instead of the scripted mock model. Only the verify
# story runs with a real model, and its checks are the ones a real model must still meet:
# the Run completes, the report names the latest patch, and every patch_verified fact is
# about a patch the workspace recorded. A real model needs more time than the scripted one,
# so the Agent's timeoutMs becomes EMBERLING_DEMO_MODEL_TIMEOUT_MS (default 1800000, thirty
# minutes) and the wait for the terminal Run grows to that budget plus a minute; the
# scripted stories keep the fixture's budget. No key is read or written here; the Backend's
# own environment supplies it (EMBERLING_MODELS, EMBERLING_MODEL_BASE_URL,
# EMBERLING_MODEL_API_KEY and, for one model call, the optional EMBERLING_MODEL_TIMEOUT). The scripted stories pass no mock mode: the failed verification
# is the real verdict on an upgrade that does not compile, which the mock runner judges
# from the patch the same way the docker runner's build does, so every story holds against
# either runner.
#
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools jq

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
release | release-restart) script=vuln-fix-release ;;
*) die "usage: vuln-fix.sh [definition] verify|scope-denied|stale-evidence|restart|release|release-restart" ;;
esac
real_model=${EMBERLING_DEMO_MODEL:-}
if [[ -n "$real_model" && "$story" != verify ]]; then
  die "EMBERLING_DEMO_MODEL runs the verify story only; the other stories are scripted"
fi
release_story=0
case "$story" in release | release-restart) release_story=1 ;; esac
# The release stories decide the approval through the approval API, whose credential the
# Backend reads from its own environment. It is read here from the shell, sent in a header
# and never printed or stored.
OPERATOR_TOKEN=${EMBERLING_OPERATOR_TOKEN:-}
OPERATOR_HEADER="X-Emberling-Operator-Token: $OPERATOR_TOKEN"
# The release target the release stories bind the Agent to, and how long each release wait
# is allowed: a short void grace for the restart story, so the unknown operation is voided
# while the audience watches; a long confirm deadline, so no human wait opens by itself.
RELEASE_SERVICE=token-service
RELEASE_ENVIRONMENT=staging
RELEASE_APPROVAL_TIMEOUT_MS=600000
RELEASE_CONFIRM_TIMEOUT_MS=120000
RELEASE_HUMAN_WAIT_TIMEOUT_MS=600000
RELEASE_MAX_RETRIES=1
case "$story" in
release-restart) RELEASE_VOID_GRACE_MS=10000 ;;
*) RELEASE_VOID_GRACE_MS=60000 ;;
esac
RELEASE_INSTRUCTIONS='Once the latest patch has passed verification, describe it with create_change_request, citing the verification log artifact of the passing verification as its decision basis, then release it with deploy_change naming that change request; both calls return REJECTED tool messages for a definite refusal. A release needs a person to approve it; wait for the result and finish with {"status","advisory","patchDigest","changeRequestId","operationKey","version","healthy"}.'

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
# to the scripted trajectory, or to the real model with the real-model budget.
build_definition() {
  if [[ -n "$real_model" ]]; then
    jq --arg model "$real_model" --argjson timeout "$agent_timeout_ms" '
      del(.workflowId)
      | .name = "Vulnerability Fix (\($model))"
      | .nodes |= map(if .id == "node_agent"
          then .config.modelId = $model | del(.config.modelConfig) | .config.timeoutMs = $timeout
          else . end)' "$FIXTURE"
  elif ((release_story)); then
    jq --arg script "$script" --arg service "$RELEASE_SERVICE" --arg environment "$RELEASE_ENVIRONMENT" \
      --arg instructions "$RELEASE_INSTRUCTIONS" \
      --argjson approval "$RELEASE_APPROVAL_TIMEOUT_MS" --argjson confirm "$RELEASE_CONFIRM_TIMEOUT_MS" \
      --argjson grace "$RELEASE_VOID_GRACE_MS" --argjson retries "$RELEASE_MAX_RETRIES" --argjson human "$RELEASE_HUMAN_WAIT_TIMEOUT_MS" '
      del(.workflowId)
      | .name = "Vulnerability Fix (\($script))"
      | .description = "An Agent repairs a dependency vulnerability inside a bound workspace, verifies the patch, describes it in a change request and releases it to a frozen deploy target once a person approves"
      | .nodes |= map(if .id == "node_agent" then
          .config.modelConfig.script = $script
          | .config.instructions += " " + $instructions
          | .config.allowedTools += ["create_change_request", "deploy_change"]
          | .config.workspace.deployTarget = {service: $service, environment: $environment}
          | .config.approvalTimeoutMs = $approval
          | .config.releaseConfirmTimeoutMs = $confirm
          | .config.voidGraceMs = $grace
          | .config.maxReleaseRetries = $retries
          | .config.humanWaitTimeoutMs = $human
        else . end)' "$FIXTURE"
  else
    jq --arg script "$script" '
      del(.workflowId)
      | .name = "Vulnerability Fix (\($script))"
      | .nodes |= map(if .id == "node_agent" then .config.modelConfig.script = $script else . end)' "$FIXTURE"
  fi
}
if [[ "$mode" == definition ]]; then
  build_definition
  exit 0
fi
require_tools docker curl
if ((release_story)) && [[ ${#OPERATOR_TOKEN} -lt 32 ]]; then
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

paused=0
production_paused=0
release_barrier() {
  if ((paused)); then
    curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null 2>&1 || true
  fi
  if ((production_paused)); then
    curl -fsS -X POST "$PRODUCTION_URL/control/release" >/dev/null 2>&1 || true
  fi
}
trap release_barrier EXIT

log "checking that the Backend and the sandbox runner are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "sandbox runner /control/barrier" 60 runner_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null
if ((release_story)); then
  wait_until "mock production /control/barrier" 60 production_ready
  curl -fsS -X POST "$PRODUCTION_URL/control/release" >/dev/null
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
log "record baselines: mock provider seq=$record_after_seq runner seq=$runner_after${release_story:+ production seq=$production_after}"

if [[ "$story" == restart ]]; then
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["callback"]}' "$RUNNER_URL/control/pause" >/dev/null
  paused=1
fi

goal=PREPARE_ONLY
((release_story)) && goal=FIX_AND_DEPLOY
task=$(jq -cn --arg advisory "$ADVISORY" --arg service "$SERVICE" --arg goal "$goal" \
  '{advisory: $advisory, goal: $goal, service: $service}')
run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson task "$task" \
  '{workflowId: $wf, definitionVersion: $v, input: {task: $task}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"
studio_run_link "$run_id"

agent_deadline_s=$((agent_timeout_ms / 1000))
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

# The release stories: wait for the Agent to ask for the release, approve it as the
# operator, and for the restart story lose the Backend while production holds the request.
release_approval_id=""
release_action_id=""
release_decisions_before=0
if ((release_story)); then
  pending_approval() {
    local items
    items=$(curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals?status=PENDING") || return 1
    release_approval_id=$(jq -r --arg run "$run_id" '[.items[] | select(.runId == $run and .toolName == "deploy_change")] | first | .id // empty' <<<"$items")
    [[ -n "$release_approval_id" ]]
  }
  wait_until "the Agent to ask for the release and its approval to be PENDING" "$TERMINAL_DEADLINE_S" pending_approval
  release_action_id=$(curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals/$release_approval_id" | jq -r '.actionId')
  release_decisions_before=$(decisions_committed)
  log "approval $release_approval_id PENDING for the release Action $release_action_id; $release_decisions_before Decisions committed"
  demo_pause "the Agent asked to release the change and waits for a person (approval $release_approval_id, agent clock paused for APPROVAL); open the approval in Studio. Next: approve it as the operator"
  if [[ "$story" == release-restart ]]; then
    curl -fsS -X POST -H 'Content-Type: application/json' --data '{"kinds":["release"]}' "$PRODUCTION_URL/control/pause" >/dev/null
    production_paused=1
  fi
  decided=$(curl -fsS -X POST -H "$OPERATOR_HEADER" -H 'Content-Type: application/json' \
    --data '{"decision":"APPROVE","reason":"Verified patch; release to staging."}' "$BACKEND_URL/api/approvals/$release_approval_id/decision")
  log "approval decided: $(jq -c '{status: .approval.status, decidedBy: .approval.decidedBy, repeated}' <<<"$decided")"
  if [[ "$story" == release-restart ]]; then
    release_held() {
      curl -fsS "$PRODUCTION_URL/control/barrier" | jq -e '[.held[] | select(.kind == "release")] | length >= 1' >/dev/null
    }
    wait_until "the release request to be held by production" "$HELD_DEADLINE_S" release_held
    log "held at production: $(curl -fsS "$PRODUCTION_URL/control/barrier" | jq -c '[.held[] | {kind, operationId, arrivalSeq}]')"
    demo_pause "the approval occupied the deploy target and the release request is held at production while its Attempt is STARTED; next: SIGKILL the backend and start it again, so production abandons the request and the answer is never learned"
    kill_and_restart_backend
    # The held request's caller died with the old process, so production abandoned it and
    # nothing was deployed under the first key. The barrier opens for the re-release.
    curl -fsS -X POST "$PRODUCTION_URL/control/release" >/dev/null
    production_paused=0
    demo_pause "backend back; the Reconciler finds the orphan release Attempt and queries production instead of sending it again (RELEASE_OUTCOME_UNKNOWN). Production does not know the operation, so after the $((RELEASE_VOID_GRACE_MS / 1000))s void grace it is voided (RELEASE_VOIDED) and the release is sent again under the next operation key; watch the release Action in Studio"
  else
    demo_pause "approved: the approval occupied the deploy target and started release operation op_${release_action_id}_1; the agent clock is paused for RELEASE while production deploys and the Runtime polls the operation; watch the release Action in Studio"
  fi
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
# The release facts: the release Action's view, its Attempts, the release_confirmed facts
# and production's release lines since the baseline. Empty for the other stories.
releases='[]'
release_facts='[]'
production='[]'
if ((release_story)); then
  releases=$(jq -c '[.turns[] | select(.action.release != null) | .action as $a | {
      actionId: $a.id, actionStatus: $a.status, code: ($a.error.code // null),
      operations: [$a.release.operations[] | {operationKey, outcome, status}],
      occupation: ($a.release.occupation | if . == null then null else {status, releaseReason, operationKey} end),
      humanWait: ($a.release.humanWait | if . == null then null else {status, outcome} end),
      attempts: [.toolAttempts[] | select(.toolName == "deploy_change")
        | {attemptNo, status, code: (.error.code // null), provider: (.callbackBinding.providerId // null), operationKey: (.callbackBinding.externalTaskId // null)}]}]' <<<"$trace")
  release_facts=$(jq -c '[.facts.items[] | select(.factType == "release_confirmed") | {subject, verdict, toolAttemptId}]' <<<"$trace")
  production=$(record_after "$production_after" "$PRODUCTION_URL" | jq -s '[.[] | select(.kind == "release")]')
fi

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
    check nine_decisions_committed "decisions=$decisions" test "$decisions" -eq 9
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
  release | release-restart)
    release_action=$(jq -c --arg id "$release_action_id" '[.[] | select(.actionId == $id)] | first // {}' <<<"$releases")
    first_key="op_${release_action_id}_1"
    second_key="op_${release_action_id}_2"
    production_of() { jq --arg op "$1" --arg event "$2" '[.[] | select(.operationId == $op and .event == $event)] | length' <<<"$production"; }
    approval_status=$(curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/approvals/$release_approval_id" | jq -r '.status')
    unknown_events=$(jq '[.[] | select(.type == "RELEASE_OUTCOME_UNKNOWN")] | length' <<<"$events")
    voided_events=$(jq -c '[.[] | select(.type == "RELEASE_VOIDED") | .payload | {operationKey, nextOperationKey}]' <<<"$events")
    check run_completed "status=$run_status termination=$termination" test "$run_status" = COMPLETED
    check approval_approved "approval=$release_approval_id status=$approval_status" test "$approval_status" = APPROVED
    check last_fact_is_latest_passed "latest=$latest_digest facts=$facts" last_fact_is_latest_passed
    check release_fact_healthy_about_latest "latest=$latest_digest release_facts=$release_facts" \
      jq -e --arg d "$latest_digest" 'length == 1 and .[0].subject == $d and .[0].verdict == true' <<<"$release_facts"
    check occupation_released_as_deployed "occupation=$(jq -c '.occupation' <<<"$release_action")" \
      jq -e '.occupation.status == "RELEASED" and .occupation.releaseReason == "DEPLOYED"' <<<"$release_action"
    check no_human_wait_opened "humanWait=$(jq -c '.humanWait' <<<"$release_action")" jq -e '.humanWait == null' <<<"$release_action"
    check report_released "result=$result" \
      jq -e --arg d "$latest_digest" '.status == "RELEASED" and .patchDigest == $d and .healthy == true and (.operationKey | startswith("op_"))' <<<"$result"
    if [[ "$story" == release ]]; then
      check one_operation_deployed "attempts=$(jq -c '.attempts' <<<"$release_action") operations=$(jq -c '.operations' <<<"$release_action")" \
        jq -e --arg k "$first_key" '.actionStatus == "SUCCEEDED" and (.attempts | length == 1) and .attempts[0].status == "SUCCEEDED" and .attempts[0].provider == "release" and .attempts[0].operationKey == $k
          and (.operations | length == 1) and .operations[0].outcome == "DEPLOYED" and .occupation.operationKey == $k' <<<"$release_action"
      check production_deployed_once "arrived=$(production_of "$first_key" arrived) deployed=$(production_of "$first_key" deployed)" \
        test "$(production_of "$first_key" arrived)" -eq 1 -a "$(production_of "$first_key" deployed)" -eq 1
      check no_outcome_unknown "unknown_events=$unknown_events voided=$voided_events" test "$unknown_events" -eq 0 -a "$(jq 'length' <<<"$voided_events")" -eq 0
      check eleven_decisions_committed "decisions=$decisions" test "$decisions" -eq 11
      summary="approval=$approval_status operation=$first_key deployed=$(production_of "$first_key" deployed) release_facts=$(jq -c 'map(.verdict)' <<<"$release_facts") latest=$latest_digest decisions=$decisions status=$run_status"
    else
      check voided_then_released_again "attempts=$(jq -c '.attempts' <<<"$release_action") operations=$(jq -c '.operations' <<<"$release_action")" \
        jq -e --arg k1 "$first_key" --arg k2 "$second_key" '.actionStatus == "SUCCEEDED" and (.attempts | length == 2)
          and .attempts[0].status == "FAILED" and .attempts[0].code == "VOIDED" and .attempts[0].operationKey == $k1
          and .attempts[1].status == "SUCCEEDED" and .attempts[1].operationKey == $k2
          and (.operations | map(.outcome)) == ["VOIDED", "DEPLOYED"] and .occupation.operationKey == $k2' <<<"$release_action"
      check outcome_unknown_then_voided_events "unknown_events=$unknown_events voided=$voided_events" \
        test "$unknown_events" -eq 1 -a "$(jq -r --arg k1 "$first_key" --arg k2 "$second_key" 'length == 1 and .[0].operationKey == $k1 and .[0].nextOperationKey == $k2' <<<"$voided_events")" = true
      check first_operation_abandoned_never_deployed "arrived=$(production_of "$first_key" arrived) abandoned=$(production_of "$first_key" abandoned) voided=$(production_of "$first_key" voided) deployed=$(production_of "$first_key" deployed)" \
        test "$(production_of "$first_key" arrived)" -eq 1 -a "$(production_of "$first_key" abandoned)" -eq 1 -a "$(production_of "$first_key" voided)" -eq 1 -a "$(production_of "$first_key" deployed)" -eq 0
      check second_operation_deployed_once "arrived=$(production_of "$second_key" arrived) deployed=$(production_of "$second_key" deployed)" \
        test "$(production_of "$second_key" arrived)" -eq 1 -a "$(production_of "$second_key" deployed)" -eq 1
      check no_decision_asked_again "decisions before=$release_decisions_before after=$decisions" \
        test "$release_decisions_before" -eq 10 -a "$decisions" -eq 11
      summary="approval=$approval_status operations=$first_key:voided,$second_key:deployed unknown_events=$unknown_events decisions=$release_decisions_before/$decisions latest=$latest_digest status=$run_status"
    fi
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
      test "$decisions_before" -eq 6 -a "$decisions" -eq 9
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
