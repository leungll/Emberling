#!/usr/bin/env bash
# Video-editing stories: an Agent cuts one recorded talk into a short vertical clip with
# the media Tools. The transcript, the timeline checks, the render and the clip check are
# real work of the media service; the Agent's decisions are scripted (the mock decision
# model replays a trajectory driven by the Tool results it receives), and the reviewed
# story's content review is the mock review model's fixed policy. Every check reads the
# Backend's public API; no live credential is involved.
#
# Usage: scripts/demo/video-edit.sh [definition] STORY
#   STORY is pass, too-long, review-fail or restart.
#
# "definition" prints the Definition the story would save, as JSON, and touches nothing:
# it needs jq only.
#
#   pass         the Agent reads the transcript, plans, checks a 45-second timeline of
#                three segments, renders it and checks the clip. Expects a COMPLETED Run
#                judged CLIP_DELIVERED, one passed TIMELINE media_checked fact, one
#                clip_rendered fact, one passed CLIP media_checked fact about that clip,
#                no content_reviewed fact and five committed Decisions.
#   too-long     the Agent's first timeline runs a minute against a 45-second target, so
#                its check fails with TIMELINE_DURATION_OUT_OF_RANGE; the Agent drops the
#                last segment, checks again, renders and checks the clip. Expects the
#                same end with exactly one failed TIMELINE media_checked fact before the
#                passed one, and six committed Decisions.
#   review-fail  the reviewed Definition: the Agent's first timeline ends on a sentence
#                the speaker continues in the next one, so the review model reports the
#                meaning distorted and the Tool records a failed content_reviewed fact;
#                the Agent adds the continuing sentence, checks and reviews again, renders
#                and checks the clip. Expects CLIP_DELIVERED with two passed TIMELINE
#                checks, exactly one failed content_reviewed fact before the passed one,
#                and eight committed Decisions.
#   restart      fault injection on the pass trajectory: once the render_timeline Attempt
#                is DISPATCHED and the media service holds its RENDER task, the backend is
#                SIGKILLed and started again (the container keeps its environment, so the
#                callback token issued before the kill still verifies). Expects everything
#                pass expects, and in addition: the Attempt bound before the kill is the one
#                that SUCCEEDED, there is exactly one render_timeline Attempt and one
#                clip_rendered fact under it, the media service's task for that Attempt is
#                the one recorded before the kill and SUCCEEDED, the Turns and committed
#                Decisions from before the kill are a prefix of the final ones (nothing was
#                decided twice), exactly one AGENT_ACTION_COMPLETED names that Attempt and
#                says whether the callback or the Provider poll completed it, and the Event
#                seq numbers are strictly increasing without a gap. Ends with the evidence
#                report of the Run (video-evidence-report.sh). Needs the kill and the restart
#                to land inside the render, which takes tens of seconds for a 45-second clip.
#
# The stack: postgres and the backend from deploy/compose.yaml, with the media service
# from the media profile unless EMBERLING_DEMO_MEDIA_URL names the media service the
# backend container should use instead (for example http://host.docker.internal:9201 for
# one running on the host). EMBERLING_DEMO_MEDIA_HOST_URL (default http://localhost:9201)
# is where this script, on the host, uploads the source and reads clip links; with the
# media profile it is the published port. A media service outside the compose network
# must be able to reach the backend's callback address: unless EMBERLING_CALLBACK_BASE_URL
# is set, the override makes it the backend's published port. The transcription of a
# source is cached by the media service, so only the first story of a source transcribes.
#
# The source is the first talk of the evaluation dataset, identified by its SHA-256. When
# the media service does not have it yet, the file EMBERLING_DEMO_VIDEO_SOURCE names
# (default: the dataset checkout beside this repository) is checked against that digest
# and uploaded; upload is idempotent by content.
#
# Writes RUN_ID, WORKFLOW_ID, RUN_STATUS and SUMMARY to $DEMO_STATE when it is set.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools jq

