# shellcheck shell=bash
# Shared settings for the fault-injection demo scripts. Sourced, never executed.
#
# The scripts drive the local Compose stack in deploy/compose.yaml only. They read the
# Backend's public REST API and the test-control routes of the Mock Provider, the mock
# production service and the sandbox runner, and run SQL through the postgres container,
# so the host needs docker, curl and jq but not psql.

DEMO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
COMPOSE_FILE="$DEMO_ROOT/deploy/compose.yaml"
BACKEND_URL=${EMBERLING_DEMO_BACKEND_URL:-http://localhost:8080}
MOCK_URL=${EMBERLING_DEMO_MOCK_URL:-http://localhost:9101}
PRODUCTION_URL=${EMBERLING_DEMO_PRODUCTION_URL:-http://localhost:9102}
RUNNER_URL=${EMBERLING_DEMO_RUNNER_URL:-http://localhost:9103}

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

production_ready() {
  curl -fsS -o /dev/null "$PRODUCTION_URL/control/barrier" 2>/dev/null
}

runner_ready() {
  curl -fsS -o /dev/null "$RUNNER_URL/control/barrier" 2>/dev/null
}

# record_last_seq [URL] prints the highest seq in a service's request record, the Mock
# Provider's dispatch record by default, paging with ?after= while the service reports a
# truncated response. The three services share one record format and control surface.
record_last_seq() {
  local url=${1:-$MOCK_URL} after=0 page headers
  headers=$(mktemp)
  while :; do
    page=$(curl -fsS -D "$headers" "$url/control/record?after=$after")
    if [[ -n "$page" ]]; then
      after=$(jq -s 'map(.seq) | max' <<<"$page")
    fi
    grep -qi '^x-mockprovider-record-truncated: true' "$headers" || break
  done
  rm -f "$headers"
  printf '%s\n' "$after"
}

# record_after SEQ [URL] prints every line of a service's request record, the Mock
# Provider's dispatch record by default, with seq greater than SEQ.
record_after() {
  local after=$1 url=${2:-$MOCK_URL} page headers
  headers=$(mktemp)
  while :; do
    page=$(curl -fsS -D "$headers" "$url/control/record?after=$after")
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

# Studio demo mode. With EMBERLING_DEMO_STUDIO=1 the demo also starts Studio, prints a link
# to every Run it creates and stops at the moments worth watching. EMBERLING_DEMO_PAUSE=0
# keeps Studio and the links but never stops. Without EMBERLING_DEMO_STUDIO=1 the helpers
# below print nothing and never wait, so the default demo output is unchanged.
STUDIO_URL=${EMBERLING_DEMO_STUDIO_URL:-http://localhost:5173}

studio_mode() {
  [[ "${EMBERLING_DEMO_STUDIO:-0}" == 1 ]]
}

studio_ready() {
  curl -fsS -o /dev/null "$STUDIO_URL/" 2>/dev/null
}

# studio_open URL opens URL in the viewer's browser in Studio demo mode. It does nothing
# without a terminal on stdin (CI or a pipe), when EMBERLING_DEMO_OPEN=0, or when neither
# `open` (macOS) nor `xdg-open` (Linux) is available; the printed link still works.
studio_open() {
  studio_mode || return 0
  [[ "${EMBERLING_DEMO_OPEN:-1}" == 1 && -t 0 ]] || return 0
  if command -v open >/dev/null 2>&1; then
    open "$1" >/dev/null 2>&1 || true
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$1" >/dev/null 2>&1 || true
  fi
}

# studio_run_link RUN_ID prints the Studio page of a Run in Studio demo mode and opens it
# in the browser.
studio_run_link() {
  studio_mode || return 0
  printf '==> Studio run: %s/runs/%s\n' "$STUDIO_URL" "$1" >&2
  studio_open "$STUDIO_URL/runs/$1"
}

# demo_pause MESSAGE says what is about to happen and what to look at in Studio, then
# waits for Enter on the terminal. It never blocks without a terminal on stdin (CI, a pipe
# or < /dev/null): it prints the message and continues. A pause only delays this script;
# the Runtime keeps running meanwhile, so a message names any deadline a long look would
# exceed and change the variant's outcome.
demo_pause() {
  studio_mode || return 0
  if [[ "${EMBERLING_DEMO_PAUSE:-1}" == 0 ]]; then
    printf '>>> (pauses off) %s\n' "$*" >&2
    return 0
  fi
  if [[ -t 0 ]] && { : </dev/tty; } 2>/dev/null; then
    printf '\n>>> PAUSE: %s\n>>> press Enter to continue ' "$*" >&2
    read -r _ </dev/tty || true
  else
    printf '>>> (no tty, continuing) %s\n' "$*" >&2
  fi
}
