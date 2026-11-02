#!/usr/bin/env bash
# Summarises the demo's evidence in two parts that rest on different grounds.
#
#   Fault injection   Runs served by the Mock Provider. Their Event ledgers are reconciled
#                     task by task against the Provider's own request record by
#                     ledger-vs-record.sh, and optionally each Run's committed rows are
#                     checked by invariants.sh. Reported: tasks bound vs created, merged
#                     duplicate submissions, duplicate generations, lost submissions,
#                     takeovers, callback- and poll-completed Nodes, and the Provider's
#                     in-flight peak against its limit.
#   Real flow         Runs served by a real model. Reported per Definition version:
#                     Runs, completed, failed, duration and the cost the Provider
#                     reported, all read from the Emberling ledger through the public
#                     API; and per tuning Run, the uncertain results handed to the Agent
#                     and the generation calls they counted against the budget. No
#                     Provider record exists for this part, so nothing here claims
#                     item-by-item agreement with the Provider.
#
# Usage: scripts/demo/evidence-report.sh [OPTION]...
#
#   --fault RUN_IDS RECORD_AFTER_SEQ [SUBMISSIONS]
#                      one fault-injection variant: its Runs (comma-separated), the
#                      Provider record seq before they started and, when known, how many
#                      POST /api/runs the clients sent. Repeatable.
#   --invariants       also run invariants.sh for every --fault Run (needs the Compose
#                      stack's postgres container).
#   --real WORKFLOW_ID the serving Workflow whose versions are compared. Repeatable.
#   --tuning RUN_ID    a tuning Run whose Agent Trace is summarised. Repeatable.
#
# Exit status 1 when any fault-injection check failed; the real part never fails the
# report, it only states what the ledger holds.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools curl jq

