#!/usr/bin/env bash
# Photo effect-template stories: an Agent driven by the scripted mock model applies one
# effect template across three uploaded photos, using the Mock Provider's image, review
# and video Tools. Every check reads the Backend's public API and the Mock Provider's
# dispatch record; no live credential is involved.
#
# Usage: scripts/demo/photo-set.sh main|gate|limit
#
#   main   two rounds over the three photos: six generations, each followed by a review,
#          then one video completed by callback and a FINAL naming one reviewed example
#          per photo. Expects a COMPLETED Run whose media result is accepted with 3 of 3
#          passed, six generation and six review facts in the Trace (each review based on
#          a generation), a generation budget of 6 used, and a dispatch record with
#          exactly the six generated assets of the ledger and one video task.
#   gate   generates one image, then asks for a video of it without a review. Expects the
#          video request rejected at claim with PRECONDITION_UNMET, the Agent ended as
#          INVALID_ACTION, a FAILED Run and no video task in the dispatch record.
#   limit  runs with a generation limit of two and asks for a third generation. Expects
#          it rejected at claim with GENERATION_LIMIT_REACHED, a FAILED Run and exactly
#          two generated assets in the dispatch record.
#
# Writes RUN_ID, WORKFLOW_ID, RECORD_AFTER, RUN_STATUS and SUMMARY to $DEMO_STATE when it
# is set, for ledger-vs-record.sh and the demo summary.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools curl jq

story=${1:-}
case "$story" in
main) script=photo-set limit=0 ;;
gate) script=photo-set-skip-review limit=0 ;;
limit) script=photo-set-limit limit=2 ;;
*) die "usage: photo-set.sh main|gate|limit" ;;
esac

FIXTURE="$DEMO_ROOT/backend/test/fixtures/definitions/photo_effect_template.json"
TERMINAL_DEADLINE_S=120

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
check() {
  local name=$1 detail=$2
  shift 2
  if "$@"; then pass "$(printf '%-34s %s' "$name" "$detail")"; else fail "$(printf '%-34s %s' "$name" "$detail")"; fi
}

log "checking that the Backend and the Mock Provider test controls are reachable"
wait_until "Backend /ready" 60 backend_ready
wait_until "Mock Provider /control/barrier" 60 mock_ready
# A barrier left paused by an interrupted earlier run would hold unrelated requests.
curl -fsS -X POST "$MOCK_URL/control/release" >/dev/null

log "uploading three photos"
photos_dir=$(mktemp -d "${TMPDIR:-/tmp}/emberling-photos.XXXXXX")
trap 'rm -rf "$photos_dir"' EXIT
photos='[]'
for name in harbour market orchard; do
  # Distinct bytes per photo and per invocation; the Mock Provider does not decode images.
  printf '\x89PNG\r\n\x1a\nphoto %s %s\n' "$name" "$(date +%s)$RANDOM" >"$photos_dir/$name.png"
  ref=$(curl -fsS -F "file=@$photos_dir/$name.png;type=image/png" "$BACKEND_URL/api/assets")
  photos=$(jq -c --argjson ref "$ref" '. + [$ref]' <<<"$photos")
done
log "photos $(jq -c 'map(.assetId)' <<<"$photos")"

log "creating a Definition from $(basename "$FIXTURE") with script $script"
definition=$(jq --arg script "$script" --argjson limit "$limit" '
  del(.workflowId)
  | .name = "Photo Effect Template (\($script))"
  | .nodes |= map(if .id == "node_agent"
      then .config.modelConfig.script = $script
        | (if $limit > 0 then .config.maxGenerationCalls = $limit else . end)
      else . end)' "$FIXTURE")
created=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$definition" "$BACKEND_URL/api/definitions")
workflow_id=$(jq -r '.workflowId' <<<"$created")
version=$(jq -r '.version' <<<"$created")
log "definition $workflow_id version $version"

record_after_seq=$(record_last_seq)
log "dispatch record baseline seq=$record_after_seq"

run_request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson photos "$photos" \
  '{workflowId: $wf, definitionVersion: $v, input: {brief: "One warm film look for the whole set", photos: $photos}}')
run_id=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  --data-binary "$run_request" "$BACKEND_URL/api/runs" | jq -r '.id')
log "run $run_id created"

run_terminal() {
  local status
  status=$(curl -fsS "$BACKEND_URL/api/runs/$run_id" 2>/dev/null | jq -r '.run.status') || return 1
  [[ "$status" == COMPLETED || "$status" == FAILED ]]
}
wait_until "run $run_id to become COMPLETED or FAILED" "$TERMINAL_DEADLINE_S" run_terminal

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
run_status=$(jq -r '.run.status' <<<"$snapshot")
agent_node_run=$(jq -r '.nodeRuns[] | select(.nodeId == "node_agent") | .id' <<<"$snapshot")
trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$agent_node_run/agent")
window=$(record_after "$record_after_seq" | jq -s '.')

