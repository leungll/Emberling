#!/usr/bin/env bash
# Collects the evidence of finished video-editing Runs through the public API alone and
# writes a one-page Markdown report of it. Nothing here reads the database, the media
# service or process memory: every line of the report is traceable to a response of the
# Backend's REST API, saved beside it.
#
# Usage: scripts/demo/video-evidence-report.sh [--out DIR] [--file FILE] [RUN_ID...]
#
#   RUN_ID...    the Runs to report, in the order given.
#   --file FILE  reads more Run ids from FILE, one per line; blank lines and lines
#                starting with # are skipped.
#   --out DIR    where to write (default deploy/.demo/video-evidence, not tracked).
#
# Per Run, DIR/runs/RUN_ID/ receives what the API served: snapshot.json (the Run
# Snapshot), trace.json (the Agent Trace), events.json (the committed Event ledger, paged
# by seq), definition.json (the Definition version the Run is bound to), and, split out of
# the Trace for reading: completion.json (the goal-based completion verdict), facts.json
# (the execution facts in recorded order) and manifests/turn-N.json (the Context Manifest
# of each Turn). DIR/report.md then summarises every Run: how the Agent's decisions were
# made (a scripted trajectory or a real model, read from the Definition), its outcome and
# completion checks, its Decisions, its Tool Attempts with the media task id and status of
# each and the path that completed it (callback or Provider poll), its facts in order with
# their verdicts and bindings, the revisions it counted against each limit, the cost its
# model calls reported, and the digest and URL of the delivered clip; a batch table opens
# the report. The clip URL is composed from EMBERLING_DEMO_MEDIA_HOST_URL (default
# http://localhost:9201); it is not probed.
#
# The report prints identifiers, statuses, counts, costs and digests only, never a Tool
# input or result, a request body or a header. Callback tokens are never served by the
# public API; the script still refuses to finish when any saved file names one or when
# the report mentions a token at all.
#
# Exit status 1 when a Run cannot be read or the report fails that check.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools curl jq

