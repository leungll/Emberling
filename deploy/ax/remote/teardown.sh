#!/usr/bin/env bash
# Removes the deployment from the sandbox host: the host processes, Studio, PostgreSQL,
# the kind cluster and its local registry. Pinned checkouts, built binaries, logs and
# request records stay under the deployment directory; the PostgreSQL volume and the
# runner's state stay too unless --purge-data is given. Nothing outside the deployment
# directory and the named containers, volume and cluster is touched.
#
# Usage: teardown.sh [--purge-data]
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

purge=false
for arg in "$@"; do
  case "${arg}" in
    --purge-data) purge=true ;;
    *) fail "unknown option ${arg}" ;;
  esac
done

bash "${SCRIPT_DIR}/stop.sh"
if docker container inspect "${POSTGRES_CONTAINER}" >/dev/null 2>&1; then
  docker rm -f "${POSTGRES_CONTAINER}" >/dev/null
  log "removed the PostgreSQL container"
fi
# The ax CLI reaches ax-server through a kubectl port-forward that it leaves running in
# the background for later calls; it is useless once the cluster is gone.
if pkill -f "^kubectl --context ${KUBE_CONTEXT} port-forward -n ax-system svc/ax-server"; then
  log "stopped the ax CLI's ax-server port-forward"
fi
if kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER}"; then
  log "deleting kind cluster ${KIND_CLUSTER}"
  kind delete cluster --name "${KIND_CLUSTER}"
fi
if docker container inspect kind-registry >/dev/null 2>&1; then
  docker rm -f kind-registry >/dev/null
  log "removed the kind registry"
fi
if [[ "${purge}" == true ]]; then
  if docker volume inspect "${POSTGRES_VOLUME}" >/dev/null 2>&1; then
    docker volume rm "${POSTGRES_VOLUME}" >/dev/null
    log "removed the PostgreSQL volume"
  fi
  rm -rf "${RECORD_DIR}" "${STATE_DIR}" "${RUN_DIR}/assets"
  log "removed records, generated secrets and assets"
fi
log "teardown complete"
