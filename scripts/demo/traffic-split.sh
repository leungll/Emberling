#!/usr/bin/env bash
# Splits a batch of serving requests between two Definition versions of one Workflow, the
# way a gateway in front of Emberling would: each request has a stable identifier, the
# identifier's hash decides its version by ratio, and the identifier is sent as the
# Idempotency-Key. Emberling binds every Run to the version it was created with, so a
# ratio change only redirects requests that have not been created yet; in-flight Runs of
# the other version finish on it.
#
# Usage: scripts/demo/traffic-split.sh WORKFLOW_ID V1 V2 RATIO [COUNT] [BATCH]
#
#   V1, V2   the two Definition versions (positive integers, V1 the current one).
#   RATIO    the percentage of new requests sent to V2, 0 to 100. 0 sends every new
#            request to V1.
#   COUNT    requests in the batch (default 30).
#   BATCH    the batch name the request identifiers start with (default "batch-1").
#            Request identifiers are "<WORKFLOW_ID>:<BATCH>:<n>".
#
# Replays. The script keeps its assignments per Workflow and batch under
# EMBERLING_DEMO_SPLIT_STATE: a request identifier it has already sent keeps the version
# it was assigned then, whatever the ratio is now, and is re-sent under the same key, so
# the Backend answers 200 with the original Run and `Idempotent-Replayed: true`. Without
# that memory a replay under a changed ratio would send the same key with another
# version, and the Backend refuses that with 409 IDEMPOTENCY_KEY_MISMATCH rather than
# rebind the key: the record keeps the gateway's choice as stable as the Backend requires.
#
# Environment:
#   EMBERLING_DEMO_SPLIT_STATE       directory of the assignment records (default
#                                    ${TMPDIR:-/tmp}/emberling-split).
#   EMBERLING_DEMO_SPLIT_PHOTO       the photo file every request carries; without it a
#                                    small generated PNG is uploaded once per batch and
#                                    reused by its replays (the asset is part of the
#                                    keyed content).
#   EMBERLING_DEMO_SPLIT_WAIT        1 (default) waits for every Run of the batch to reach
#                                    a terminal status; 0 returns after submitting.
#   EMBERLING_DEMO_SPLIT_DEADLINE_S  the wait deadline in seconds (default 600).
#   EMBERLING_DEMO_SPLIT_DRY_RUN     1 prints the assignment of every request of the
#                                    batch and sends nothing; useful to pick a batch name
#                                    before the demo.
#
# Prints one line per request (identifier, version, Run, HTTP status, replayed) and a
# per-version count read back through GET /api/runs?workflowId=&definitionVersion=.
# Writes RUN_ID (every Run of the batch, comma-separated), WORKFLOW_ID, RUN_STATUS,
# SUBMISSIONS and SUMMARY to $DEMO_STATE when it is set. Exit status 1 when any
# submission was refused or, with the wait on, any Run did not complete.
set -euo pipefail

# shellcheck source=scripts/demo/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_tools curl jq

workflow_id=${1:?usage: traffic-split.sh WORKFLOW_ID V1 V2 RATIO [COUNT] [BATCH]}
v1=${2:?usage: traffic-split.sh WORKFLOW_ID V1 V2 RATIO [COUNT] [BATCH]}
v2=${3:?usage: traffic-split.sh WORKFLOW_ID V1 V2 RATIO [COUNT] [BATCH]}
ratio=${4:?usage: traffic-split.sh WORKFLOW_ID V1 V2 RATIO [COUNT] [BATCH]}
count=${5:-30}
batch=${6:-batch-1}
[[ "$workflow_id" =~ ^[A-Za-z0-9_-]+$ ]] || die "workflow id has unexpected characters: $workflow_id"
[[ "$v1" =~ ^[1-9][0-9]*$ && "$v2" =~ ^[1-9][0-9]*$ ]] || die "versions must be positive integers"
[[ "$ratio" =~ ^[0-9]+$ ]] && ((ratio <= 100)) || die "RATIO must be an integer percentage from 0 to 100"
[[ "$count" =~ ^[1-9][0-9]*$ ]] || die "COUNT must be a positive integer"
[[ "$batch" =~ ^[A-Za-z0-9_.-]+$ ]] || die "BATCH may only contain letters, digits, '.', '_' and '-'"
wait_for_runs=${EMBERLING_DEMO_SPLIT_WAIT:-1}
dry_run=${EMBERLING_DEMO_SPLIT_DRY_RUN:-0}
deadline_s=${EMBERLING_DEMO_SPLIT_DEADLINE_S:-600}
[[ "$deadline_s" =~ ^[1-9][0-9]*$ ]] || die "EMBERLING_DEMO_SPLIT_DEADLINE_S must be a positive integer"