STORIES="pass too-long review-fail restart"
usage="usage: video-edit.sh [definition] ${STORIES// /|}"

mode=run
if [[ "${1:-}" == definition ]]; then
  mode=definition
  shift
fi
story=${1:-}
case "$story" in
pass)
  script=video-edit-pass
  fixture_name=video_edit_basic.json
  ;;
too-long)
  script=video-edit-too-long
  fixture_name=video_edit_basic.json
  ;;
review-fail)
  script=video-edit-review-fail
  fixture_name=video_edit_reviewed.json
  ;;
restart)
  script=video-edit-pass
  fixture_name=video_edit_basic.json
  ;;
*) die "$usage" ;;
esac
FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/$fixture_name"

# The source talk: the first sample of the evaluation dataset.
SOURCE_SHA256=422934139a6c23fbce1947bb3099e017d16a8b7d4e1af3c678fdc84955407403
SOURCE_ID="src_${SOURCE_SHA256:0:32}"
SOURCE_FILE=${EMBERLING_DEMO_VIDEO_SOURCE:-$DEMO_ROOT/../Emberling-video-data/sources/01-deborah-engineer.webm}
case "$story" in
review-fail) REQUIREMENT="Cut a short vertical clip with her advice to women starting out in engineering." ;;
*) REQUIREMENT="Cut a 45-second vertical clip on why she loves engineering and what the job actually involves from one day to the next." ;;
esac

# build_definition prints the Definition this story saves: the fixture with its Agent
# bound to the story's trajectory.
build_definition() {
  jq --arg script "$script" '
    del(.workflowId)
    | .name = "Video Edit (\($script))"
    | .nodes |= map(if .id == "node_agent" then .config.modelConfig.script = $script else . end)' "$FIXTURE"
}
if [[ "$mode" == definition ]]; then
  build_definition
  exit 0
fi
require_tools docker curl shasum

