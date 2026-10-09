#!/usr/bin/env bash
# Software delivery stories: an Agent driven by the scripted mock model tests a patch in
# the sandbox runner, which answers by callback, and deploys it once through the mock
# production service. Every check reads the Backend's public API and the request records
# of the sandbox runner and the mock production service; no live credential is involved.
#
# Usage: scripts/demo/delivery.sh main|restart|deploy-lost|redelivery
#
#   main         tests the patch, then deploys it. Expects a COMPLETED Run whose result
#                names the deployment, one passing patch_tested fact, one test the runner
#                ran for the bound testId and one deployment production recorded for the
#                operationId derived from the deploy Action.
#   restart      the runner delivers its callback twice and the first delivery is held at
#                the runner's barrier. While the sandbox_test Attempt is DISPATCHED and the
#                Agent waits, the Backend is killed with SIGKILL and started again; then
#                the callback is released. Expects the original Attempt to complete once,
#                both deliveries answered 200 with one callback completion in the ledger,
#                no second test dispatch and one deployment.
#   deploy-lost  runs with a short Agent deadline and holds the deployment request at
#                production's barrier, then kills and restarts the Backend, so the request
#                is abandoned and its answer is lost. Expects the Reconciler to end the
#                Agent with TIMEOUT, the deploy Attempt and its Action FAILED, a FAILED Run,
#                no second deploy Attempt and at most one production call for the
#                operationId.
#   redelivery   runs the main story, then restarts the sandbox runner, which delivers its
#                stored result again. Expects a second delivery for the same testId
#                answered 200, no Event after the Run's terminal Event and the Run still
#                COMPLETED. The Backend answers such a delivery as a duplicate; the runner
#                records only the HTTP status, so the unchanged ledger is the evidence here.
#
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools docker curl jq

story=${1:-}
timeout_ms=0
case "$story" in
main | redelivery) script=software-delivery ;;
restart) script=software-delivery-duplicate-callback ;;
deploy-lost)
  script=software-delivery
  # Long enough for the test and the held deployment to happen before the kill, short
  # enough that the restarted Backend's Reconciler finds the deadline expired soon.
  timeout_ms=${EMBERLING_DEMO_DELIVERY_TIMEOUT_MS:-20000}
  ;;
*) die "usage: delivery.sh main|restart|deploy-lost|redelivery" ;;
esac

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/software_delivery.json"
HELD_DEADLINE_S=60
RESTART_DEADLINE_S=120
TERMINAL_DEADLINE_S=180
BASE_COMMIT=fixture-v1
SERVICE=checkout
ENVIRONMENT=staging
# A patch to the runner's fixture module; the mock runner backend only digests it.
PATCH=$'--- a/greet.go\n+++ b/greet.go\n@@ -3,5 +3,5 @@ package greet\n import "fmt"\n \n func Greeting(name string) string {\n-\treturn fmt.Sprintf("Hello, %s", name)\n+\treturn fmt.Sprintf("Hello, %s!", name)\n }\n'

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

paused_urls=()
release_barriers() {
  local url
  for url in "${paused_urls[@]+"${paused_urls[@]}"}"; do
    curl -fsS -X POST "$url/control/release" >/dev/null 2>&1 || true
  done
}
trap release_barriers EXIT
pause() {
  local url=$1 kind=$2
  curl -fsS -X POST -H 'Content-Type: application/json' \
    --data "{\"kinds\":[\"$kind\"]}" "$url/control/pause" >/dev/null
  paused_urls+=("$url")
}

log "checking that the Backend, the sandbox runner and the mock production service are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "sandbox runner /control/barrier" 60 runner_ready
wait_until "mock production /control/barrier" 60 production_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null
curl -fsS -X POST "$PRODUCTION_URL/control/release" >/dev/null

log "creating a Definition from $(basename "$FIXTURE") with script $script"
definition=$(jq --arg script "$script" --argjson timeout "$timeout_ms" '
  del(.workflowId)
  | .name = "Software Delivery (\($script))"
  | .nodes |= map(if .id == "node_agent"
      then .config.modelConfig.script = $script
        | (if $timeout > 0 then .config.timeoutMs = $timeout else . end)
      else . end)' "$FIXTURE")
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
runner_after=$(record_last_seq "$RUNNER_URL")
production_after=$(record_last_seq "$PRODUCTION_URL")
log "record baselines: mock provider seq=$record_after_seq runner seq=$runner_after production seq=$production_after"

case "$story" in
main | redelivery | restart) pause "$RUNNER_URL" callback ;;
deploy-lost) pause "$PRODUCTION_URL" deploy ;;
esac