state_dir=${EMBERLING_DEMO_SPLIT_STATE:-${TMPDIR:-/tmp}/emberling-split}
mkdir -p "$state_dir"
record="$state_dir/$workflow_id.$batch.tsv"
photo_record="$state_dir/$workflow_id.$batch.photo.json"
touch "$record"

if command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 | cut -c1-8; }
else
  require_tools sha256sum
  digest() { sha256sum | cut -c1-8; }
fi

# assign ID prints the version for a request identifier: the recorded one when the
# identifier was sent before, otherwise V2 when the first 32 bits of its SHA-256, taken
# modulo 100, fall below RATIO.
assign() {
  local recorded bucket
  recorded=$(awk -F '\t' -v id="$1" '$1 == id {print $2; exit}' "$record")
  if [[ -n "$recorded" ]]; then
    printf '%s\n' "$recorded"
    return
  fi
  bucket=$((16#$(printf '%s' "$1" | digest) % 100))
  if ((bucket < ratio)); then printf '%s\n' "$v2"; else printf '%s\n' "$v1"; fi
}

if [[ "$dry_run" == 1 ]]; then
  printf '%-40s %s\n' request version
  for ((n = 1; n <= count; n++)); do
    id="$workflow_id:$batch:$n"
    printf '%-40s v%s\n' "$id" "$(assign "$id")"
  done
  exit 0
fi

log "checking that the Backend is reachable"
wait_until "Backend /ready" 60 backend_ready

if [[ -s "$photo_record" ]]; then
  photo=$(cat "$photo_record")
else
  work=$(mktemp -d "${TMPDIR:-/tmp}/emberling-split.XXXXXX")
  trap 'rm -rf "$work"' EXIT
  if [[ -n "${EMBERLING_DEMO_SPLIT_PHOTO:-}" ]]; then
    photo_file=$EMBERLING_DEMO_SPLIT_PHOTO
    [[ -r "$photo_file" ]] || die "EMBERLING_DEMO_SPLIT_PHOTO is not readable: $photo_file"
    media_type=image/jpeg
    [[ "$photo_file" == *.png ]] && media_type=image/png
  else
    printf '\x89PNG\r\n\x1a\nsplit photo %s %s\n' "$workflow_id" "$batch" >"$work/photo.png"
    photo_file="$work/photo.png" media_type=image/png
  fi
  log "uploading the photo of batch $batch"
  photo=$(curl -fsS -F "file=@$photo_file;type=$media_type" "$BACKEND_URL/api/assets")
  printf '%s\n' "$photo" >"$photo_record"
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/emberling-split-run.XXXXXX")
cleanup() { rm -rf "$work_dir"; }
trap cleanup EXIT

log "batch $batch: $count request(s), $ratio% of new requests to version $v2, the rest to version $v1"
printf '%-40s %-8s %-28s %-5s %s\n' request version run http replayed
refused=0 created=0 replayed=0 run_ids=()
for ((n = 1; n <= count; n++)); do
  id="$workflow_id:$batch:$n"
  version=$(assign "$id")
  request=$(jq -n --arg wf "$workflow_id" --argjson v "$version" --argjson photo "$photo" --arg text "Request $n of $batch." \
    '{workflowId: $wf, definitionVersion: $v, input: {photo: $photo, request: $text}}')
  code=$(curl -sS -o "$work_dir/body" -D "$work_dir/headers" -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' -H "Idempotency-Key: $id" --data-binary "$request" "$BACKEND_URL/api/runs")
  run_of_request=$(jq -r '.id // empty' "$work_dir/body")
  was_replayed=no
  case "$code" in
  201) created=$((created + 1)) ;;
  200)
    replayed=$((replayed + 1))
    grep -qi '^idempotent-replayed: true' "$work_dir/headers" && was_replayed=yes
    ;;
  *)
    refused=$((refused + 1))
    run_of_request="-"
    ;;
  esac
  printf '%-40s %-8s %-28s %-5s %s\n' "$id" "v$version" "$run_of_request" "$code" "$was_replayed"
  if [[ "$run_of_request" != "-" ]]; then
    if ! awk -F '\t' -v id="$id" '$1 == id {found = 1} END {exit !found}' "$record"; then
      printf '%s\t%s\t%s\n' "$id" "$version" "$run_of_request" >>"$record"
    fi
    run_ids+=("$run_of_request")
  fi
