# shellcheck shell=bash
# The release stories of vuln-fix.sh, which sources this file; never executed on its own.
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
# The two stop stories give the release a fifteen-second confirm deadline, so the release
# reaches a person while the audience watches, and the person stops waiting through the
# human wait API with the same operator credential:
#
#   release-unreachable-stop
#                   production answers every release request and query with 503 from
#                   before the approval, so the operation's outcome stays unknown until the
#                   confirm deadline hands it to a person, who stops waiting. A second task
#                   then asks to release to the same target while production is still
#                   unreachable; production comes back, and once post-task reconciliation
#                   has freed the target a third task releases to it. Expects the stopped
#                   task FAILED as RELEASE_UNKNOWN (HUMAN_STOPPED), its human wait opened as
#                   OUTCOME_UNKNOWN and STOPPED, the target still ACTIVE with a query
#                   schedule after the stop, the second task's request FAILED as
#                   TARGET_OCCUPIED from AUTHORIZATION before any approval or Attempt, the
#                   target RELEASED as VOIDED with no unchecked version, production never
#                   having received the first operation and having voided it, no Event
#                   added to the stopped Run, and the third task COMPLETED as DEPLOYED with
#                   production deploying its operation once.
#   stop-then-deployed
#                   production accepts the approved release but holds its completion, so
#                   the release is unconfirmed at the confirm deadline and a person stops
#                   waiting; production then completes the deployment. Expects the task
#                   FAILED as RELEASE_UNKNOWN with its human wait opened as NOT_CONFIRMED
#                   and STOPPED, the target RELEASED as DEPLOYED afterwards with the version
#                   production deployed recorded as live but unchecked, no release_confirmed
#                   fact and no Event added to the stopped Run, and production deploying
#                   the operation once.

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

# open_human_wait_of RUN sets human_wait to the OPEN human wait of RUN as the operator list
# shows it, and fails while there is none.
human_wait=""
open_human_wait_of() {
  local items
  items=$(curl -fsS -H "$OPERATOR_HEADER" "$BACKEND_URL/api/human-waits") || return 1
  human_wait=$(jq -c --arg run "$1" '[.items[] | select(.runId == $run)] | first // empty' <<<"$items")
  [[ -n "$human_wait" ]]
}
# release_view_of RUN ACTION prints the release view of ACTION in RUN's Agent Trace.
release_view_of() {
  agent_trace_of "$1" | jq -c --arg id "$2" '[.turns[].action | select(. != null and .id == $id) | .release] | first // {}'
}
occupation_released() {
  release_view_of "$run_id" "$release_action_id" | jq -e '.occupation.status == "RELEASED"' >/dev/null
}
# The confirm deadline plus a few Reconciler passes, and the post-task query backoff a
# target freed after production comes back may have reached.
STOP_DEADLINE_S=120
FREED_DEADLINE_S=300
# stop_after_confirm_deadline waits until the confirm deadline handed the release of the
# story's Run to a person, stops that wait as the operator and waits for the Run to end.
opened_wait='{}'
stop_answer='{}'
stopped_events=0
stopped_events_after=0
occupation_after_stop='{}'
stop_after_confirm_deadline() {
  local wait_id
  wait_until "the release of run $run_id to wait for a person after its confirm deadline" "$STOP_DEADLINE_S" open_human_wait_of "$run_id"
  opened_wait=$human_wait
  wait_id=$(jq -r '.id' <<<"$opened_wait")
  log "human wait $wait_id opened: $(jq -c '{reason, operationKey, targetKey}' <<<"$opened_wait")"
  demo_pause "the release passed its confirm deadline without an answer and waits for a person (human wait $wait_id); next: the operator stops waiting"
  stop_answer=$(curl -fsS -X POST -H "$OPERATOR_HEADER" "$BACKEND_URL/api/human-waits/$wait_id/stop" |
    jq -c '{status: .humanWait.status, closedBy: .humanWait.closedBy, repeated}')
  log "human wait $wait_id stopped: $stop_answer"
  wait_until "run $run_id to end after the stop" "$TERMINAL_DEADLINE_S" run_terminal_of "$run_id"
  stopped_events=$(run_events "$run_id" | jq 'length')
  occupation_after_stop=$(release_view_of "$run_id" "$release_action_id" | jq -c '.occupation')
}
later_run_ids=""
contender_run_id=""
successor_run_id=""
successor_action_id=""

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

