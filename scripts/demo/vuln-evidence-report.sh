#!/usr/bin/env bash
# Builds the one-page evidence report of the vulnerability-repair demo. Each conclusion
# names the Runs it comes from (Run ID and variant) and the records it was checked against:
# the Emberling ledger read through the public API, the sandbox runner's own request record
# and the mock production service's own request record.
#
# Usage:
#   vuln-evidence-report.sh collect DATA_DIR VARIANT RUN_IDS STORY INVARIANTS LEDGER [LOG_DIR]
#       saves what the report needs about one finished variant: each Run's Snapshot status,
#       committed Events and Agent Traces, the verdicts of its story checks, invariant
#       report and ledger (PASS, FAIL or -), and, when LOG_DIR holds story.log,
#       invariants.log or ledger.log, the names of the checks each reported. The runner and
#       production records are saved whole each time, so the last collect holds them all.
#   vuln-evidence-report.sh render DATA_DIR OUT_FILE
#       writes the Markdown report from the collected data alone, without the stack.
#   vuln-evidence-report.sh report OUT_FILE VARIANT=RUN_ID[,RUN_ID...]...
#       collects the named Runs, with no verdicts, into a temporary directory and renders
#       them, for a report made by hand.
#
# make demo collects after every vuln variant and renders after the last one.
#
# The report covers five aspects: authorization, evidence consistency, completion, release
# reconciliation and execution recovery. An aspect PASSes when every variant it rests on
# ran with all its checks passed and the cross-checks made here from the collected records
# hold; it is NOT VERIFIED when one of those variants did not run, and FAILs otherwise.
# Versions production confirmed live only after a person stopped waiting are listed in a
# section of their own, marked as never checked.
#
# Only identifiers, statuses, check names, versions and shortened digests are printed. The
# report never prints a request body, a header, a Tool input or result, or a whole record
# line, so no credential, token or internal storage key can reach it.
#
# Exit status 1 when any aspect FAILs.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools jq

usage="usage: vuln-evidence-report.sh collect DATA_DIR VARIANT RUN_IDS STORY INVARIANTS LEDGER [LOG_DIR] | render DATA_DIR OUT_FILE | report OUT_FILE VARIANT=RUN_IDS..."

# checks_of LOG prints the PASS and FAIL lines of a check report as [{name, passed}].
checks_of() {
  if [[ -f "$1" ]]; then
    sed -n -E 's/^(PASS|FAIL) +([A-Za-z0-9_.:-]+).*/\1 \2/p' "$1" |
      jq -R -s 'split("\n") | map(select(length > 0) | split(" ") | {name: .[1], passed: (.[0] == "PASS")})'
  else
    printf '[]'
  fi
}

verdict_word() {
  case "$1" in
  PASS | FAIL | -) printf '%s' "$1" ;;
  0) printf 'PASS' ;;
  *) [[ "$1" =~ ^[0-9]+$ ]] && printf 'FAIL' || die "a verdict must be PASS, FAIL, - or an exit status: $1" ;;
  esac
}