MEDIA_HOST_URL=${EMBERLING_DEMO_MEDIA_HOST_URL:-http://localhost:9201}
media_profile=(--profile media)
media_service=media
if [[ -n "${EMBERLING_DEMO_MEDIA_URL:-}" ]]; then
  export EMBERLING_MEDIA_SERVICE_URL=$EMBERLING_DEMO_MEDIA_URL
  media_profile=()
  media_service=""
  if [[ -z "${EMBERLING_CALLBACK_BASE_URL:-}" ]]; then
    export EMBERLING_CALLBACK_BASE_URL=$BACKEND_URL
  fi
else
  export EMBERLING_MEDIA_SERVICE_URL=http://media:9201
fi
video_compose() {
  docker compose -f "$COMPOSE_FILE" "${media_profile[@]+"${media_profile[@]}"}" "$@"
}

# The wait for the terminal Run covers the transcript node's Attempt deadline, the Agent's
# budget and a minute of Tool work after it.
transcript_timeout_ms=$(jq '.nodes[] | select(.id == "node_transcript") | .executionPolicy.timeoutMs' "$FIXTURE")
agent_timeout_ms=$(jq '.nodes[] | select(.id == "node_agent") | .config.timeoutMs' "$FIXTURE")
TERMINAL_DEADLINE_S=${EMBERLING_DEMO_VIDEO_TERMINAL_DEADLINE_S:-$(((transcript_timeout_ms + agent_timeout_ms) / 1000 + 60))}
# The restart story waits for the render dispatch through the transcript (cached after the
# first story of a source) and the Agent's first two Tool calls, then for the restarted
# backend.
DISPATCH_DEADLINE_S=${EMBERLING_DEMO_VIDEO_DISPATCH_DEADLINE_S:-$((transcript_timeout_ms / 1000 + 120))}
RESTART_DEADLINE_S=120
EVIDENCE_DIR=${EMBERLING_DEMO_VIDEO_EVIDENCE_DIR:-$DEMO_ROOT/deploy/.demo/video-evidence}

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

media_ready() {
  curl -fsS -o /dev/null "$MEDIA_HOST_URL/healthz" 2>/dev/null
}

# A backend left killed by an interrupted restart story is started again on exit.
backend_down=false
cleanup() {
  if [[ "$backend_down" == true ]]; then
    video_compose start backend >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

log "building the backend image (cached when sources are unchanged)"
video_compose build --quiet backend $media_service
log "starting postgres, backend${media_service:+ and $media_service} (media service for the backend: $EMBERLING_MEDIA_SERVICE_URL)"
video_compose up -d --wait postgres backend $media_service
wait_until "Backend /ready" 60 backend_ready
wait_until "media service $MEDIA_HOST_URL/healthz" 300 media_ready

# The source is addressed by content: it is uploaded only when the service lacks it.
if source=$(curl -fsS "$MEDIA_HOST_URL/v1/sources/$SOURCE_ID" 2>/dev/null); then
  log "source $SOURCE_ID is at the media service"
else
  [[ -f "$SOURCE_FILE" ]] || die "source $SOURCE_ID is not at the media service and $SOURCE_FILE does not exist; set EMBERLING_DEMO_VIDEO_SOURCE to the file whose SHA-256 is $SOURCE_SHA256"
  actual=$(shasum -a 256 "$SOURCE_FILE" | cut -d' ' -f1)
  [[ "$actual" == "$SOURCE_SHA256" ]] || die "$SOURCE_FILE has SHA-256 $actual, want $SOURCE_SHA256"
  log "uploading $(basename "$SOURCE_FILE") to the media service"
  source=$(curl -fsS -F "file=@$SOURCE_FILE;type=video/webm" "$MEDIA_HOST_URL/v1/sources")
fi
[[ "$(jq -r '.sourceId' <<<"$source")" == "$SOURCE_ID" ]] || die "the media service answered a different source: $source"
[[ "$(jq -r '.sha256' <<<"$source")" == "$SOURCE_SHA256" ]] || die "the media service answered a different digest: $source"
duration_ms=$(jq '.durationMs' <<<"$source")

# Each story keeps one Workflow, found by its name, so repeated demos add Runs (and, when
# the fixture changed, a new version) instead of a new Workflow every time. A saved
# version is reused only when its graph equals the one this story would save; a Run
# stays bound to the version it was created with either way. The Backend fills defaults
# (a model temperature, for one) when it saves, so "equal" means every value this story
# sets is saved unchanged and the Nodes and Edges are the same ones.
definition=$(build_definition)
definition_name=$(jq -r '.name' <<<"$definition")
workflow_id=$(curl -fsS "$BACKEND_URL/api/definitions" |
  jq -r --arg name "$definition_name" '[.items[] | select(.name == $name)] | sort_by(.updatedAt) | last | .workflowId // empty')
if [[ -z "$workflow_id" ]]; then
  log "creating a Definition from $fixture_name with script $script"
  saved=$(curl -fsS -X POST -H 'Content-Type: application/json' \
    --data-binary "$definition" "$BACKEND_URL/api/definitions")
else
  latest=$(curl -fsS "$BACKEND_URL/api/definitions/$workflow_id")
  if jq -e --argjson want "$definition" '
    . as $saved
    | ($want | {name, description, nodes, edges}) as $w
    | ([$w.nodes[].id, "|", $w.edges[].id] == [$saved.nodes[].id, "|", $saved.edges[].id])
      and ([$w | paths(scalars)] | all(. as $p | ($w | getpath($p)) == ($saved | getpath($p))))' \
    <<<"$latest" >/dev/null; then
    log "reusing $workflow_id, its latest version matches $fixture_name with script $script"
    saved=$latest
  else
    log "saving a new version of $workflow_id from $fixture_name with script $script"
    saved=$(jq --argjson base "$(jq '.version' <<<"$latest")" '. + {baseVersion: $base}' <<<"$definition" |
      curl -fsS -X PUT -H 'Content-Type: application/json' --data-binary @- "$BACKEND_URL/api/definitions/$workflow_id")
  fi
fi
workflow_id=$(jq -r '.workflowId' <<<"$saved")
version=$(jq -r '.version' <<<"$saved")
log "definition $workflow_id version $version"

task=$(jq -cn --arg sourceId "$SOURCE_ID" --arg sha "$SOURCE_SHA256" --argjson duration "$duration_ms" \
  --arg requirement "$REQUIREMENT" \
  '{sourceId: $sourceId, sourceSha256: $sha, durationMs: $duration, requirement: $requirement, goal: "DELIVER_CLIP"}')
run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson task "$task" \
  '{workflowId: $wf, definitionVersion: $v, input: {task: $task}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"
studio_run_link "$run_id"

agent_node_run() {
  curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id // empty'
}
agent_trace() {
  local node
  node=$(agent_node_run) || return 1
  [[ -n "$node" ]] || return 1
  curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$node/agent"
}
# render_attempts prints the render_timeline Tool Attempts of a Trace with their Action's
# status, oldest first.
render_attempts() {
  jq -c '[.turns[] | .action as $a | .toolAttempts[] | select(.toolName == "render_timeline")
    | {id, attemptNo, status, task: .callbackBinding.externalTaskId, action: $a.status}]' <<<"$1"
}
# turn_ids prints the Turn ids of a Trace in order; decision_turns the turnId of every
# committed Decision in Event order.
turn_ids() { jq -c '[.turns[].id]' <<<"$1"; }
decision_turns() { jq -c '[.[] | select(.type == "AGENT_DECISION_COMMITTED") | .payload.turnId]' <<<"$1"; }

if [[ "$story" == restart ]]; then
  # The moment the story is about: the render dispatch has been committed (the Attempt is
  # DISPATCHED and bound to the media service's task), so the task belongs to the media
  # service and the Attempt to PostgreSQL. The condition, not the clock, decides the kill.
  render_dispatched() {
    local trace
    trace=$(agent_trace 2>/dev/null) || return 1
    jq -e '[.turns[].toolAttempts[] | select(.toolName == "render_timeline" and .status == "DISPATCHED" and .callbackBinding.externalTaskId != null)] | length == 1' <<<"$trace" >/dev/null
  }
  log "waiting up to ${DISPATCH_DEADLINE_S}s for the render_timeline Attempt to be DISPATCHED (the first story of a source transcribes it first)"
  await_running "$run_id" "the render_timeline Attempt to be DISPATCHED and bound to a media task" "$DISPATCH_DEADLINE_S" render_dispatched
  trace_before=$(agent_trace)
  events_before=$(run_events "$run_id")
  render_before=$(render_attempts "$trace_before" | jq -c 'first')
  render_attempt=$(jq -r '.id' <<<"$render_before")
  render_task=$(jq -r '.task' <<<"$render_before")
  turns_before=$(turn_ids "$trace_before")
  decisions_before=$(decision_turns "$events_before")
  # The media service's own view of the task, read just before the kill.
  task_before=$(curl -fsS "$MEDIA_HOST_URL/v1/tasks/$render_task" | jq -c '{taskId, status}')
  log "before the kill: render_timeline $render_before; media task $task_before; $(jq 'length' <<<"$turns_before") Turn(s), $(jq 'length' <<<"$decisions_before") committed Decision(s)"
  demo_pause "run $run_id: render_timeline Attempt $render_attempt is DISPATCHED to media task $render_task, which is rendering; next: SIGKILL the backend. Continue before the render ends (tens of seconds)"

  log "SIGKILL backend"
  killed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  backend_down=true
  video_compose kill -s SIGKILL backend >/dev/null
  demo_pause "backend killed while the media service renders; the Attempt is DISPATCHED in PostgreSQL and the task is the media service's to finish. Next: start the backend; its poll and the service's retried callback both enter the same resume path"

  log "starting backend again"
  video_compose start backend >/dev/null
  backend_down=false
  wait_until "restarted Backend /ready" "$RESTART_DEADLINE_S" backend_ready
  restarted_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  render_after_restart=$(render_attempts "$(agent_trace)" | jq -c 'first')
  log "after the restart: render_timeline $render_after_restart (read from the restarted backend, which found it in PostgreSQL)"
fi

run_terminal() {
  local status
  status=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" 2>/dev/null | jq -r '.run.status') || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}
started=$SECONDS
log "waiting up to ${TERMINAL_DEADLINE_S}s for the Run to end (the first story of a source transcribes it)"
wait_until "run $run_id to end" "$TERMINAL_DEADLINE_S" run_terminal
log "run ended after $((SECONDS - started))s"

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
run_status=$(jq -r '.run.status' <<<"$snapshot")
agent_node_run_id=$(jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id' <<<"$snapshot")
trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$agent_node_run_id/agent")
events=$(run_events "$run_id")
decisions=$(jq '[.[] | select(.type == "AGENT_DECISION_COMMITTED")] | length' <<<"$events")
outcome=$(jq -r '.generationBudget.completion.outcome // .agentRun.completion.outcome // "none"' <<<"$trace")
# Fact verdicts in recorded order, one letter each, per fact type and check kind.
verdicts() {
  jq -r --arg type "$1" --arg kind "${2:-}" '
    [.facts.items[] | select(.factType == $type and ($kind == "" or .bindings.kind == $kind))
      | if .verdict == true then "T" elif .verdict == false then "F" else "?" end] | join("")' <<<"$trace"
}
timeline_verdicts=$(verdicts media_checked TIMELINE)
clip_verdicts=$(verdicts media_checked CLIP)
review_verdicts=$(verdicts content_reviewed)
rendered_subjects=$(jq -r '[.facts.items[] | select(.factType == "clip_rendered") | .subject] | join(",")' <<<"$trace")
clip_sha256=$(jq -r '[.facts.items[] | select(.factType == "clip_rendered") | .subject] | last // ""' <<<"$trace")
clip_check_subject=$(jq -r '[.facts.items[] | select(.factType == "media_checked" and .bindings.kind == "CLIP") | .subject] | last // ""' <<<"$trace")
# The result node receives the completion verdict with the Agent's own report inside it.
report=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.text // "{}"' <<<"$snapshot" | jq -c '.agentReport // {}' 2>/dev/null || echo '{}')
report_outcome=$(jq -r '.outcome // "none"' <<<"$report")
report_clip=$(jq -r '.clipSha256 // ""' <<<"$report")
transcript_error=$(jq -r '[.nodeRuns[] | select(.nodeId == "node_transcript") | .error.code // empty] | first // "none"' <<<"$snapshot")

case "$story" in
pass)
  want_timeline=T
  want_review=""
  want_decisions=5
  ;;
too-long)
  want_timeline=FT
  want_review=""
  want_decisions=6
  ;;
review-fail)
  want_timeline=TT
  want_review=FT
  want_decisions=8
  ;;
