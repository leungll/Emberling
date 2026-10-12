#!/usr/bin/env bash
# Starts the Emberling stack on the sandbox host against the AX cluster: PostgreSQL in
# Docker, then the Mock Provider, the mock production service, the router port-forward,
# the sandbox runner with its ax backend and test controls, and the Backend, each as a
# background process bound to the loopback interface. With --studio it also builds and
# starts Studio. Running it again restarts the processes on the current binaries.
#
# Usage: start.sh [--studio]
#        start.sh --kill-restart backend|sandboxrunner
#
# --kill-restart sends SIGKILL to one running process and starts it again with the same
# settings and binary, leaving everything else running. The Backend restart and runner
# redelivery checks in the runbook use it.
#
# Timeouts, the reconcile interval and the optional real Model settings (EMBERLING_MODELS,
# EMBERLING_MODEL_BASE_URL, EMBERLING_MODEL_API_KEY) come from defaults below, overridden
# by ${STATE_DIR}/operator.env when the driver copied one. The callback signing secret and the Model Provider key are generated
# on this host into ${STATE_DIR}/secrets.env (mode 0600) on first start and never printed.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

studio=false
restart=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --studio) studio=true; shift ;;
    --kill-restart)
      [[ $# -ge 2 ]] || fail "--kill-restart needs backend or sandboxrunner"
      restart="$2"; shift 2 ;;
    *) fail "unknown option $1" ;;
  esac
done

require_cmd docker go curl openssl
[[ -x "${BIN_DIR}/ax" ]] || fail "run the control-plane step first"
[[ -s "${SUPERVISOR_REF_FILE}" ]] || fail "run the image step first"
[[ -d "${EMBERLING_SRC}/backend" ]] || fail "no Emberling source at ${EMBERLING_SRC}; the driver sends it"
mkdir -p "${BIN_DIR}" "${LOG_DIR}" "${PID_DIR}" "${STATE_DIR}" "${RECORD_DIR}" "${RUN_DIR}/assets"
use_kind_context

secrets="${STATE_DIR}/secrets.env"
if [[ ! -s "${secrets}" ]]; then
  umask 077
  {
    echo "EMBERLING_CALLBACK_SIGNING_SECRET=$(openssl rand -hex 32)"
    echo "EMBERLING_MODEL_PROVIDER_API_KEY=$(openssl rand -hex 24)"
  } >"${secrets}"
  umask 022
  log "generated ${secrets}"
fi
chmod 600 "${secrets}"
operator_env="${STATE_DIR}/operator.env"

# load_settings exports the defaults for every process, then the operator's overrides.
load_settings() {
  set -a
  SANDBOXRUNNER_TEST_TIMEOUT=300s
  SANDBOXRUNNER_AX_READY_TIMEOUT=120s
  EMBERLING_RECONCILE_INTERVAL=5s
  # shellcheck disable=SC1090
  [[ -f "${operator_env}" ]] && source "${operator_env}"
  set +a
}

# launch runs "$@" detached in its own process group with the environment the named
# function exports, appending to the named log and recording the pid.
launch() {
  local name="$1" envfn="$2"
  shift 2
  stop_process "${name}"
  (
    "${envfn}"
    exec setsid "$@" >>"${LOG_DIR}/${name}.log" 2>&1 </dev/null
  ) &
  echo $! >"${PID_DIR}/${name}.pid"
}

no_env() { :; }

mockprovider_env() {
  load_settings
  export MOCKPROVIDER_ADDR="127.0.0.1:${MOCKPROVIDER_PORT}"
  export MOCKPROVIDER_TEST_CONTROLS=true
  export MOCKPROVIDER_RECORD_PATH="${RECORD_DIR}/mockprovider-dispatch-record.jsonl"
  export EMBERLING_MOCKPROVIDER_PUBLIC_URL="http://localhost:${MOCKPROVIDER_PORT}"
}

mockproduction_env() {
  load_settings
  export MOCKPRODUCTION_ADDR="127.0.0.1:${MOCKPRODUCTION_PORT}"
  export MOCKPRODUCTION_TEST_CONTROLS=true
  export MOCKPRODUCTION_RECORD_PATH="${RECORD_DIR}/mockproduction-request-record.jsonl"
}

# The runner holds the only cluster access: the ax CLI follows the kind context of this
# user's kubeconfig. Nothing from this environment reaches a Task.
runner_env() {
  load_settings
  export SANDBOXRUNNER_ADDR="127.0.0.1:${RUNNER_PORT}"
  export SANDBOXRUNNER_BACKEND=ax
  export SANDBOXRUNNER_TEST_CONTROLS=true
  export SANDBOXRUNNER_RECORD_PATH="${RECORD_DIR}/sandboxrunner-test-record.jsonl"
  SANDBOXRUNNER_IMAGE="$(cat "${SUPERVISOR_REF_FILE}")"
  export SANDBOXRUNNER_IMAGE
  export SANDBOXRUNNER_AX_ATESPACE="${ATESPACE}"
  export SANDBOXRUNNER_AX_ROUTER_URL="http://127.0.0.1:${ROUTER_LOCAL_PORT}"
  export SANDBOXRUNNER_AX_CLI="${BIN_DIR}/ax"
}

# The generated secrets load first so an operator env file may replace the Model Provider
# key; the addresses below are fixed by this layout and always win.
backend_env() {
  set -a
  # shellcheck disable=SC1090
  source "${secrets}"
  set +a
  load_settings
  export EMBERLING_DATABASE_URL="postgres://emberling@127.0.0.1:${POSTGRES_PORT}/emberling?sslmode=disable"
  export EMBERLING_DATABASE_MAX_CONNS=10
  export EMBERLING_ASSET_STORAGE_ROOT="${RUN_DIR}/assets"
  export EMBERLING_ASSET_MAX_UPLOAD_BYTES=16777216
  export EMBERLING_MODEL_PROVIDER_BASE_URL="http://127.0.0.1:${MOCKPROVIDER_PORT}"
  export EMBERLING_CALLBACK_BASE_URL="http://127.0.0.1:${BACKEND_PORT}"
  export EMBERLING_PRODUCTION_BASE_URL="http://127.0.0.1:${MOCKPRODUCTION_PORT}"
  export EMBERLING_SANDBOX_RUNNER_BASE_URL="http://127.0.0.1:${RUNNER_PORT}"
  export EMBERLING_RECONCILE_BATCH_SIZE=100
  export EMBERLING_PENDING_CALLBACK_TTL=24h
  export EMBERLING_PENDING_CALLBACK_MAX_PAYLOAD_BYTES=262144
  export EMBERLING_TRACE_MAX_FIELD_BYTES=8192
  export EMBERLING_TRACE_MAX_RESPONSE_BYTES=1048576
  export EMBERLING_HTTP_ADDR="127.0.0.1:${BACKEND_PORT}"
}

if [[ -n "${restart}" ]]; then
  case "${restart}" in
    backend) envfn=backend_env health="http://127.0.0.1:${BACKEND_PORT}/ready" ;;
    sandboxrunner) envfn=runner_env health="http://127.0.0.1:${RUNNER_PORT}/healthz" ;;
    *) fail "--kill-restart takes backend or sandboxrunner, not ${restart}" ;;
  esac
  pid_alive "${restart}" || fail "${restart} is not running; run start first"
  pid="$(cat "${PID_DIR}/${restart}.pid")"
  log "SIGKILL ${restart} (pid ${pid})"
  kill -KILL -- "-${pid}"
  while kill -0 "${pid}" 2>/dev/null; do sleep 0.2; done
  rm -f "${PID_DIR}/${restart}.pid"
  launch "${restart}" "${envfn}" "${BIN_DIR}/$([[ "${restart}" == backend ]] && echo emberling || echo sandboxrunner)"
  wait_http "${health}" 90 || fail "${restart} did not come back; see ${LOG_DIR}/${restart}.log"
  log "${restart} restarted and healthy"
  exit 0
