#!/usr/bin/env bash
# Compares a Run's Event ledger, read through the Backend's public API, with what the Mock
# Provider itself observed in its append-only dispatch record.
#
# Usage: scripts/demo/ledger-vs-record.sh RUN_ID [RECORD_AFTER_SEQ]
#
# RECORD_AFTER_SEQ is the record's last seq before the Run started (fault-kill.sh prints
# it); only later record lines are compared. The window must not contain task dispatches
# from another Run, which holds for the demo because it starts exactly one Run.
#
# How the record tells a re-sent request apart. The Provider writes `arrived` for every
# request it decodes, before any barrier hold, under a task id it generates on arrival,
# because the Backend does not choose one. The idempotency key is claimed only when the
# request is let through:
#   - a first dispatch is `arrived X` ... `responded X status=202`: task X was created;
#   - a re-sent dispatch carrying an already-claimed key is `arrived Y` ...
#     `responded X status=200 replayed=true`: no task and no callback were created, and
#     the caller is told the original id X, so Y never names a task;
#   - a dispatch whose caller disconnected while it was held is `arrived X` ...
#     `abandoned X`: no task was created and no key was claimed, so the retry is a first
#     dispatch, not a replay.
#
# Checks (exit status 1 when any fails):
#   - every task the Provider created appears exactly once as `arrived`;
#   - every external task id the Run is bound to is a task the Provider created, and
#     every created task is bound to exactly one dispatch unit of the Run, so a
#     non-replayed duplicate dispatch of one NodeRun or Tool Attempt is a failure while a
#     replayed one passes with a note;
#   - accepted callback deliveries are at least the callback-received Events;
#   - status queries the Provider answered with a finished task (`polled` with taskStatus
#     SUCCEEDED or FAILED) are at least the completions whose source is PROVIDER_POLL. A
#     poll that finds the task still RUNNING changes nothing and is only counted.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools curl jq

