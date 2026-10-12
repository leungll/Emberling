#!/usr/bin/env bash
# Deploys the AX control plane on the kind cluster from the manifest copies next to this
# script: Redis with a persistent volume, ax-server with its snapshot location pointed at
# the in-cluster object store, and the Substrate WorkerPool that hosts AX's actors. Builds
# the ax CLI and kubectl-ate at the pinned commits and creates the dedicated atespace.
# No Model, Secret or credential is configured for the atespace. Safe to run again.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

require_cmd docker go kubectl ko
[[ -d "${AX_DIR}/.git" && -d "${SUBSTRATE_DIR}/.git" ]] || fail "run the cluster step first"
use_kind_context
mkdir -p "${BIN_DIR}" "${LOG_DIR}" "${STATE_DIR}"

log "Redis with a persistent volume"
kubectl apply -f "${MANIFEST_DIR}/redis.yaml"
kubectl -n ax-system rollout status deployment/ax-redis --timeout=300s

# ko resolves ko:// references against the module of the directory it runs in, so the
# ax-server image builds in the AX checkout and the worker image in the Substrate one.
# The platform is set explicitly; it is also ko's default.
log "ax-server (built with ko for linux/amd64)"
(cd "${AX_DIR}" && KO_DOCKER_REPO="${REGISTRY}" KO_DEFAULTPLATFORMS=linux/amd64 \
  ko apply -f "${MANIFEST_DIR}/ax-server.yaml") >"${LOG_DIR}/ax-server-apply.log" 2>&1 \
  || { tail -20 "${LOG_DIR}/ax-server-apply.log"; fail "ax-server deployment failed"; }
grep -h "Building\|Published" "${LOG_DIR}/ax-server-apply.log" | grep -v sbom || true
kubectl -n ax-system rollout status deployment/ax-server --timeout=300s

log "WorkerPool ax-workers/ax"
substrate_version="${SUBSTRATE_COMMIT:0:8}"
sed "s/\${SUBSTRATE_VERSION}/${substrate_version}/" "${MANIFEST_DIR}/workerpool.yaml" >"${STATE_DIR}/workerpool.rendered.yaml"
(cd "${SUBSTRATE_DIR}" && KO_DOCKER_REPO="${REGISTRY}" KO_DEFAULTPLATFORMS=linux/amd64 \
  hack/run-tool.sh ko apply -f "${STATE_DIR}/workerpool.rendered.yaml") >"${LOG_DIR}/workerpool-apply.log" 2>&1 \
  || { tail -20 "${LOG_DIR}/workerpool-apply.log"; fail "WorkerPool deployment failed"; }
for _ in $(seq 1 60); do
  kubectl -n ax-workers get deployment ax >/dev/null 2>&1 && break
  sleep 2
done
kubectl -n ax-workers rollout status deployment/ax --timeout=300s
kubectl -n ax-workers get workerpool ax

log "ax CLI and kubectl-ate"
(cd "${AX_DIR}" && make build) >"${LOG_DIR}/ax-build.log" 2>&1 || { tail -20 "${LOG_DIR}/ax-build.log"; fail "ax build failed"; }
install -m 0755 "${AX_DIR}/bin/ax" "${BIN_DIR}/ax"
(cd "${SUBSTRATE_DIR}" && GOBIN="${BIN_DIR}" go install ./cmd/kubectl-ate)
ax version
echo "ax sha256 $(sha256sum "${BIN_DIR}/ax" | cut -d' ' -f1)"

log "atespace ${ATESPACE}"
if kubectl ate get atespaces -o json | jq -e --arg a "${ATESPACE}" '[.atespaces[]?.metadata.name] | index($a) != null' >/dev/null; then
  log "atespace ${ATESPACE} exists; keeping it"
else
  kubectl ate create atespace "${ATESPACE}"
fi
kubectl ate get atespaces
log "Models configured for ${ATESPACE} (expected: none)"
ax get models -a "${ATESPACE}"
