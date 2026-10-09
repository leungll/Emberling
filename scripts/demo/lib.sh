# shellcheck shell=bash
# Shared settings for the fault-injection demo scripts. Sourced, never executed.
#
# The scripts drive the local Compose stack in deploy/compose.yaml only. They read the
# Backend's public REST API and the Mock Provider's test-control routes, and run SQL
# through the postgres container, so the host needs docker, curl and jq but not psql.

DEMO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
COMPOSE_FILE="$DEMO_ROOT/deploy/compose.yaml"
BACKEND_URL=${EMBERLING_DEMO_BACKEND_URL:-http://localhost:8080}
MOCK_URL=${EMBERLING_DEMO_MOCK_URL:-http://localhost:9101}

# The backend service refuses to start without these two values. When the developer has
# not set them, each script invocation generates throwaway local values. They are never
# printed. A restart with `docker compose start` keeps the container's original values,
# so callback tokens issued before the kill still verify after it.
if [[ -z "${EMBERLING_MODEL_PROVIDER_API_KEY:-}" ]]; then
  EMBERLING_MODEL_PROVIDER_API_KEY="demo-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
  export EMBERLING_MODEL_PROVIDER_API_KEY
fi
if [[ -z "${EMBERLING_CALLBACK_SIGNING_SECRET:-}" ]]; then
  EMBERLING_CALLBACK_SIGNING_SECRET="demo-$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  export EMBERLING_CALLBACK_SIGNING_SECRET
fi

compose() {
  docker compose -f "$COMPOSE_FILE" --profile mock "$@"
}

log() {
  printf '==> %s\n' "$*" >&2
}

die() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_tools() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required on the host"
  done
}

# wait_until DESCRIPTION SECONDS COMMAND... runs COMMAND every half second until it
# succeeds or the deadline passes. The interval only paces the polling; the condition, not
# the elapsed time, decides when the script moves on.
wait_until() {
  local description=$1 seconds=$2
  shift 2
  local deadline=$((SECONDS + seconds))
  until "$@"; do
    if ((SECONDS >= deadline)); then
      die "timed out after ${seconds}s waiting for: $description"
    fi
    sleep 0.5
  done
}

backend_ready() {
  curl -fsS -o /dev/null "$BACKEND_URL/ready" 2>/dev/null
}

mock_ready() {
  curl -fsS -o /dev/null "$MOCK_URL/control/barrier" 2>/dev/null
}

# record_last_seq prints the highest seq in the Mock Provider's dispatch record, paging
# with ?after= while the provider reports a truncated response.
record_last_seq() {
  local after=0 page headers
  headers=$(mktemp)
  while :; do
    page=$(curl -fsS -D "$headers" "$MOCK_URL/control/record?after=$after")
    if [[ -n "$page" ]]; then
      after=$(jq -s 'map(.seq) | max' <<<"$page")
    fi
    grep -qi '^x-mockprovider-record-truncated: true' "$headers" || break
  done
  rm -f "$headers"
  printf '%s\n' "$after"
}

# record_after SEQ prints every dispatch record line with seq greater than SEQ.
record_after() {
  local after=$1 page headers
  headers=$(mktemp)
  while :; do
    page=$(curl -fsS -D "$headers" "$MOCK_URL/control/record?after=$after")
    if [[ -n "$page" ]]; then
      printf '%s\n' "$page"
      after=$(jq -s 'map(.seq) | max' <<<"$page")
    fi
    grep -qi '^x-mockprovider-record-truncated: true' "$headers" || break
  done
  rm -f "$headers"
}

# run_events RUN_ID prints the Run's committed Event ledger as one JSON array, paging by
# seq through the JSON branch of the events endpoint.
run_events() {
  local run_id=$1 after=0 page all='[]' count
  while :; do
    page=$(curl -fsS -H 'Accept: application/json' \
      "$BACKEND_URL/api/runs/$run_id/events?afterSeq=$after&limit=200" | jq '.items')
    count=$(jq 'length' <<<"$page")
    ((count > 0)) || break
    all=$(jq -s 'add' <(printf '%s' "$all") <(printf '%s' "$page"))
    after=$(jq 'map(.seq) | max' <<<"$page")
  done
  printf '%s\n' "$all"
}