task=$(jq -cn --arg base "$BASE_COMMIT" --arg patch "$PATCH" --arg service "$SERVICE" --arg env "$ENVIRONMENT" \
  '{baseCommit: $base, patch: $patch, target: {service: $service, environment: $env}}')
run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --arg task "$task" \
  '{workflowId: $wf, definitionVersion: $v, input: {task: $task}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"

agent_node_run() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id // empty'
}
agent_trace() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$(agent_node_run)/agent"
}
kill_and_restart_backend() {
  log "SIGKILL backend"
  compose kill -s SIGKILL backend >/dev/null
  log "starting backend again"
  compose start backend >/dev/null
  wait_until "restarted Backend /ready" "$RESTART_DEADLINE_S" backend_ready
}

# The runner finishes a test at once, so its callback could reach the Backend before the
# callback Binding commits; the Backend would then park it as pending. Holding the callback
# at the runner's barrier until the Agent waits makes the first delivery meet the Binding.
agent_waiting() {
  local node trace
  node=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .status') || return 1
  [[ "$node" == WAITING_CALLBACK ]] || return 1
  trace=$(agent_trace) || return 1
  jq -e '[.turns[].toolAttempts[] | select(.toolName == "sandbox_test" and .status == "DISPATCHED")] | length == 1' <<<"$trace" >/dev/null
}
callback_held() {
  curl -fsS "$RUNNER_URL/control/barrier" | jq -e '[.held[] | select(.kind == "callback")] | length >= 1' >/dev/null
}
test_attempt_before=""
case "$story" in
main | redelivery | restart)
  wait_until "the sandbox_test Attempt to be DISPATCHED while the Agent waits for its callback" "$HELD_DEADLINE_S" agent_waiting
  bound=$(agent_trace | jq -c '[.turns[].toolAttempts[] | select(.toolName == "sandbox_test")] | first | {id, testId: .callbackBinding.externalTaskId}')
  test_attempt_before=$(jq -r '.id' <<<"$bound")
  bound_test=$(jq -r '.testId' <<<"$bound")
  wait_until "the runner callback for test $bound_test to be held" "$HELD_DEADLINE_S" callback_held
  log "sandbox_test Attempt $test_attempt_before DISPATCHED for test $bound_test, its callback held at the runner"
  if [[ "$story" == restart ]]; then
    kill_and_restart_backend
  fi
  log "releasing the runner's callback"
  curl -fsS -X POST "$RUNNER_URL/control/release" >/dev/null
  ;;
deploy-lost)
  deploy_held() {
    curl -fsS "$PRODUCTION_URL/control/barrier" | jq -e '[.held[] | select(.kind == "deploy")] | length >= 1' >/dev/null
  }
  wait_until "the deployment request to be held by production" "$HELD_DEADLINE_S" deploy_held
  log "held at production: $(curl -fsS "$PRODUCTION_URL/control/barrier" | jq -c '[.held[] | {kind, operationId, arrivalSeq}]')"
  kill_and_restart_backend
  ;;
esac