faults=()
real_workflows=()
tuning_runs=()
invariants=0
while (($# > 0)); do
  case "$1" in
  --fault)
    (($# >= 3)) || die "--fault needs RUN_IDS and RECORD_AFTER_SEQ"
    submissions=""
    if (($# >= 4)) && [[ "$4" =~ ^[0-9]+$ ]]; then
      submissions=$4
      faults+=("$2 $3 $submissions")
      shift 4
    else
      faults+=("$2 $3 ")
      shift 3
    fi
    ;;
  --invariants) invariants=1; shift ;;
  --real)
    (($# >= 2)) || die "--real needs WORKFLOW_ID"
    [[ "$2" =~ ^[A-Za-z0-9_-]+$ ]] || die "workflow id has unexpected characters: $2"
    real_workflows+=("$2")
    shift 2
    ;;
  --tuning)
    (($# >= 2)) || die "--tuning needs RUN_ID"
    [[ "$2" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $2"
    tuning_runs+=("$2")
    shift 2
    ;;
  *) die "unknown option $1; see the header of evidence-report.sh" ;;
  esac
done
((${#faults[@]} + ${#real_workflows[@]} + ${#tuning_runs[@]} > 0)) || die "nothing to report; give --fault, --real or --tuning"

failed_checks=0

# summary_value REPORT KEY prints the value of one line of ledger-vs-record.sh's serving
# summary, whose keys are printed left-aligned in a 34-character column after two spaces.
summary_value() {
  awk -v k="  $2" 'index($0, k) == 1 {print substr($0, 38); exit}' <<<"$1"
}

if ((${#faults[@]} > 0)); then
  printf '== Fault injection: Emberling ledger reconciled with the Mock Provider record ==\n\n'
  printf '%-4s %-6s %-14s %-7s %-10s %-6s %-9s %-14s %-12s %-6s\n' \
    '#' runs 'bound/created' merged dup-gen lost takeovers 'callback/poll' 'peak/limit' checks
  n=0
  for entry in "${faults[@]}"; do
    n=$((n + 1))
    read -r run_ids record_after submissions <<<"$entry"
    report=$(LEDGER_SUBMISSIONS=$submissions LEDGER_REQUIRE_COMPLETED=1 \
      "$DEMO_ROOT/scripts/demo/ledger-vs-record.sh" "$run_ids" "$record_after" 2>&1) || true
    passes=$(grep -c '^PASS ' <<<"$report" || true)
    fails=$(grep -c '^FAIL ' <<<"$report" || true)
    failed_checks=$((failed_checks + fails))
    if ((invariants == 1)); then
      inv=$("$DEMO_ROOT/scripts/demo/invariants.sh" "$run_ids" 2>&1) || true
      inv_fails=$(grep -c '^FAIL ' <<<"$inv" || true)
      failed_checks=$((failed_checks + inv_fails))
      checks="ledger $passes/$((passes + fails)), invariants $(grep -c '^PASS ' <<<"$inv" || true)/$(($(grep -c '^PASS ' <<<"$inv" || true) + inv_fails))"
    else
      checks="ledger $passes/$((passes + fails))"
    fi
    runs=$(summary_value "$report" "runs")
    printf '%-4s %-6s %-14s %-7s %-10s %-6s %-9s %-14s %-12s %s\n' "$n" \
      "${runs%% *}" "$(summary_value "$report" "tasks bound / tasks created" | tr -d ' ')" \
      "$(summary_value "$report" "merged duplicate submissions" | cut -d' ' -f1)" \
      "$(summary_value "$report" "duplicate generations")" "$(summary_value "$report" "lost submissions")" \
      "$(summary_value "$report" "takeovers (replayed dispatches)" | cut -d' ' -f1)" \
      "$(summary_value "$report" "callback-completed / poll-completed" | tr -d ' ')" \
      "$(summary_value "$report" "Provider in-flight peak / limit" | tr -d ' ')" "$checks"
    if ((fails > 0)); then
      grep '^FAIL ' <<<"$report" | sed 's/^/     /'
    fi
    if ((invariants == 1)) && grep -q '^FAIL ' <<<"$inv"; then
      grep '^FAIL ' <<<"$inv" | sed 's/^/     /'
    fi
  done
  printf '\nGround: every bound external task is matched against the Mock Provider'"'"'s request record; a check\n'
  printf 'counts only when the record and the ledger agree task by task (ledger-vs-record.sh).\n\n'
fi

# list_version_runs WORKFLOW_ID VERSION prints every Run list item of that version as a
# JSON array, following the server's cursors.
list_version_runs() {
  local wf=$1 v=$2 cursor="" page items='[]'
  while :; do
    page=$(curl -fsS "$BACKEND_URL/api/runs?workflowId=$wf&definitionVersion=$v&limit=100${cursor:+&cursor=$cursor}")
    items=$(jq -s 'add' <(printf '%s' "$items") <(jq '.items' <<<"$page"))
    cursor=$(jq -r '.nextCursor // empty' <<<"$page")
    [[ -n "$cursor" ]] || break
  done
  printf '%s\n' "$items"
}

if ((${#real_workflows[@]} > 0)); then
  printf '== Real flow: per Definition version, from the Emberling ledger and the Provider-reported cost ==\n\n'
  for wf in "${real_workflows[@]}"; do
    latest=$(curl -fsS "$BACKEND_URL/api/definitions/$wf" | jq -r '.version') || die "workflow $wf could not be read"
    name=$(curl -fsS "$BACKEND_URL/api/definitions/$wf" | jq -r '.name')
    printf 'workflow %s (%s), %s version(s)\n' "$wf" "$name" "$latest"
    printf '%-8s %-5s %-9s %-6s %-9s %-13s %-13s %-24s %s\n' version runs completed failed in-flight 'avg duration' 'max duration' cost 'not priced'
    for ((v = 1; v <= latest; v++)); do
      items=$(list_version_runs "$wf" "$v")
      total=$(jq 'length' <<<"$items")
      ((total > 0)) || continue
      # Durations are whole seconds: the timestamps' fractions are dropped before parsing,
      # and a numeric UTC offset is applied by hand because jq parses only "Z" itself.
      stats=$(jq -r '
        def secs: sub("\\.[0-9]+"; "")
          | if endswith("Z") then fromdateiso8601
            else capture("^(?<base>.*T[0-9:]+)(?<sign>[+-])(?<h>[0-9]{2}):(?<m>[0-9]{2})$")
              | (.base | strptime("%Y-%m-%dT%H:%M:%S") | mktime)
                - (if .sign == "+" then 1 else -1 end) * ((.h | tonumber) * 3600 + (.m | tonumber) * 60)
            end;
        [.[] | select(.completedAt != null) | ((.completedAt | secs) - (.startedAt | secs))] as $d
        | [
            ([.[] | select(.status == "COMPLETED")] | length),
            ([.[] | select(.status == "FAILED")] | length),
            ([.[] | select(.status == "RUNNING" or .status == "PAUSED")] | length),
            (if ($d | length) > 0 then (($d | add) / ($d | length) | . * 10 | round / 10 | tostring) + "s" else "-" end),
            (if ($d | length) > 0 then ($d | max | tostring) + "s" else "-" end),
            ([.[].cost.totals[]] | group_by(.currency)
              | map("\(map(.amount | tonumber) | add | . * 1000000 | round / 1000000) \(.[0].currency)") | join(", ")
              | if . == "" then "-" else . end),
            ([.[].cost.unknownCalls] | add)
          ] | @tsv' <<<"$items")
      IFS=$'\t' read -r completed failed inflight avg max cost unknown <<<"$stats"
      printf '%-8s %-5s %-9s %-6s %-9s %-13s %-13s %-24s %s\n' "v$v" "$total" "$completed" "$failed" "$inflight" "$avg" "$max" "$cost" "$unknown"
    done
    printf 'drill down: %s/api/runs?workflowId=%s&definitionVersion=<v>   Studio: %s/runs?wf=%s&v=<v>\n\n' \
      "$BACKEND_URL" "$wf" "$STUDIO_URL" "$wf"
  done
fi

if ((${#tuning_runs[@]} > 0)); then
  printf '== Real flow: tuning Runs, from the Agent Trace ==\n\n'
  printf '%-28s %-10s %-18s %-18s %-24s %s\n' run status 'uncertain results' 'generation calls' cost 'not priced'
  for run_id in "${tuning_runs[@]}"; do
    snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id") || die "run $run_id could not be read"
    status=$(jq -r '.run.status' <<<"$snapshot")
    agent_node=$(jq -r '[.nodeRuns[] | select(.nodeType == "agent")] | first | .id // empty' <<<"$snapshot")
    uncertain="-" calls="-"
    if [[ -n "$agent_node" ]]; then
      trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$agent_node/agent")
      # An uncertain result reaches the Agent as an Action that failed with
      # RESULT_UNCERTAIN; each one is counted against the generation budget.
      uncertain=$(jq '[.turns[] | select(.action.error.code == "RESULT_UNCERTAIN")] | length' <<<"$trace")
      calls=$(jq -r '"\(.generationBudget.generationCallsUsed) of \(.generationBudget.maxGenerationCalls // "unlimited")"' <<<"$trace")
    fi
    item=$(curl -fsS "$BACKEND_URL/api/runs?code=$run_id&limit=1" | jq '.items[0] // {}')
    cost=$(jq -r '[.cost.totals[]? | "\(.amount) \(.currency)"] | join(", ") | if . == "" then "-" else . end' <<<"$item")
    unknown=$(jq -r '.cost.unknownCalls // "-"' <<<"$item")
    printf '%-28s %-10s %-18s %-18s %-24s %s\n' "$run_id" "$status" "$uncertain" "$calls" "$cost" "$unknown"
  done
  printf '\n'
fi

if ((${#real_workflows[@]} + ${#tuning_runs[@]} > 0)); then
  printf 'Ground: the real-flow figures are the Emberling ledger (Run status, timestamps, Agent Trace) and the\n'
  printf 'cost each Provider reported with its result. No item-by-item agreement with the Provider'"'"'s own records\n'
  printf 'is claimed for this part.\n'
fi

if ((failed_checks > 0)); then
  printf '\nevidence report: %d fault-injection check(s) FAILED\n' "$failed_checks"
  exit 1
fi