generated_record=$(jq -c '[.[] | select(.event == "generated") | .assetId] | sort' <<<"$window")
video_record=$(jq -c '[.[] | select(.event == "video_dispatched") | .externalTaskId] | sort' <<<"$window")
generated_ledger=$(jq -c '[.facts.items[] | select(.factType == "image_generated") | .subject] | sort' <<<"$trace")
reviewed_count=$(jq '[.facts.items[] | select(.factType == "asset_reviewed")] | length' <<<"$trace")
used=$(jq '.generationBudget.generationCallsUsed' <<<"$trace")
max_calls=$(jq '.generationBudget.maxGenerationCalls' <<<"$trace")
termination=$(jq -r '.agentRun.termination // "none"' <<<"$trace")
rejected_code=$(jq -r '[.turns[] | select(.action.status == "FAILED") | .action.error.code] | first // "none"' <<<"$trace")
generated_count=$(jq 'length' <<<"$generated_record")
video_count=$(jq 'length' <<<"$video_record")

printf '\nphoto-set %s story, run %s (record lines after seq %s)\n' "$story" "$run_id" "$record_after_seq"
printf 'run status: %s, agent termination: %s, generation budget: %s of %s, record generated: %s, record video tasks: %s\n\n' \
  "$run_status" "$termination" "$used" "$max_calls" "$generated_count" "$video_count"

case "$story" in
main)
  caption=$(jq -r '.nodeRuns[] | select(.nodeId == "node_result") | .output.caption // "{}"' <<<"$snapshot" | jq -c '.')
  bound_tasks=$(jq -c '[.turns[].toolAttempts[] | .callbackBinding.externalTaskId // empty] | sort' <<<"$trace")
  unbased_reviews=$(jq '(.facts.items | map(select(.factType == "image_generated") | {key: .id, value: .subject}) | from_entries) as $gen
      | [.facts.items[] | select(.factType == "asset_reviewed" and ($gen[.basisFactId // ""] != .subject))] | length' <<<"$trace")
  check run_completed "status=$run_status" test "$run_status" = COMPLETED
  caption_accepted() {
    jq -e '.accepted == true and .passedCount == 3 and .photoCount == 3
      and (.policyVersion | type == "string" and length > 0)
      and (.settingsDigest | type == "string" and length > 0)' <<<"$caption" >/dev/null
  }
  check media_result_accepted "$(jq -c '{accepted, passedCount, photoCount, policyVersion, settingsDigest}' <<<"$caption")" \
    caption_accepted
  check ledger_facts "image_generated=$(jq 'length' <<<"$generated_ledger") asset_reviewed=$reviewed_count reviews_not_based_on_their_generation=$unbased_reviews" \
    test "$(jq 'length' <<<"$generated_ledger")" -eq 6 -a "$reviewed_count" -eq 6 -a "$unbased_reviews" -eq 0
  check generation_budget "used=$used limit=$max_calls" test "$used" -eq 6
  check record_generated_matches_ledger "record=$generated_count ledger=$(jq 'length' <<<"$generated_ledger")" \
    test "$generated_record" = "$generated_ledger" -a "$generated_count" -eq 6
  check record_video_matches_trace "record=$video_record trace=$bound_tasks" \
    test "$video_record" = "$bound_tasks" -a "$video_count" -eq 1
  summary="accepted=$(jq -r '.accepted' <<<"$caption") passed=$(jq -r '.passedCount' <<<"$caption")/$(jq -r '.photoCount' <<<"$caption") policy=$(jq -r '.policyVersion' <<<"$caption") budget=$used/$max_calls generated=$generated_count reviewed=$reviewed_count video_tasks=$video_count"
  ;;
gate)
  check run_failed "status=$run_status" test "$run_status" = FAILED
  check video_rejected_at_claim "termination=$termination code=$rejected_code" \
    test "$termination" = INVALID_ACTION -a "$rejected_code" = PRECONDITION_UNMET
  check no_video_dispatched "record video tasks=$video_count" test "$video_count" -eq 0
  summary="termination=$termination code=$rejected_code generated=$generated_count video_tasks=$video_count"
  ;;
limit)
  check run_failed "status=$run_status" test "$run_status" = FAILED
  check third_generation_rejected "termination=$termination code=$rejected_code" \
    test "$rejected_code" = GENERATION_LIMIT_REACHED
  check generation_budget "used=$used limit=$max_calls" test "$used" -eq 2 -a "$max_calls" = 2
  check record_generated_two "record generated=$generated_count" test "$generated_count" -eq 2
  summary="code=$rejected_code budget=$used/$max_calls generated=$generated_count video_tasks=$video_count"
  ;;
esac

if [[ -n "${DEMO_STATE:-}" ]]; then
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRECORD_AFTER=%s\nRUN_STATUS=%s\nSUMMARY=%s\n' \
    "$run_id" "$workflow_id" "$record_after_seq" "$run_status" "$summary" >"$DEMO_STATE"
fi

printf '\n'
if ((failures > 0)); then
  printf 'photo-set %s story: %d check(s) FAILED\n' "$story" "$failures"
  exit 1
fi
printf 'photo-set %s story: all checks passed\n' "$story"