restart)
  want_timeline=T
  want_review=""
  want_decisions=5
  ;;
esac

if [[ "$story" == restart ]]; then
  printf '\nvideo edit restart story: fault injection (SIGKILL during rendering), deterministic decisions (scripted mock model)\n'
else
  printf '\nvideo edit %s story: deterministic decisions (scripted mock model)\n' "$story"
fi
check run_completed "status=$run_status transcript_error=$transcript_error" test "$run_status" == COMPLETED
check clip_delivered "outcome=$outcome" test "$outcome" == CLIP_DELIVERED
check timeline_checks "media_checked TIMELINE verdicts=$timeline_verdicts want=$want_timeline" test "$timeline_verdicts" == "$want_timeline"
check clip_rendered_once "clip_rendered subjects=$rendered_subjects" test "$(tr -cd ',' <<<"$rendered_subjects" | wc -c | tr -d ' ')" -eq 0 -a -n "$clip_sha256"
check clip_check_passed "media_checked CLIP verdicts=$clip_verdicts subject_matches_clip=$([[ "$clip_check_subject" == "$clip_sha256" ]] && echo yes || echo no)" test "$clip_verdicts" == T -a "$clip_check_subject" == "$clip_sha256"
check content_reviews "content_reviewed verdicts=${review_verdicts:-none} want=${want_review:-none}" test "$review_verdicts" == "$want_review"
check decisions_committed "decisions=$decisions want=$want_decisions" test "$decisions" -eq "$want_decisions"
check final_report "outcome=$report_outcome clipSha256_matches=$([[ "$report_clip" == "$clip_sha256" ]] && echo yes || echo no)" test "$report_outcome" == DELIVERED -a "$report_clip" == "$clip_sha256"