done
submissions=$count
printf '\nsubmitted %d: created %d, replayed %d, refused %d\n' "$count" "$created" "$replayed" "$refused"

run_status() {
  curl -fsS "$BACKEND_URL/api/runs/$1" 2>/dev/null | jq -r '.run.status'
}
all_terminal() {
  local run_id status
  for run_id in "${run_ids[@]}"; do
    status=$(run_status "$run_id") || return 1
    [[ "$status" == COMPLETED || "$status" == FAILED ]] || return 1
  done
}
completed=0
if [[ "$wait_for_runs" == 1 && ${#run_ids[@]} -gt 0 ]]; then
  log "waiting for ${#run_ids[@]} Run(s) to reach a terminal status (deadline ${deadline_s}s)"
  wait_until "every Run of the batch to become COMPLETED or FAILED" "$deadline_s" all_terminal
  for run_id in "${run_ids[@]}"; do
    [[ "$(run_status "$run_id")" == COMPLETED ]] && completed=$((completed + 1))
  done
fi

# version_count VERSION prints how many Runs of the Workflow the Backend lists under that
# version: the server's own count, not the script's record.
version_count() {
  curl -fsS "$BACKEND_URL/api/runs?workflowId=$workflow_id&definitionVersion=$1&limit=1" | jq -r '.counts.all'
}
recorded_v1=$(awk -F '\t' -v v="$v1" '$2 == v' "$record" | wc -l | tr -d ' ')
recorded_v2=$(awk -F '\t' -v v="$v2" '$2 == v' "$record" | wc -l | tr -d ' ')
listed_v1=$(version_count "$v1")
listed_v2=$(version_count "$v2")
printf 'batch %s assignments: v%s=%s v%s=%s; Runs the Backend lists for the workflow: v%s=%s v%s=%s\n' \
  "$batch" "$v1" "$recorded_v1" "$v2" "$recorded_v2" "$v1" "$listed_v1" "$v2" "$listed_v2"
summary="batch=$batch ratio=$ratio% count=$count created=$created replayed=$replayed refused=$refused v$v1=$recorded_v1 v$v2=$recorded_v2"

if [[ -n "${DEMO_STATE:-}" ]]; then
  ids=$(IFS=,; printf '%s' "${run_ids[*]}")
  printf 'RUN_ID=%s\nWORKFLOW_ID=%s\nRUN_STATUS=%s\nSUBMISSIONS=%s\nSUMMARY=%s\n' \
    "$ids" "$workflow_id" "COMPLETED $completed/${#run_ids[@]}" "$submissions" "$summary" >>"$DEMO_STATE"
fi

if ((refused > 0)); then
  printf 'traffic split: %d submission(s) refused\n' "$refused"
  exit 1
fi
if [[ "$wait_for_runs" == 1 ]] && ((completed != ${#run_ids[@]})); then
  printf 'traffic split: %d of %d Run(s) did not complete\n' $((${#run_ids[@]} - completed)) "${#run_ids[@]}"
  exit 1
fi