fi

log "PostgreSQL 18 in Docker on 127.0.0.1:${POSTGRES_PORT}"
if ! docker container inspect "${POSTGRES_CONTAINER}" >/dev/null 2>&1; then
  # Trust authentication is acceptable only because the port is bound to the loopback
  # interface of a single-user experiment host.
  docker run -d --name "${POSTGRES_CONTAINER}" \
    -e POSTGRES_USER=emberling -e POSTGRES_DB=emberling -e POSTGRES_HOST_AUTH_METHOD=trust \
    -v "${POSTGRES_VOLUME}:/var/lib/postgresql" -p "127.0.0.1:${POSTGRES_PORT}:5432" postgres:18 >/dev/null
else
  docker start "${POSTGRES_CONTAINER}" >/dev/null
fi
for _ in $(seq 1 60); do
  docker exec "${POSTGRES_CONTAINER}" pg_isready -h 127.0.0.1 -U emberling -d emberling >/dev/null 2>&1 && break
  sleep 1
done
docker exec "${POSTGRES_CONTAINER}" pg_isready -h 127.0.0.1 -U emberling -d emberling

log "building the Backend, Mock Provider, mock production service and sandbox runner"
(cd "${EMBERLING_SRC}/backend" && for cmd in emberling mockprovider mockproduction sandboxrunner; do
  go build -trimpath -o "${BIN_DIR}/${cmd}" "./cmd/${cmd}"
done)