clip_url="$MEDIA_HOST_URL/v1/clips/$clip_sha256"
if [[ -n "$clip_sha256" ]]; then
  # The clip route serves the file; a one-byte range proves it is there without downloading it.
  check clip_served "$clip_url" curl -fsS -o /dev/null -r 0-0 "$clip_url"
fi

if [[ "$story" == restart ]]; then
  render_attempts_final=$(render_attempts "$trace")
  turns_final=$(turn_ids "$trace")
  decisions_final=$(decision_turns "$events")
  task_final=$(curl -fsS "$MEDIA_HOST_URL/v1/tasks/$render_task" | jq -c '{taskId, status}')
  rendered_by_attempt=$(jq --arg id "$render_attempt" '[.facts.items[] | select(.factType == "clip_rendered" and .toolAttemptId == $id)] | length' <<<"$trace")
  # The Event that completed the in-flight Attempt names the path that did it: the media
  # service's callback, or the Backend's own poll of the task.
  completions=$(jq -c --arg id "$render_attempt" '[.[] | select((.type == "AGENT_ACTION_COMPLETED" or .type == "AGENT_ACTION_FAILED") and .payload.toolAttemptId == $id) | {type, source: (.payload.completionSource // .payload.failureSource)}]' <<<"$events")
  completion_path=$(jq -r 'if length == 1 and .[0].type == "AGENT_ACTION_COMPLETED" then .[0].source else "none" end' <<<"$completions")
  seqs=$(jq -c '[.[].seq]' <<<"$events")
  event_at() {
    jq -r --arg type "$1" --arg id "${2:-}" '[.[] | select(.type == $type and ($id == "" or .payload.toolAttemptId == $id)) | .timestamp] | first // "-"' <<<"$events"
  }

  printf '\ntimeline of the render Action (media task %s):\n' "$render_task"
  printf '  %-36s %s\n' \
    "AGENT_ACTION_WAITING (ledger)" "$(event_at AGENT_ACTION_WAITING)" \
    "Backend SIGKILL (script)" "$killed_at" \
    "Backend restart (script)" "$restarted_at" \
    "AGENT_ACTION_COMPLETED (ledger)" "$(event_at AGENT_ACTION_COMPLETED "$render_attempt")" \
    "RUN_COMPLETED (ledger)" "$(event_at RUN_COMPLETED)"
  printf 'evidence: before the kill     %s, media task %s\n' "$render_before" "$task_before"
  printf 'evidence: after the restart   %s\n' "$render_after_restart"
  printf 'evidence: at the end          attempts=%s, media task %s\n' "$render_attempts_final" "$task_final"
  printf 'evidence: completed by        %s\n' "$completion_path"
  printf 'evidence: turns               before=%s final=%s\n' "$turns_before" "$turns_final"
  printf 'evidence: decisions           before=%s final=%s\n\n' "$decisions_before" "$decisions_final"

  task_in_flight_at_kill() {
    jq -e --arg id "$render_task" '.taskId == $id and (.status == "QUEUED" or .status == "RUNNING")' <<<"$task_before" >/dev/null
  }
  check task_in_flight_at_kill "media task at the kill: $task_before" task_in_flight_at_kill
  attempt_survived_restart() {
    jq -e --arg id "$render_attempt" '.id == $id and (.status == "DISPATCHED" or .status == "SUCCEEDED")' <<<"$render_after_restart" >/dev/null
  }
  check attempt_survived_restart "right after the restart: $render_after_restart" attempt_survived_restart
  original_attempt_completed() {
    jq -e --arg id "$render_attempt" 'length == 1 and .[0].id == $id and .[0].attemptNo == 1 and .[0].status == "SUCCEEDED" and .[0].action == "SUCCEEDED"' <<<"$render_attempts_final" >/dev/null
  }
  check original_attempt_completed "before=$render_attempt final=$render_attempts_final" original_attempt_completed
  check clip_rendered_by_that_attempt "clip_rendered facts under $render_attempt: $rendered_by_attempt" test "$rendered_by_attempt" -eq 1
  task_not_resubmitted() {
    # One Attempt means one task id at the media service; the task recorded before the
    # kill is the one that finished.
    jq -e --arg id "$render_task" '.taskId == $id and .status == "SUCCEEDED"' <<<"$task_final" >/dev/null
  }
  check render_task_not_resubmitted "media task $render_task: $(jq -r '.status' <<<"$task_final"), render attempts=$(jq 'length' <<<"$render_attempts_final")" task_not_resubmitted
  check completed_once_by "completions=$completions" test "$completion_path" == CALLBACK -o "$completion_path" == PROVIDER_POLL
  turns_prefix() {
    jq -e --argjson before "$turns_before" '.[0:($before | length)] == $before and (length == (unique | length))' <<<"$turns_final" >/dev/null
  }
  check turns_not_repeated "before=$(jq 'length' <<<"$turns_before") final=$(jq 'length' <<<"$turns_final") want=$want_decisions" turns_prefix
  decisions_prefix() {
    jq -e --argjson before "$decisions_before" --argjson want "$want_decisions" \
      '.[0:($before | length)] == $before and length == $want and (unique | length) == $want' <<<"$decisions_final" >/dev/null
  }
  check decisions_not_repeated "before=$(jq 'length' <<<"$decisions_before") final=$(jq 'length' <<<"$decisions_final") distinct_turns=$(jq 'unique | length' <<<"$decisions_final")" decisions_prefix
  seqs_in_order() {
    jq -e '. == sort and length == (unique | length)' <<<"$seqs" >/dev/null
  }
  check events_replay_in_order "events=$(jq 'length' <<<"$seqs") seq $(jq 'min' <<<"$seqs")..$(jq 'max' <<<"$seqs")" seqs_in_order
fi

printf '\nrun %s\nclip %s\n' "$run_id" "$clip_url"

summary="run=$run_id outcome=$outcome timeline=$timeline_verdicts clip=$clip_verdicts review=${review_verdicts:-none} decisions=$decisions"
if [[ "$story" == restart ]]; then
  summary="$summary attempt=$render_attempt task=$render_task completed_by=$completion_path"
fi
if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' "$run_id" "$workflow_id" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'video edit %s story: %d check(s) FAILED\n' "$story" "$failures"
else
  printf 'video edit %s story: all checks passed\n' "$story"
fi

report_status=0
if [[ "$story" == restart ]]; then
  # The fault story ends with the evidence report of its Run, read back through the API.
  log "evidence report for run $run_id under $EVIDENCE_DIR"
  "$DEMO_ROOT/scripts/demo/video-evidence-report.sh" --out "$EVIDENCE_DIR" "$run_id" || report_status=$?
fi
((failures == 0 && report_status == 0))