collect() {
  local data_dir=$1 variant=$2 run_arg=$3 story invariants ledger log_dir=${7:-}
  story=$(verdict_word "$4")
  invariants=$(verdict_word "$5")
  ledger=$(verdict_word "$6")
  require_tools curl
  [[ "$variant" =~ ^[A-Za-z0-9_-]+$ ]] || die "variant has unexpected characters: $variant"
  local run_ids run_id runs='[]' snapshot events traces node_run_id order agent=scripted
  IFS=, read -r -a run_ids <<<"$run_arg"
  ((${#run_ids[@]} > 0)) || die "no run id given for $variant"
  mkdir -p "$data_dir/variants"
  for run_id in "${run_ids[@]}"; do
    [[ "$run_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $run_id"
    snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
    events=$(run_events "$run_id")
    traces='[]'
    while IFS= read -r node_run_id; do
      [[ -n "$node_run_id" ]] || continue
      traces=$(jq -s 'add' <(printf '%s' "$traces") \
        <(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$node_run_id/agent" | jq '[.]'))
    done < <(jq -r '.nodeRuns[] | select(.nodeType == "agent") | .id' <<<"$snapshot")
    runs=$(jq --arg id "$run_id" --arg status "$(jq -r '.run.status' <<<"$snapshot")" \
      --slurpfile events <(printf '%s' "$events") --slurpfile traces <(printf '%s' "$traces") \
      '. + [{runId: $id, status: $status, traces: $traces[0], events: $events[0]}]' <<<"$runs")
  done
  if [[ -n "${EMBERLING_DEMO_MODEL:-}" && ("$variant" == vuln-verify || "$variant" == vuln-main) ]]; then
    agent="model ${EMBERLING_DEMO_MODEL}"
  fi
  if [[ -f "$data_dir/variants/$variant.json" ]]; then
    order=$(jq '.order' "$data_dir/variants/$variant.json")
  else
    order=$(find "$data_dir/variants" -name '*.json' | wc -l | tr -d ' ')
  fi
  jq -n --arg variant "$variant" --argjson order "$order" --arg agent "$agent" --arg ids "$run_arg" \
    --arg story "$story" --arg invariants "$invariants" --arg ledger "$ledger" \
    --argjson storyChecks "$(checks_of "${log_dir:+$log_dir/story.log}")" \
    --argjson invariantChecks "$(checks_of "${log_dir:+$log_dir/invariants.log}")" \
    --argjson ledgerChecks "$(checks_of "${log_dir:+$log_dir/ledger.log}")" \
    --slurpfile runs <(printf '%s' "$runs") '{
      variant: $variant, order: $order, agent: $agent, runIds: ($ids | split(",")),
      verdicts: {story: $story, invariants: $invariants, ledger: $ledger},
      checks: {story: $storyChecks, invariants: $invariantChecks, ledger: $ledgerChecks},
      runs: $runs[0]}' >"$data_dir/variants/$variant.json"
  record_after 0 "$PRODUCTION_URL" | jq -s '.' >"$data_dir/production-record.json"
  record_after 0 "$RUNNER_URL" | jq -s '.' >"$data_dir/runner-record.json"
}

render() {
  local data_dir=$1 out_file=$2 rendered
  compgen -G "$data_dir/variants/*.json" >/dev/null || die "no variant was collected under $data_dir"
  local production="$data_dir/production-record.json" runner="$data_dir/runner-record.json"
  [[ -f "$production" ]] || production=/dev/null
  [[ -f "$runner" ]] || runner=/dev/null
  rendered=$(jq -n -r --slurpfile variants <(jq -s 'sort_by(.order)' "$data_dir"/variants/*.json) \
    --slurpfile production <(jq -s 'add // []' "$production") --slurpfile runner <(jq -s 'add // []' "$runner") \
    --arg generated "$(date -u +%Y-%m-%dT%H:%M:%SZ)" -f <(render_program))
  mkdir -p "$(dirname "$out_file")"
  printf '%s\n' "${rendered%$'\n'*}" >"$out_file"
  printf 'evidence report written to %s\n' "$out_file"
  # The program's last line is the overall verdict.
  [[ "${rendered##*$'\n'}" == "OVERALL PASS" ]]
}

render_program() {
  cat <<'JQ'
def short: if type == "string" and test("^[0-9a-f]{40,}$") then .[0:12] else . end;
def cell: if . == null then "-" else tostring | gsub("\\|"; "/") | gsub("\n"; " ") end;
def row: "| " + (map(cell) | join(" | ")) + " |";
def joined: if length == 0 then "-" else join(", ") end;

$variants[0] as $variants | $production[0] as $production | $runner[0] as $runner
| ($variants | map(.variant)) as $ran
| [$variants[] | .variant as $v | .runs[] | . + {variant: $v}] as $runs
| [$production[] | select(.kind == "release" and (.operationId | type) == "string")] as $release_lines
| def lines_of($key; $event): [$release_lines[] | select(.operationId == $key and .event == $event)];
  def deployed_of($key): [lines_of($key; "deployed")[] | select(.replayed | not)];
  def actions_of($run): [$run.traces[].turns[] | select(.action != null) | .action + {attempts: (.toolAttempts // [])}];
  def release_actions_of($run): [actions_of($run)[] | select(.release != null)];
  def latest_patch($run): ([$run.traces[].workspace.patches // [] | .[]] | last | .patchDigest) // null;
  def facts_of($run): [$run.traces[].facts.items // [] | .[]];
  def verified($run; $digest): any(facts_of($run)[]; .factType == "patch_verified" and .subject == $digest and .verdict == true);
  def completion_of($run): ([$run.traces[].agentRun.completion | select(. != null)] | first) // null;
  def agent_run_of($run): ([$run.traces[].agentRun] | first) // {};
  def action_deployments($action): [$release_lines[] | select(.event == "deployed" and (.replayed | not)
      and (.operationId | startswith("op_\($action.id)_")))];
  def occupations: [$runs[] | . as $run | release_actions_of($run)[] | select(.release.occupation != null)
      | {run: $run.runId, variant: $run.variant, action: .id} + .release.occupation];

  # The verdicts of the variants an aspect rests on, and its conclusion.
  def sources($names): [$names[] | . as $n | ($variants | map(select(.variant == $n)) | first) // {variant: $n, missing: true}];
  def variant_ok: (.missing | not) and ([.verdicts[]] | all(. == "PASS" or . == "-"))
      and ([.checks[][]] | all(.passed));
  def conclusion($sources; $failures):
    if ($failures | length) > 0 or any($sources[]; (.missing | not) and (variant_ok | not)) then "FAIL"
    elif any($sources[]; .missing) then "NOT VERIFIED"
    else "PASS" end;
  def source_table($sources):
    ["| Variant | Runs | Agent | Story checks | Invariants | Ledger vs record | Checks |", "|---|---|---|---|---|---|---|"]
    + [$sources[] | if .missing then [.variant, "not run", "-", "-", "-", "-", "-"] | row
       else [.variant, (.runIds | joined), .agent, .verdicts.story, .verdicts.invariants, .verdicts.ledger,
             ([.checks[][]] | "\(map(select(.passed)) | length) of \(length) passed"
               + ([.[] | select(.passed | not) | .name] | if length > 0 then "; failed: " + join(", ") else "" end))] | row end];
  def aspect($title; $sources; $basis; $failures; $facts):
    conclusion($sources; $failures) as $c
    | {title: $title, conclusion: $c, lines: ([
        "## \($title)", "",
        "Conclusion: \($c)", "",
        "Checked against: \($basis)", ""]
      + source_table($sources)
      + (if ($facts | length) > 0 then [""] + $facts else [] end)
      + (if ($failures | length) > 0 then ["", "Failed cross-checks:"] + ($failures | map("- " + .)) else [] end)
      + (if $c == "NOT VERIFIED" then ["", "Not run in this demo: " + ([$sources[] | select(.missing) | .variant] | joined)] else [] end)
      + [""])};

  # Authorization.
  ([$runs[] | . as $run | actions_of($run)[] | {run: $run.runId, variant: $run.variant} + .]) as $actions
| ([$actions[] | select(.authorization == "DENY" or .failureSource == "AUTHORIZATION")]) as $denied
| ([$actions[] | select(.release != null)]) as $releases
| aspect("Authorization";
    sources(["vuln-main", "vuln-scope-denied", "vuln-target-occupied", "vuln-duplicate-approval", "vuln-reject-resubmit"]);
    "the Agent Trace's authorization decision, approval and Tool Attempts of every Action (Emberling); the sandbox runner's arrivals; production's release lines under each release Action";
    ([$denied[] | select((.attempts | length) > 0) | "\(.run) Action \(.id) was refused but has \(.attempts | length) Tool Attempt(s)"]
     + [$releases[] | select((action_deployments(.) | length) > 0 and .approval.status != "APPROVED")
        | "\(.run) Action \(.id) deployed without an APPROVED approval (approval \(.approval.status // "none"))"]
     + [$releases[] | select(.approval.status == "REJECTED" and ([$release_lines[] | select(.operationId | startswith("op_\(.id)_"))] | length) > 0)
        | "\(.run) Action \(.id) reached production although its approval was REJECTED"]);
    ["Actions with an authorization decision: \([$actions[] | select(.authorization != null)] | length) of \($actions | length).",
     "Refused Actions (none ran a Tool Attempt): " + ([$denied[] | "\(.run) \(.id) (\(.error.code // .authorization))"] | joined) + ".",
     "Release approvals: " + ([$releases[] | "\(.run) \(.id) \(.approval.status // "none")\(if .approval.decidedBy then " by " + .approval.decidedBy else "" end)"] | joined) + "."]) as $authorization

  # Evidence consistency.
| ([$runs[] | . as $run | latest_patch($run) as $latest | {
      run: $run.runId, variant: $run.variant, latest: $latest,
      latestVerified: (if $latest == null then null else verified($run; $latest) end),
      changeRequests: [$run.traces[].changeRequests.items // [] | .[]],
      facts: facts_of($run),
      deployed: [release_actions_of($run)[] | action_deployments(.)[] | {operationId, patchDigest, version}]}]) as $evidence
| aspect("Evidence consistency";
    sources(["vuln-verify", "vuln-stale-evidence", "vuln-main", "vuln-reject-resubmit", "vuln-incomplete-final"]);
    "the recorded patches, patch_verified facts and change requests in the Agent Trace (Emberling); the sandbox runner's verification record; the patch digest of each production deployment";
    ([$evidence[] | . as $e | .changeRequests[]
        | select(. as $cr | any($e.facts[]; .id == $cr.verificationFactId and .subject == $cr.patchDigest and .verdict == true) | not)
        | "\($e.run) change request \(.id) rests on no passing verification of patch \(.patchDigest | short)"]
     + [$evidence[] | . as $e | .deployed[]
        | select(.patchDigest != $e.latest or ($e.latestVerified | not))
        | "\($e.run) deployed patch \(.patchDigest | short) under \(.operationId), which is not the latest verified patch \($e.latest | short)"]);
    (["| Run | Variant | Latest patch | Latest verified | Change request patch | Deployed patch |", "|---|---|---|---|---|---|"]
     + [$evidence[] | [.run, .variant, (.latest | short), .latestVerified,
         ([.changeRequests[] | .patchDigest | short] | unique | joined),
         ([.deployed[] | "\(.patchDigest | short) as \(.version)"] | joined)] | row])) as $consistency

  # Completion.
| ([$runs[] | . as $run | completion_of($run) as $c | agent_run_of($run) as $a
    | ([release_actions_of($run)[] | .release.occupation | select(. != null)]) as $occ | {
      run: $run.runId, variant: $run.variant, status: $run.status, completion: $c,
      termination: $a.termination, error: $a.error.code,
      remaining: (
        (if $c == null then ["not evaluated: the Agent Run ended as \($a.termination // "unknown")\(if $a.error.code then " (" + $a.error.code + ")" else "" end)"]
         elif $c.outcome == "READY_FOR_APPROVAL" then ["the service owner approves the change request and schedules the release"]
         elif $c.outcome == "DEPLOYED" then []
         elif $c.outcome == "DEPLOYED_CHECK_FAILED" then ["the new version is live but failed its check: roll back by the change request's rollback plan"]
         elif $c.outcome == "RELEASE_UNKNOWN" then ["the release result was unknown when the task ended: the target stays occupied until post-task reconciliation gets a definite result"]
         else [$c.missing[] | "\(.code): \(.description)"] end)
        + [$occ[] | select(.status == "ACTIVE") | "target \(.targetKey) is still occupied under \(.operationKey)"]
        + [$occ[] | select(.deployedUncheckedVersion != null) | "version \(.deployedUncheckedVersion) went live after the stop and was never checked"])}]) as $results
| aspect("Completion";
    sources(["vuln-main", "vuln-prepare-only", "vuln-unhealthy", "vuln-incomplete-final"]);
    "the completion verdict of each Agent Run in the Agent Trace: its outcome, every check and what it still lacks (Emberling)";
    ([$results[] | select(.completion != null) | select((.completion.checks | length) == 0
        or (.completion.outcome == "INCOMPLETE" and (.completion.missing | length) == 0))
        | "\(.run) completion \(.completion.outcome) lists no checks or no missing facts"]);
    (["| Run | Variant | Run status | Task result | Checks | Remaining |", "|---|---|---|---|---|---|"]
     + [$results[] | [.run, .variant, .status, (.completion.outcome // "not evaluated"),
         ([.completion.checks // [] | .[] | "\(.name) \(if .passed then "passed" else "failed" end)"] | joined),
         (.remaining | if length == 0 then "none" else join("; ") end)] | row])) as $completion

  # Release reconciliation.
| ([$releases[] | select(.approval.status == "APPROVED") | {run, action: .id, deployments: (action_deployments(.) | length)}]) as $approved
| ([$runs[] | . as $run | release_actions_of($run)[] | . as $a | .release.operations[] | select(.outcome == "UNKNOWN")
    | {run: $run.runId, action: $a.id, operationKey, occupation: $a.release.occupation}]) as $unknown
| aspect("Release reconciliation";
    sources(["vuln-duplicate-approval", "vuln-release-crash-deployed", "vuln-release-crash-voided", "vuln-void-exhausted",
             "vuln-target-occupied", "vuln-release-refused", "vuln-release-unreachable-stop", "vuln-stop-then-deployed"]);
    "the release operations, target occupations and human waits in the Agent Trace (Emberling); production's arrived, deployed and voided lines for each operation key";
    ([$approved[] | select(.deployments > 1) | "\(.run) Action \(.action) was approved once and deployed \(.deployments) times"]
     + [$unknown[] | select(.occupation == null or .occupation.operationKey != .operationKey)
        | "\(.run) operation \(.operationKey) had an unknown result and no occupation follows it"]
     + [occupations[] | select(.status == "RELEASED" and .releaseReason == "DEPLOYED" and (deployed_of(.operationKey) | length) != 1)
        | "\(.run) occupation of \(.targetKey) was freed as DEPLOYED but production deployed \(deployed_of(.operationKey) | length) time(s) under \(.operationKey)"]
     + [occupations[] | select(.status == "RELEASED" and .releaseReason == "VOIDED"
          and ((lines_of(.operationKey; "voided") | length) == 0 or (deployed_of(.operationKey) | length) > 0))
        | "\(.run) occupation of \(.targetKey) was freed as VOIDED but production's record of \(.operationKey) does not show it voided and not deployed"]);
    (["Each approved Action deployed at most once: " + ([$approved[] | "\(.run) \(.action) \(.deployments)x"] | joined) + ".",
      "Operations with an unknown result and where they went: " + ([$unknown[] | "\(.run) \(.operationKey) -> " +
        (if .occupation == null then "nowhere"
         elif .occupation.status == "ACTIVE" then "target still occupied, post-task reconciliation pending"
         else "target freed as \(.occupation.releaseReason)" end)] | joined) + ".", "",
      "| Run | Variant | Target | Created by operation | Status | Freed as | Production record |", "|---|---|---|---|---|---|---|"]
     + [occupations[] | [.run, .variant, .targetKey, .operationKey, .status, .releaseReason,
         "arrived \(lines_of(.operationKey; "arrived") | length), deployed \(deployed_of(.operationKey) | length), voided \(lines_of(.operationKey; "voided") | length)"] | row])) as $reconciliation

  # Execution recovery.
| ([$runs[] | . as $run | $run.traces[].turns[] | .toolAttempts // [] | .[]
    | select(.callbackBinding.providerId == "sandboxrunner") | . as $t
    | (if .toolName == "verify_patch" then "verification" else "test" end) as $kind
    | (if $kind == "verification" then "verificationId" else "testId" end) as $idKey
    | .callbackBinding.externalTaskId as $id | {
      run: $run.runId, variant: $run.variant, id: $id,
      arrived: ([$runner[] | select(.event == "arrived" and .kind == $kind and .[$idKey] == $id)] | length),
      started: ([$runner[] | select(.event == "started" and .kind == $kind and .[$idKey] == $id)] | length),
      completed: ([$runner[] | select(.event == "completed" and .kind == $kind and .[$idKey] == $id)] | length),
      delivered: ([$runner[] | select(.event == "callback" and .testId == $id and .httpStatus >= 200 and .httpStatus < 300)] | length),
      completions: ([$run.events[] | select((.type == "AGENT_ACTION_COMPLETED" or .type == "AGENT_ACTION_FAILED")
          and ((.payload.completionSource // .payload.failureSource) == "CALLBACK") and .payload.toolAttemptId == $t.id)] | length)}]) as $executions
| aspect("Execution recovery";
    sources(["vuln-verify", "vuln-restart-during-verify", "vuln-restart-during-approval", "vuln-release-crash-deployed", "vuln-release-crash-voided"]);
    "the sandbox runner's arrived, started, completed and callback lines for each verification; the callback completions committed for its Tool Attempt (Emberling)";
    ([$executions[] | select(.arrived != 1 or .started != 1 or .completed != 1 or .completions > 1)
      | "\(.run) verification \(.id) arrived \(.arrived), started \(.started), completed \(.completed), completed the Action \(.completions) time(s)"]);
    (["Each verification ran exactly once at the runner, and a result delivered again completed its Tool Attempt at most once.", "",
      "| Run | Variant | Verification | Arrived | Started | Completed | 2xx deliveries | Callback completions |", "|---|---|---|---|---|---|---|---|"]
     + [$executions[] | [.run, .variant, .id, .arrived, .started, .completed, .delivered, .completions] | row]
     + [""] + ([$executions[] | select(.delivered > .completions)
        | "- \(.run) verification \(.id): \(.delivered - .completions) duplicate delivery(ies) answered without advancing the task."])
     + ["", "Resuming a Run's view after a disconnect adds no new behavior here; it is covered by existing tests:",
        "TestAPI_Stream_Reconnect_WithLastEventID_ResumesWithoutDuplicates and TestAPI_Stream_EventCommittedBetweenQueryAndWait_DeliveredOnceInOrder (Backend SSE contract), studio/src/api/sse.test.ts and runObserver.test.ts (Studio)."])) as $recovery

| [$authorization, $consistency, $completion, $reconciliation, $recovery] as $aspects
| ([occupations[] | select(.deployedUncheckedVersion != null)]) as $unchecked
| ([
    "# Vulnerability repair demo evidence", "",
    "Generated \($generated) from \($variants | length) variant(s) and \($runs | length) Run(s).", "",
    "| Aspect | Conclusion |", "|---|---|"]
   + [$aspects[] | [.title, .conclusion] | row]
   + ["", "## Variants", "",
      "| Variant | Runs | Run status | Agent | Story checks | Invariants | Ledger vs record |", "|---|---|---|---|---|---|---|"]
   + [$variants[] | . as $v | [.variant, (.runIds | joined), ([.runs[] | .status] | joined), .agent,
       .verdicts.story, .verdicts.invariants, .verdicts.ledger] | row]
   + [""]
   + [$aspects[] | .lines[]]
   + ["## Versions confirmed live only after a stop, never checked", "",
      "A person stopped waiting for these releases; post-task reconciliation later learned that production deployed them. The Agent never checked these versions, and the Run that made each release has ended.", ""]
   + (if ($unchecked | length) == 0 then ["None in this demo."]
      else ["| Run | Variant | Target | Operation | Version |", "|---|---|---|---|---|"]
        + [$unchecked[] | [.run, .variant, .targetKey, .operationKey, .deployedUncheckedVersion] | row] end)
   + ["", "## Not covered", "",
      "- An Agent Run that ends because its state patch or fact check failed after the release was DEPLOYED: no demo variant shows it. It is judged INCOMPLETE, missing the release confirmation and the FINAL, while the target is freed as DEPLOYED; covered by TestAgentCompletion_DeployedThenStatePatchFails_IncompleteWithoutReleaseConfirmation (Backend integration test)."]
   | .[]),
  (if any($aspects[]; .conclusion == "FAIL") then "OVERALL FAIL" else "OVERALL PASS" end)
JQ
}

cmd=${1:-}
case "$cmd" in
collect)
  (($# >= 7)) || die "$usage"
  collect "${@:2}"
  ;;
render)
  (($# == 3)) || die "$usage"
  render "$2" "$3"
  ;;
report)
  (($# >= 3)) || die "$usage"
  out_file=$2
  shift 2
  data_dir=$(mktemp -d "${TMPDIR:-/tmp}/emberling-evidence.XXXXXX")
  trap 'rm -rf "$data_dir"' EXIT
  for pair in "$@"; do
    [[ "$pair" == *=* ]] || die "$usage"
    collect "$data_dir" "${pair%%=*}" "${pair#*=}" - - -
  done
  render "$data_dir" "$out_file"
  ;;
*)
  die "$usage"
  ;;
esac
