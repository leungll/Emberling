#!/usr/bin/env bash
# Builds the sandbox supervisor image for linux/amd64 from the Emberling source the driver
# sent to this host, pushes it to the kind cluster's local registry and records the
# digest-pinned reference the runner must use. Prints that reference on its last line.
#
# Usage: image.sh <source revision>
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

revision="${1:-local}"
[[ "${revision}" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || fail "the source revision must be a plain tag"
require_cmd docker
[[ -f "${EMBERLING_SRC}/deploy/Dockerfile.sandboxsupervisor" ]] || fail "no Emberling source at ${EMBERLING_SRC}; the driver sends it"
mkdir -p "${STATE_DIR}" "${LOG_DIR}"

tag="${SUPERVISOR_REPO}:${revision}"
log "building ${tag} for linux/amd64"
docker build --platform linux/amd64 -f "${EMBERLING_SRC}/deploy/Dockerfile.sandboxsupervisor" \
  -t "${tag}" "${EMBERLING_SRC}/backend" >"${LOG_DIR}/supervisor-image.log" 2>&1 \
  || { tail -30 "${LOG_DIR}/supervisor-image.log"; fail "image build failed"; }
docker push "${tag}" >>"${LOG_DIR}/supervisor-image.log" 2>&1 || fail "image push failed"

platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "${tag}")"
[[ "${platform}" == "linux/amd64" ]] || fail "built image is ${platform}, not linux/amd64"
ref="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${tag}" | grep -m1 "^${SUPERVISOR_REPO}@sha256:")" \
  || fail "the pushed image has no registry digest"
echo "${ref}" >"${SUPERVISOR_REF_FILE}"
log "supervisor image (${platform}), recorded in ${SUPERVISOR_REF_FILE}"
echo "${ref}"
