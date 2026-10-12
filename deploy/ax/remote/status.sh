#!/usr/bin/env bash
# Prints whether each host process is running and answering its health check, and the
# supervisor image the runner uses.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

check() {
  local name="$1" url="$2" state="stopped" code="-"
  pid_alive "${name}" && state="running"
  [[ -n "${url}" ]] && code="$(curl -s -o /dev/null -m 2 -w '%{http_code}' "${url}" || true)"
  printf '%-16s %-8s %s %s\n' "${name}" "${state}" "${code}" "${url}"
}

printf '%-16s %-8s %s\n' PROCESS STATE "HTTP URL"
check mockprovider "http://127.0.0.1:${MOCKPROVIDER_PORT}/healthz"
check mockproduction "http://127.0.0.1:${MOCKPRODUCTION_PORT}/healthz"
check router-forward "http://127.0.0.1:${ROUTER_LOCAL_PORT}/healthz"
check sandboxrunner "http://127.0.0.1:${RUNNER_PORT}/healthz"
check backend "http://127.0.0.1:${BACKEND_PORT}/ready"
if docker container inspect "${STUDIO_CONTAINER}" >/dev/null 2>&1; then
  printf '%-16s %-8s %s\n' studio "$(docker inspect -f '{{.State.Status}}' "${STUDIO_CONTAINER}")" "$(curl -s -o /dev/null -m 2 -w '%{http_code}' "http://127.0.0.1:${STUDIO_PORT}/" || true) http://127.0.0.1:${STUDIO_PORT}/"
fi
if docker container inspect "${POSTGRES_CONTAINER}" >/dev/null 2>&1; then
  printf '%-16s %s\n' postgres "$(docker inspect -f '{{.State.Status}}' "${POSTGRES_CONTAINER}")"
fi
[[ -s "${SUPERVISOR_REF_FILE}" ]] && echo "supervisor image: $(cat "${SUPERVISOR_REF_FILE}")"
echo "router 404 without an actor header means the port-forward is up"
