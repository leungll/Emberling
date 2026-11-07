#!/usr/bin/env bash
# Video-editing stories: an Agent cuts one recorded talk into a short vertical clip with
# the media Tools. The transcript, the timeline checks, the render and the clip check are
# real work of the media service; the Agent's decisions are scripted (the mock decision
# model replays a trajectory driven by the Tool results it receives), and the reviewed
# story's content review is the mock review model's fixed policy. Every check reads the
# Backend's public API; no live credential is involved.
#
# Usage: scripts/demo/video-edit.sh [definition] STORY
#   STORY is pass, too-long or review-fail.
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

STORIES="pass too-long review-fail"
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

log "creating a Definition from $fixture_name with script $script"
definition=$(build_definition)
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
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

run_terminal() {
  local status
  status=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" | jq -r '.run.status')
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
esac

printf '\nvideo edit %s story: deterministic decisions (scripted mock model)\n' "$story"
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
printf '\nrun %s\nclip %s\n' "$run_id" "$clip_url"

summary="run=$run_id outcome=$outcome timeline=$timeline_verdicts clip=$clip_verdicts review=${review_verdicts:-none} decisions=$decisions"
if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' "$run_id" "$workflow_id" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'video edit %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'video edit %s story: all checks passed\n' "$story"