# drive_release_story waits for each release request of the story, decides it and
# injects the story's fault.
drive_release_story() {
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
  release-unreachable-stop) production_faults '{"unreachable":true}' ;;
  stop-then-deployed) production_completion true ;;
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
  release-unreachable-stop)
    approve "$release_approval_id"
    stop_after_confirm_deadline
    demo_pause "the task ended as RELEASE_UNKNOWN and $RELEASE_SERVICE/$RELEASE_ENVIRONMENT stays occupied; next: a second task asks to release to it while production is still unreachable"
    contender_run_id=$(create_run)
    wait_until "run $contender_run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal_of "$contender_run_id"
    demo_pause "the second task was refused as TARGET_OCCUPIED; next: production comes back and post-task reconciliation voids the operation it never received, which frees the target"
    production_faults '{}'
    wait_until "post-task reconciliation to free $RELEASE_SERVICE/$RELEASE_ENVIRONMENT" "$FREED_DEADLINE_S" occupation_released
    stopped_events_after=$(run_events "$run_id" | jq 'length')
    demo_pause "the target is free and the stopped Run gained no Event; next: a third task releases to the same target"
    successor_run_id=$(create_run)
    await_request "$successor_run_id"
    successor_action_id=$request_action
    approve "$request_approval"
    wait_until "run $successor_run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal_of "$successor_run_id"
    later_run_ids="$contender_run_id,$successor_run_id"
    ;;
  stop-then-deployed)
    approve "$release_approval_id"
    wait_until "production to accept $first_key" "$HELD_DEADLINE_S" production_line arrived "$first_key"
    stop_after_confirm_deadline
    demo_pause "the task ended as RELEASE_UNKNOWN with the target occupied; next: production finishes the deployment after the task ended"
    production_completion false
    wait_until "post-task reconciliation to free $RELEASE_SERVICE/$RELEASE_ENVIRONMENT" "$FREED_DEADLINE_S" occupation_released
    stopped_events_after=$(run_events "$run_id" | jq 'length')
    ;;
  esac
}

