#!/usr/bin/env bash
# Stops the host processes start.sh launched, Studio, and the PostgreSQL container. Data
# in the PostgreSQL volume and the request records is kept.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

for name in backend sandboxrunner router-forward mockproduction mockprovider; do
  stop_process "${name}"
done
if docker container inspect "${STUDIO_CONTAINER}" >/dev/null 2>&1; then
  docker rm -f "${STUDIO_CONTAINER}" >/dev/null
  log "removed Studio"
fi
if docker container inspect "${POSTGRES_CONTAINER}" >/dev/null 2>&1; then
  docker stop "${POSTGRES_CONTAINER}" >/dev/null
  log "stopped PostgreSQL"
fi