run_id=${1:?usage: ledger-vs-record.sh RUN_ID [RECORD_AFTER_SEQ]}
record_after_seq=${2:-0}
[[ "$run_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "run id has unexpected characters: $run_id"
[[ "$record_after_seq" =~ ^[0-9]+$ ]] || die "RECORD_AFTER_SEQ must be a non-negative integer"

failures=0
pass() { printf 'PASS %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; failures=$((failures + 1)); }
note() { printf '     note: %s\n' "$*"; }

snapshot=$(curl -fsS "$BACKEND_URL/api/runs/$run_id")
events=$(run_events "$run_id")

# Bindings the Run committed, one row per dispatch unit: the NodeRun for a plain node,
# the Tool Attempt's Action for an Agent node. Ids are read from the read-only Node
# detail and Agent Trace projections, never from the Provider.
bindings='[]'
while IFS=$'\t' read -r node_run_id node_type; do
  [[ -n "$node_run_id" ]] || continue
  detail=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$node_run_id")
  rows=$(jq --arg unit "$node_run_id" '[.attempts[]
      | select(.callbackBinding.externalTaskId != null)
      | {unit: $unit, attempt: .attemptNo, id: .callbackBinding.externalTaskId}]' <<<"$detail")
  if [[ "$node_type" == agent ]]; then
    trace=$(curl -fsS "$BACKEND_URL/api/runs/$run_id/nodes/$node_run_id/agent")
    rows=$(jq --argjson rows "$rows" --arg unit "$node_run_id" '$rows + [paths(objects) as $p
        | getpath($p) | select(type == "object" and (.externalTaskId? | type) == "string")
        | {unit: ($unit + ":" + ($p | map(tostring) | join("."))), attempt: (.attemptNo // null), id: .externalTaskId}]' <<<"$trace")
  fi
  bindings=$(jq -s 'add' <(printf '%s' "$bindings") <(printf '%s' "$rows"))
done < <(jq -r '.nodeRuns[] | [.id, .nodeType] | @tsv' <<<"$snapshot")

window=$(record_after "$record_after_seq" | jq -s '.')
record=$(jq '[.[] | select(.kind == "task")]' <<<"$window")
polls=$(jq '[.[] | select(.kind == "poll")]' <<<"$window")

printf 'ledger vs dispatch record for run %s (record lines after seq %s)\n' "$run_id" "$record_after_seq"
printf 'run status: %s, ledger events: %s, bound external tasks: %s, record task lines: %s, record poll lines: %s\n\n' \
  "$(jq -r '.run.status' <<<"$snapshot")" "$(jq 'length' <<<"$events")" \
  "$(jq 'length' <<<"$bindings")" "$(jq 'length' <<<"$record")" "$(jq 'length' <<<"$polls")"

created=$(jq '[.[] | select(.event == "responded" and .status == 202 and (.replayed | not)) | .externalTaskId]' <<<"$record")

# 1. Every created task arrived exactly once.
bad=$(jq --argjson created "$created" '[$created[] as $id
    | {id: $id, arrived: ([.[] | select(.event == "arrived" and .externalTaskId == $id)] | length)}
    | select(.arrived != 1)]' <<<"$record")
if [[ $(jq 'length' <<<"$bad") -eq 0 ]]; then
  pass "created_task_arrived_once         created=$(jq 'length' <<<"$created")"
else
  fail "created_task_arrived_once         $(jq -c '.' <<<"$bad")"
fi

# 2. Bound ids are created tasks, and each created task belongs to exactly one dispatch unit.
unknown=$(jq --argjson created "$created" '[.[] | select(.id as $id | $created | index($id) | not)]' <<<"$bindings")
orphan=$(jq --argjson bindings "$bindings" '[.[] as $id | select([$bindings[] | select(.id == $id)] | length == 0) | $id]' <<<"$created")
duplicate_units=$(jq --argjson created "$created" '[.[] | select(.id as $id | $created | index($id))]
    | group_by(.unit | sub(":.*"; "")) | map(select((map(.id) | unique | length) > 1))
    | map({unit: .[0].unit, tasks: (map(.id) | unique)})' <<<"$bindings")
if [[ $(jq 'length' <<<"$unknown") -eq 0 && $(jq 'length' <<<"$orphan") -eq 0 &&
  $(jq 'length' <<<"$duplicate_units") -eq 0 ]]; then
  pass "one_created_task_per_dispatch     bound=$(jq -c 'map(.id)' <<<"$bindings")"
else
  fail "one_created_task_per_dispatch     bound-but-not-created=$(jq -c '.' <<<"$unknown") created-but-not-bound=$(jq -c '.' <<<"$orphan") units-with-several-tasks=$(jq -c '.' <<<"$duplicate_units")"
fi
jq -r '.[] | select(.event == "responded" and .replayed == true)
    | "re-sent dispatch answered with original task \(.externalTaskId) (replayed=true): idempotent, no new task"' <<<"$record" |
  while IFS= read -r line; do note "$line"; done
jq -r '.[] | select(.event == "abandoned")
    | "dispatch \(.externalTaskId) was abandoned: the caller disconnected while it was held, so no task was created"' <<<"$record" |
  while IFS= read -r line; do note "$line"; done
jq -r --argjson created "$created" '. as $all | [.[] | select(.event == "arrived") | .externalTaskId]
    | map(select(. as $id | ($created | index($id) | not)
        and ([$all[] | select(.externalTaskId == $id and (.event == "abandoned" or .event == "rejected"))] | length == 0)))
    | .[] | "arrival \(.) names no task: it is the arrival of a re-sent dispatch"' <<<"$record" |
  while IFS= read -r line; do note "$line"; done

# 3. Accepted callback deliveries cover every callback-received Event.
deliveries=$(jq --argjson created "$created" '[.[] | select(.event == "callback" and (.externalTaskId as $id | $created | index($id)))]' <<<"$record")
attempted=$(jq 'length' <<<"$deliveries")
accepted=$(jq '[.[] | select(.status >= 200 and .status < 300)] | length' <<<"$deliveries")
received=$(jq '[.[] | select(.type == "NODE_CALLBACK_RECEIVED"
    or ((.type == "AGENT_ACTION_COMPLETED" or .type == "AGENT_ACTION_FAILED")
        and ((.payload.completionSource // .payload.failureSource) == "CALLBACK")))] | length' <<<"$events")
if ((accepted >= received)); then
  pass "callback_deliveries_cover_events  deliveries=$attempted accepted=$accepted callback_received_events=$received"
else
  fail "callback_deliveries_cover_events  deliveries=$attempted accepted=$accepted callback_received_events=$received"
fi
if ((attempted > accepted)); then
  note "$((attempted - accepted)) delivery attempt(s) got no 2xx answer, for example while the Backend was down"
fi

# 4. Finished-task poll answers cover every completion whose source is a Provider poll.
polled=$(jq --argjson created "$created" '[.[] | select(.event == "polled" and (.externalTaskId as $id | $created | index($id)))]' <<<"$polls")
poll_answers=$(jq 'length' <<<"$polled")
poll_finished=$(jq '[.[] | select(.status == 200 and (.taskStatus == "SUCCEEDED" or .taskStatus == "FAILED"))] | length' <<<"$polled")
poll_running=$(jq '[.[] | select(.status == 200 and .taskStatus == "RUNNING")] | length' <<<"$polled")
poll_completions=$(jq '[.[] | select((.type == "NODE_COMPLETED" or .type == "AGENT_ACTION_COMPLETED")
    and .payload.completionSource == "PROVIDER_POLL")] | length' <<<"$events")
if ((poll_finished >= poll_completions)); then
  pass "poll_answers_cover_completions    polled=$poll_answers finished=$poll_finished running=$poll_running provider_poll_completions=$poll_completions"
else
  fail "poll_answers_cover_completions    polled=$poll_answers finished=$poll_finished running=$poll_running provider_poll_completions=$poll_completions"
fi
jq -r '[.[] | select(.type == "NODE_COMPLETED")] | group_by(.payload.completionSource)
    | .[] | "\(length) NODE_COMPLETED event(s) with completionSource \(.[0].payload.completionSource)"' <<<"$events" |
  while IFS= read -r line; do note "$line"; done

printf '\n'
if ((failures > 0)); then
  printf 'ledger vs record: %d check(s) FAILED\n' "$failures"
  exit 1
fi
printf 'ledger vs record: all checks passed\n'