# check_stopped REASON: the person stopped the release wait the confirm deadline opened for
# REASON, and the task ended as RELEASE_UNKNOWN with the target still occupied and a
# post-task query scheduled; the stopped Run gained no Event afterwards.
check_stopped() {
  local release
  release=$(release_of "$release_action_id")
  check run_failed_release_unknown "status=$run_status node_error=$agent_node_error" \
    test "$run_status" = FAILED -a "$agent_node_error" = RELEASE_UNKNOWN
  check stopped_by_person "termination=$termination completion=$outcome" \
    test "$termination" = HUMAN_STOPPED -a "$outcome" = RELEASE_UNKNOWN
  check handed_to_person_after_deadline "wait=$(jq -c '{reason, operationKey}' <<<"$opened_wait") stop=$stop_answer" \
    jq -e --arg r "$1" --arg k "$first_key" --argjson stop "$stop_answer" \
    '.reason == $r and .operationKey == $k and $stop.status == "STOPPED" and $stop.repeated == false and ($stop.closedBy | length) > 0' <<<"$opened_wait"
  check human_wait_stopped "humanWait=$(jq -c '.humanWait' <<<"$release")" \
    jq -e '.humanWait.status == "STOPPED" and .humanWait.outcome == "RELEASE_UNKNOWN"' <<<"$release"
  check target_held_after_stop "occupation=$occupation_after_stop" \
    jq -e --arg k "$first_key" '.status == "ACTIVE" and .operationKey == $k and .nextQueryAt != null' <<<"$occupation_after_stop"
  check ended_run_gained_no_event "events at the stop=$stopped_events at the end=$stopped_events_after" \
    test "$stopped_events" -gt 0 -a "$stopped_events" -eq "$stopped_events_after"
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

# check_release_story runs the checks of a scripted release story.
check_release_story() {
  case "$story" in
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
  release-unreachable-stop)
    release_action=$(release_of "$release_action_id")
    unknown_events=$(jq -c '[.[] | select(.type == "RELEASE_OUTCOME_UNKNOWN") | .payload.operationKey]' <<<"$events")
    contender_status=$(run_status_of "$contender_run_id")
    contender_trace=$(agent_trace_of "$contender_run_id")
    contender_release=$(jq -c '[.turns[] | select(.decision.tool == "deploy_change" and .action != null)
        | {status: .action.status, code: (.action.error.code // null), failureSource: (.action.failureSource // null),
           approval: .action.approval, attempts: (.toolAttempts | length)}]' <<<"$contender_trace")
    contender_outcome=$(jq -r '.agentRun.completion.outcome // "none"' <<<"$contender_trace")
    successor_status=$(run_status_of "$successor_run_id")
    successor_trace=$(agent_trace_of "$successor_run_id")
    successor_outcome=$(jq -r '.agentRun.completion.outcome // "none"' <<<"$successor_trace")
    successor_release=$(jq -c --arg id "$successor_action_id" '[.turns[].action | select(. != null and .id == $id)
        | {status, occupation: (.release.occupation | {status, releaseReason})}] | first // {}' <<<"$successor_trace")
    successor_key="op_${successor_action_id}_1"
    check_stopped OUTCOME_UNKNOWN
    check outcome_unknown_once "unknown=$unknown_events" jq -e --arg k "$first_key" '. == [$k]' <<<"$unknown_events"
    check second_task_refused_target_occupied "status=$contender_status completion=$contender_outcome release=$contender_release" \
      jq -e --arg s "$contender_status" --arg o "$contender_outcome" 'length == 1 and .[0].status == "FAILED" and .[0].code == "TARGET_OCCUPIED"
        and .[0].failureSource == "AUTHORIZATION" and .[0].approval == null and .[0].attempts == 0
        and $s == "FAILED" and $o == "INCOMPLETE"' <<<"$contender_release"
    check target_freed_as_voided "occupation=$(jq -c '.occupation' <<<"$release_action")" \
      jq -e '.occupation.status == "RELEASED" and .occupation.releaseReason == "VOIDED"
        and .occupation.nextQueryAt == null and .occupation.deployedUncheckedVersion == null' <<<"$release_action"
    check production_voided_never_received "arrived=$(production_of "$first_key" arrived) voided=$(production_of "$first_key" voided) deployed=$(production_of "$first_key" deployed)" \
      test "$(production_of "$first_key" arrived)" -eq 0 -a "$(production_of "$first_key" voided)" -eq 1 -a "$(production_of "$first_key" deployed)" -eq 0
    check third_task_deployed_once "status=$successor_status completion=$successor_outcome release=$successor_release deployed=$(production_of "$successor_key" deployed)" \
      test "$successor_status" = COMPLETED -a "$successor_outcome" = DEPLOYED -a "$(jq -r '.status' <<<"$successor_release")" = SUCCEEDED \
      -a "$(jq -r '.occupation.releaseReason' <<<"$successor_release")" = DEPLOYED -a "$(production_of "$successor_key" deployed)" -eq 1 \
      -a "$(deployed_under "$successor_action_id")" -eq 1
    summary="stopped=$run_id:$outcome wait=$(jq -r '.reason' <<<"$opened_wait") contender=$contender_run_id:$(jq -r '.[0].code // "none"' <<<"$contender_release") occupation=$(jq -r '.occupation.releaseReason' <<<"$release_action") successor=$successor_run_id:$successor_outcome"
    run_status="$run_status (contender $contender_status, successor $successor_status)"
    ;;
  stop-then-deployed)
    release_action=$(release_of "$release_action_id")
    deployed_version=$(jq -r --arg k "$first_key" '[.[] | select(.event == "deployed" and .operationId == $k) | .version] | first // ""' <<<"$production")
    check_stopped NOT_CONFIRMED
    check target_freed_with_unchecked_version "occupation=$(jq -c '.occupation' <<<"$release_action") production_version=$deployed_version" \
      jq -e --arg v "$deployed_version" '$v != "" and .occupation.status == "RELEASED" and .occupation.releaseReason == "DEPLOYED"
        and .occupation.deployedUncheckedVersion == $v and .occupation.nextQueryAt == null' <<<"$release_action"
    check never_checked_by_the_task "release_facts=$release_facts" test "$(jq 'length' <<<"$release_facts")" -eq 0
    check production_deployed_once "arrived=$(production_of "$first_key" arrived) deployed=$(production_of "$first_key" deployed) under_action=$(deployed_under "$release_action_id")" \
      test "$(production_of "$first_key" arrived)" -eq 1 -a "$(production_of "$first_key" deployed)" -eq 1 -a "$(deployed_under "$release_action_id")" -eq 1
    summary="operation=$first_key wait=$(jq -r '.reason' <<<"$opened_wait") occupation=$(jq -r '.occupation.releaseReason' <<<"$release_action") unchecked_version=$(jq -r '.occupation.deployedUncheckedVersion' <<<"$release_action") completion=$outcome status=$run_status"
    ;;
  esac
}