launch mockprovider mockprovider_env "${BIN_DIR}/mockprovider"
launch mockproduction mockproduction_env "${BIN_DIR}/mockproduction"
launch router-forward no_env bash "${SCRIPT_DIR}/router-forward.sh"
wait_http "http://127.0.0.1:${MOCKPROVIDER_PORT}/healthz" || fail "Mock Provider is not healthy; see ${LOG_DIR}/mockprovider.log"
wait_http "http://127.0.0.1:${MOCKPRODUCTION_PORT}/healthz" || fail "mock production service is not healthy; see ${LOG_DIR}/mockproduction.log"
for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -m 2 -w '%{http_code}' "http://127.0.0.1:${ROUTER_LOCAL_PORT}/healthz" || true)"
  [[ "${code}" == "404" ]] && break
  sleep 1
done
[[ "${code:-}" == "404" ]] || fail "the router port-forward does not answer; see ${LOG_DIR}/router-forward.log"
log "router reachable at 127.0.0.1:${ROUTER_LOCAL_PORT} (404 without an actor header is expected)"

launch sandboxrunner runner_env "${BIN_DIR}/sandboxrunner"
wait_http "http://127.0.0.1:${RUNNER_PORT}/healthz" 90 || fail "sandbox runner is not healthy; see ${LOG_DIR}/sandboxrunner.log"
launch backend backend_env "${BIN_DIR}/emberling"
wait_http "http://127.0.0.1:${BACKEND_PORT}/health" 90 || fail "Backend is not healthy; see ${LOG_DIR}/backend.log"
wait_http "http://127.0.0.1:${BACKEND_PORT}/ready" 90 || fail "Backend is not ready; see ${LOG_DIR}/backend.log"

if [[ "${studio}" == true ]]; then
  log "Studio on 127.0.0.1:${STUDIO_PORT}"
  docker build -t emberling-ax-studio:local -f "${EMBERLING_SRC}/deploy/Dockerfile.studio" \
    "${EMBERLING_SRC}/studio" >"${LOG_DIR}/studio-image.log" 2>&1 || fail "Studio image build failed; see ${LOG_DIR}/studio-image.log"
  docker rm -f "${STUDIO_CONTAINER}" >/dev/null 2>&1 || true
  docker run -d --name "${STUDIO_CONTAINER}" --network host \
    -e EMBERLING_BACKEND_ORIGIN="http://127.0.0.1:${BACKEND_PORT}" emberling-ax-studio:local \
    pnpm run preview --host 127.0.0.1 --port "${STUDIO_PORT}" >/dev/null
  wait_http "http://127.0.0.1:${STUDIO_PORT}/" 90 || fail "Studio is not answering; see docker logs ${STUDIO_CONTAINER}"
fi

bash "${SCRIPT_DIR}/status.sh"