usage="usage: video-evidence-report.sh [--out DIR] [--file FILE] [RUN_ID...]"
MEDIA_HOST_URL=${EMBERLING_DEMO_MEDIA_HOST_URL:-http://localhost:9201}

out_dir="$DEMO_ROOT/deploy/.demo/video-evidence"
run_ids=()
while (($# > 0)); do
  case "$1" in
  --out)
    (($# >= 2)) || die "$usage"
    out_dir=$2
    shift 2
    ;;
  --file)
    (($# >= 2)) || die "$usage"
    [[ -f "$2" ]] || die "$2 does not exist"
    while IFS= read -r line; do
      line=${line%%#*}
      line=${line//[[:space:]]/}
      [[ -n "$line" ]] && run_ids+=("$line")
    done <"$2"
    shift 2
    ;;
  -*) die "$usage" ;;
  *)
    run_ids+=("$1")
    shift
    ;;
  esac
done
((${#run_ids[@]} > 0)) || die "$usage (no Run id given)"

# collect RUN_ID saves the API's answers about one Run under the output directory and
# prints the path of the file that joins them for the report.
collect() {
  local run_id=$1 dir snapshot trace events agent_node definition workflow_id version turn_no
  [[ "$run_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $run_id"
  dir="$out_dir/runs/$run_id"
  mkdir -p "$dir/manifests"
  snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id") || die "run $run_id cannot be read from $BACKEND_URL"
  printf '%s\n' "$snapshot" | jq '.' >"$dir/snapshot.json"
  events=$(run_events "$run_id")
  printf '%s\n' "$events" | jq '.' >"$dir/events.json"
  workflow_id=$(jq -r '.run.workflowId' <<<"$snapshot")
  version=$(jq -r '.run.definitionVersion' <<<"$snapshot")
  definition=$(curl -fsS "$BACKEND_URL/api/definitions/$workflow_id/versions/$version")
  printf '%s\n' "$definition" | jq '.' >"$dir/definition.json"
  # The video-editing Definitions have one Agent node; a Run without one gets an empty
  # Trace and the report says so.
  agent_node=$(jq -r '[.nodeRuns[] | select(.nodeType == "agent") | .id] | first // empty' <<<"$snapshot")
  if [[ -n "$agent_node" ]]; then
    trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$agent_node/agent")
  else
    trace=null
  fi
  printf '%s\n' "$trace" | jq '.' >"$dir/trace.json"
  jq '.agentRun.completion' <<<"$trace" >"$dir/completion.json"
  jq '.facts // {items: [], truncated: false}' <<<"$trace" >"$dir/facts.json"
  while IFS= read -r turn_no; do
    [[ -n "$turn_no" ]] || continue
    jq --argjson n "$turn_no" '.turns[] | select(.turnNo == $n) | {turnNo, id, manifest}' <<<"$trace" >"$dir/manifests/turn-$turn_no.json"
  done < <(jq -r '.turns[]?.turnNo' <<<"$trace")
  jq -n --arg runId "$run_id" --arg media "$MEDIA_HOST_URL" \
    --slurpfile snapshot "$dir/snapshot.json" --slurpfile trace "$dir/trace.json" \
    --slurpfile events "$dir/events.json" --slurpfile definition "$dir/definition.json" \
    '{runId: $runId, mediaUrl: $media, snapshot: $snapshot[0], trace: $trace[0], events: $events[0], definition: $definition[0]}' >"$dir/run.json"
  printf '%s\n' "$dir/run.json"
}

render_program() {
  cat <<'JQ'
def short: if type == "string" and test("^[0-9a-f]{40,}$") then .[0:12] else . end;
def cell: if . == null then "-" else tostring | gsub("\\|"; "/") | gsub("\n"; " ") end;
def row: "| " + (map(cell) | join(" | ")) + " |";
def joined: if length == 0 then "-" else join(", ") end;
def yesno: if . == true then "yes" elif . == false then "no" else "-" end;
def ts: if . == null then "-" else . end;

# bindings_text renders a fact's bindings as key=value pairs with digests shortened.
def bindings_text: if . == null then "-" else to_entries | map("\(.key)=\(.value | short | tostring)") | joined end;

# agent_label says how the Run's Agent decided, from the Definition it is bound to.
def agent_label($def):
  ([$def.nodes[]? | select(.type == "agent")] | first) as $node
  | if $node == null then "no Agent node"
    elif ($node.config.modelConfig.script // null) != null
      then "deterministic decisions (scripted mock model, trajectory \($node.config.modelConfig.script))"
    else "real model \($node.config.modelId // "unknown")" end;

# cost_totals sums the costs the Turns and Tool Attempts report, per currency.
def cost_totals($trace):
  ([$trace.turns[]? | .cost, (.toolAttempts[]? | .cost)] | map(select(. != null)))
  | group_by(.currency) | map("\(map(.amount | tonumber) | add) \(.[0].currency)") | joined;

def completion_of($trace): $trace.agentRun.completion;

# completions_by_attempt maps a Tool Attempt id to the source of the Event that ended its
# Action: CALLBACK or PROVIDER_POLL for an asynchronous Tool, SYNC_EXECUTION otherwise.
def completions_by_attempt($events):
  [$events[] | select((.type == "AGENT_ACTION_COMPLETED" or .type == "AGENT_ACTION_FAILED") and .payload.toolAttemptId != null)
    | {key: .payload.toolAttemptId, value: "\(if .type == "AGENT_ACTION_COMPLETED" then "completed" else "failed" end) by \(.payload.completionSource // .payload.failureSource // "-")"}]
  | from_entries;

def revisions($trace):
  ($trace.agentRun.maxFailedFacts // {}) as $limits
  | ([$trace.facts.items[]? | select(.verdict == false) | .factType] | group_by(.) | map({key: .[0], value: length}) | from_entries) as $failed
  | (($limits | keys) + ($failed | keys) | unique)
  | map("\(.): \($failed[.] // 0) failed\(if $limits[.] != null then " of \($limits[.]) allowed" else "" end)") | joined;

def clip_of($trace): ([$trace.facts.items[]? | select(.factType == "clip_rendered") | .subject] | last) // null;

def run_section($r):
  $r.trace as $trace | $r.events as $events | $r.snapshot as $snapshot
  | completions_by_attempt($events) as $sources
  | completion_of($trace) as $c
  | clip_of($trace) as $clip
  | ([$events[] | select(.type == "AGENT_DECISION_COMMITTED")] | length) as $decisions
  | [$trace.turns[]? | . as $t | .toolAttempts[]? | . + {turnNo: $t.turnNo}] as $attempts
  | ([$events[] | .seq] | (. == sort and length == (unique | length))) as $ordered
  | ["## Run \($r.runId)", "",
     "- Agent: \(agent_label($r.definition))",
     "- Definition: \($snapshot.run.workflowId) version \($snapshot.run.definitionVersion)",
     "- Run status: \($snapshot.run.status); Agent Run termination: \($trace.agentRun.termination // "-")\(if $trace.agentRun.error then " (\($trace.agentRun.error.code))" else "" end)",
     "- Outcome: \($c.outcome // "not evaluated")" +
       (if $c then "; checks: " + ([$c.checks[] | "\(.name) \(if .passed then "passed" else "failed" end)"] | joined)
          + (if ($c.missing | length) > 0 then "; missing: " + ([$c.missing[] | .code] | joined) else "" end) else "" end),
     "- Committed Decisions: \($decisions) in \($trace.turns | length) Turn(s); Event ledger: \($events | length) Events, seq \(if $ordered then "strictly increasing without a duplicate" else "NOT in order" end)",
     "- Revisions counted: \(revisions($trace))",
     "- Cost reported by model calls: \(cost_totals($trace))",
     "- Clip: \(if $clip then "\($clip) at \($r.mediaUrl)/v1/clips/\($clip)" else "none delivered" end)",
     "", "### Decisions", "",
     "| Turn | Decision | Tool | Action | Claimed by | Cost | Manifest items (included/omitted) |", "|---|---|---|---|---|---|---|"]
  + [$trace.turns[]? | [.turnNo, (.decision.kind // .status), .decision.tool, (.action.status // "-"), .claimSource,
       (if .cost then "\(.cost.amount) \(.cost.currency)" else null end),
       (if .manifest then "\([.manifest[] | select(.included)] | length)/\([.manifest[] | select(.included | not)] | length)" else null end)] | row]
  + ["", "### Tool Attempts", "",
     "| Turn | Tool | Attempt | Status | Media task | Ended | Error |", "|---|---|---|---|---|---|---|"]
  + (if ($attempts | length) == 0 then ["| - | - | - | - | - | - | - |"]
     else [$attempts[] | [.turnNo, .toolName, .attemptNo, .status, (.callbackBinding.externalTaskId // null), ($sources[.id] // "-"), (.error.code // null)] | row] end)
  + ["", "### Facts in recorded order", "",
     "| # | Type | Subject | Verdict | Bindings | Tool Attempt |", "|---|---|---|---|---|---|"]
  + (if (($trace.facts.items // []) | length) == 0 then ["| - | - | - | - | - | - |"]
     else [$trace.facts.items | to_entries[] | [.key + 1, .value.factType, (.value.subject | short), (.value.verdict | yesno), (.value.bindings | bindings_text), .value.toolAttemptId] | row] end)
  + (if $trace.facts.truncated then ["", "The fact ledger holds more facts than the Trace shows."] else [] end)
  + [""];

$runs[0] as $runs
| ["# Video-editing demo evidence", "",
   "Generated \($generated) from \($runs | length) Run(s), read through the public API; the responses are saved under runs/.", "",
   "| Run | Agent | Status | Outcome | Decisions | Tool Attempts | Facts | Revisions | Cost | Clip |", "|---|---|---|---|---|---|---|---|---|---|"]
  + [$runs[] | [.runId, agent_label(.definition), .snapshot.run.status, (completion_of(.trace).outcome // "not evaluated"),
       ([.events[] | select(.type == "AGENT_DECISION_COMMITTED")] | length),
       ([.trace.turns[]? | .toolAttempts[]?] | length),
       ((.trace.facts.items // []) | length), revisions(.trace), cost_totals(.trace), (clip_of(.trace) | short)] | row]
  + [""]
  + [$runs[] | run_section(.)[]]
  | .[]
JQ
}

mkdir -p "$out_dir"
run_files=()
for run_id in "${run_ids[@]}"; do
  log "collecting run $run_id"
  run_files+=("$(collect "$run_id")")
done

report="$out_dir/report.md"
jq -r -n --arg generated "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --slurpfile runs <(jq -s '.' "${run_files[@]}") -f <(render_program) >"$report"

# The public API never serves a callback token or a signing secret; this guards the
# report against a projection that would start to.
if grep -rliE 'callbackToken|signingSecret|X-Emberling-Callback-Token' "$out_dir/runs" >/dev/null; then
  die "a saved API response names a callback token or signing secret under $out_dir/runs"
fi
if grep -qi 'token' "$report"; then
  die "the report mentions a token; nothing it prints may"
fi
printf 'evidence report written to %s (%d run(s))\n' "$report" "${#run_ids[@]}"