run_terminal() {
  local status
  status=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" 2>/dev/null | jq -r '.run.status') || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}
wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal
release_barriers
paused_urls=()

events_before=$(run_events "$run_id")
if [[ "$story" == redelivery ]]; then
  runner_restart_after=$(record_last_seq "$RUNNER_URL")
  log "restarting the sandbox runner (record seq=$runner_restart_after); it delivers its stored results again"
  compose restart sandboxrunner >/dev/null
  wait_until "restarted sandbox runner /control/barrier" "$RESTART_DEADLINE_S" runner_ready
  redelivered_test=$(agent_trace | jq -r '[.turns[].toolAttempts[] | select(.toolName == "sandbox_test")] | first | .callbackBinding.externalTaskId')
  redelivery_answered() {
    record_after "$runner_restart_after" "$RUNNER_URL" |
      jq -se --arg id "$redelivered_test" '[.[] | select(.event == "callback" and .testId == $id and .attempt >= 2)] | length >= 1' >/dev/null
  }
  wait_until "the runner to redeliver test $redelivered_test" "$HELD_DEADLINE_S" redelivery_answered
fi

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
run_status=$(jq -r '.run.status' <<<"$snapshot")
trace=$(agent_trace)
events=$(run_events "$run_id")
runner=$(record_after "$runner_after" "$RUNNER_URL" | jq -s '.')
production=$(record_after "$production_after" "$PRODUCTION_URL" | jq -s '.')

tests=$(jq -c '[.turns[] | .action as $a | .toolAttempts[] | select(.toolName == "sandbox_test")
    | {id, status, testId: (.callbackBinding.externalTaskId // null), provider: (.callbackBinding.providerId // null), actionStatus: $a.status}]' <<<"$trace")
deploys=$(jq -c '[.turns[] | .action as $a | .toolAttempts[] | select(.toolName == "deploy")
    | {id, status, attemptNo, actionId: $a.id, actionStatus: $a.status, actionError: ($a.error.code // null),
       operationId: "op_\($a.id)_\(.attemptNo)"}]' <<<"$trace")
test_id=$(jq -r 'first.testId // ""' <<<"$tests")
operation_id=$(jq -r 'first.operationId // ""' <<<"$deploys")
termination=$(jq -r '.agentRun.termination // "none"' <<<"$trace")
fact=$(jq -c '[.facts.items[] | select(.factType == "patch_tested")]' <<<"$trace")
runner_test_arrivals=$(jq '[.[] | select(.event == "arrived" and .kind == "test")] | length' <<<"$runner")
runner_started=$(jq --arg id "$test_id" '[.[] | select(.event == "started" and .testId == $id)] | length' <<<"$runner")
runner_passed=$(jq --arg id "$test_id" '[.[] | select(.event == "completed" and .testId == $id and .passed == true)] | length' <<<"$runner")
runner_callbacks=$(jq -c --arg id "$test_id" '[.[] | select(.event == "callback" and .testId == $id) | {attempt, httpStatus}]' <<<"$runner")
production_arrivals=$(jq '[.[] | select(.event == "arrived" and .kind == "deploy")] | length' <<<"$production")
production_op_arrivals=$(jq --arg op "$operation_id" '[.[] | select(.event == "arrived" and .operationId == $op)] | length' <<<"$production")
deployed=$(jq -c --arg op "$operation_id" '[.[] | select(.event == "deployed" and .operationId == $op)]' <<<"$production")
deployed_count=$(jq 'length' <<<"$deployed")
callback_completions=$(jq '[.[] | select(.type == "AGENT_ACTION_COMPLETED" and .payload.completionSource == "CALLBACK")] | length' <<<"$events")

printf '\ndelivery %s story, run %s\n' "$story" "$run_id"
printf 'run status: %s, agent termination: %s, sandbox_test attempts: %s, deploy attempts: %s, runner test arrivals: %s, production deploy arrivals: %s\n\n' \
  "$run_status" "$termination" "$(jq 'length' <<<"$tests")" "$(jq 'length' <<<"$deploys")" "$runner_test_arrivals" "$production_arrivals"

one_passing_test() {
  jq -e 'length == 1 and .[0].status == "SUCCEEDED" and .[0].provider == "sandboxrunner"' <<<"$tests" >/dev/null &&
    jq -e 'length == 1 and .[0].verdict == true' <<<"$fact" >/dev/null
}
runner_ran_bound_test() {
  [[ -n "$test_id" && "$runner_test_arrivals" -eq 1 && "$runner_started" -eq 1 && "$runner_passed" -eq 1 ]]
}
deployed_once() {
  jq -e 'length == 1 and .[0].status == "SUCCEEDED" and .[0].attemptNo == 1' <<<"$deploys" >/dev/null &&
    [[ "$production_arrivals" -eq 1 && "$deployed_count" -eq 1 ]] &&
    jq -e '.[0].replayed == false' <<<"$deployed" >/dev/null
}
result=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.text // "{}"' <<<"$snapshot" | jq -c '.' 2>/dev/null || echo '{}')
version=$(jq -r 'first.version // ""' <<<"$deployed")
result_names_deployment() {
  jq -e --arg op "$operation_id" --arg v "$version" --argjson fact "$fact" \
    '.operationId == $op and .version == $v and .patchDigest == $fact[0].subject' <<<"$result" >/dev/null
}

case "$story" in
main | redelivery)
  check run_completed "status=$run_status" test "$run_status" = COMPLETED
  check one_passing_test "tests=$tests fact_verdict=$(jq -c 'map(.verdict)' <<<"$fact")" one_passing_test
  check runner_ran_bound_test "test=$test_id arrivals=$runner_test_arrivals started=$runner_started passed=$runner_passed" runner_ran_bound_test
  check deployed_once "operation=$operation_id production_arrivals=$production_arrivals deployed=$deployed_count" deployed_once
  check result_names_deployment "result=$result" result_names_deployment
  summary="test=$test_id passed=$runner_passed deploy=$operation_id version=$version runner_tests=$runner_test_arrivals production_deploys=$deployed_count"
  if [[ "$story" == redelivery ]]; then
    last_before=$(jq '[.[].seq] | max' <<<"$events_before")
    terminal_seq=$(jq '[.[] | select(.type == "RUN_COMPLETED" or .type == "RUN_FAILED") | .seq] | max' <<<"$events")
    last_after=$(jq '[.[].seq] | max' <<<"$events")
    redelivered=$(jq --arg id "$test_id" '[.[] | select(.event == "redelivered" and .testId == $id)] | length' <<<"$runner")
    later_answered() {
      jq -e '[.[] | select(.attempt >= 2 and .httpStatus == 200)] | length >= 1' <<<"$runner_callbacks" >/dev/null
    }
    check runner_redelivered "test=$test_id redelivered=$redelivered callbacks=$runner_callbacks" \
      test "$redelivered" -ge 1
    check redelivery_answered_200 "callbacks=$runner_callbacks" later_answered
    check no_event_after_terminal "terminal_seq=$terminal_seq last_seq_before=$last_before last_seq_after=$last_after" \
      test "$last_after" = "$terminal_seq" -a "$last_after" = "$last_before"
    check callback_completed_once "callback_completions=$callback_completions" test "$callback_completions" -eq 1
    summary="test=$test_id deliveries=$(jq -c 'map("\(.attempt):\(.httpStatus)")' <<<"$runner_callbacks") terminal_seq=$terminal_seq last_seq=$last_after callback_completions=$callback_completions status=$run_status"
  fi
  ;;
restart)
  test_attempt_after=$(jq -r 'first.id // ""' <<<"$tests")
  both_answered() {
    jq -e 'length == 2 and all(.[]; .httpStatus == 200) and (map(.attempt) | sort) == [1, 2]' <<<"$runner_callbacks" >/dev/null
  }
  check run_completed "status=$run_status" test "$run_status" = COMPLETED
  check original_attempt_completed "before=$test_attempt_before after=$tests" \
    test "$(jq 'length' <<<"$tests")" -eq 1 -a "$test_attempt_after" = "$test_attempt_before" -a "$(jq -r 'first.status' <<<"$tests")" = SUCCEEDED
  check test_not_redispatched "runner test arrivals=$runner_test_arrivals started=$runner_started" \
    test "$runner_test_arrivals" -eq 1 -a "$runner_started" -eq 1
  check both_deliveries_answered "callbacks=$runner_callbacks" both_answered
  check callback_completed_once "callback_completions=$callback_completions" test "$callback_completions" -eq 1
  check deployed_once "operation=$operation_id production_arrivals=$production_arrivals deployed=$deployed_count" deployed_once
  summary="test=$test_id attempt=$test_attempt_after deliveries=$(jq -c 'map("\(.attempt):\(.httpStatus)")' <<<"$runner_callbacks") callback_completions=$callback_completions deploys=$deployed_count status=$run_status"
  ;;
deploy-lost)
  abandoned=$(jq --arg op "$operation_id" '[.[] | select(.event == "abandoned" and .operationId == $op)] | length' <<<"$production")
  deploy_timed_out() {
    jq -e 'length == 1 and .[0].status == "FAILED" and .[0].attemptNo == 1
      and .[0].actionStatus == "FAILED" and .[0].actionError == "TIMEOUT"' <<<"$deploys" >/dev/null
  }
  check run_failed "status=$run_status" test "$run_status" = FAILED
  check agent_timed_out "termination=$termination" test "$termination" = TIMEOUT
  check deploy_attempt_timed_out "deploys=$deploys" deploy_timed_out
  check at_most_one_production_call "operation=$operation_id arrivals=$production_op_arrivals all_arrivals=$production_arrivals deployed=$deployed_count abandoned=$abandoned" \
    test "$production_op_arrivals" -le 1 -a "$production_arrivals" -le 1 -a "$deployed_count" -le 1
  summary="termination=$termination deploy=$operation_id attempts=$(jq 'length' <<<"$deploys") production_arrivals=$production_op_arrivals deployed=$deployed_count abandoned=$abandoned status=$run_status"
  ;;
esac

if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'delivery %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'delivery %s story: all checks passed\n' "$story"
