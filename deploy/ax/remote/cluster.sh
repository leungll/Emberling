#!/usr/bin/env bash
# Checks out Substrate and AX at their pinned commits, creates the kind cluster with its
# local registry on port 5001 through Substrate's own script, and deploys Substrate. The
# cluster script probes /dev/kvm and disables micro-VM support when it is absent; gVisor,
# the only sandbox AX requests, works either way.
#
# Safe to run again: an existing cluster is kept unless --recreate is given, and the
# Substrate install is skipped when this Substrate version is already rolled out unless
# --reinstall is given.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

recreate=false
reinstall=false
for arg in "$@"; do
  case "${arg}" in
    --recreate) recreate=true; reinstall=true ;;
    --reinstall) reinstall=true ;;
    *) fail "unknown option ${arg}" ;;
  esac
done

require_cmd docker go kubectl kind ko git make
docker info >/dev/null 2>&1 || fail "docker is not usable by ${USER}; open a new SSH session after the prepare step"

checkout_pinned "${SUBSTRATE_REPO}" "${SUBSTRATE_DIR}" "${SUBSTRATE_COMMIT}"
checkout_pinned "${AX_REPO}" "${AX_DIR}" "${AX_COMMIT}"
mkdir -p "${LOG_DIR}"

if [[ "${recreate}" == true ]] || ! kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER}"; then
  log "creating the kind cluster and the local registry"
  (cd "${SUBSTRATE_DIR}" && KIND_CLUSTER_NAME="${KIND_CLUSTER}" hack/create-kind-cluster.sh) 2>&1 | tee "${LOG_DIR}/create-kind-cluster.log"
else
  log "kind cluster ${KIND_CLUSTER} exists; keeping it"
fi
use_kind_context
grep -h "/dev/kvm" "${LOG_DIR}/create-kind-cluster.log" 2>/dev/null | tail -1 || true

substrate_version="${SUBSTRATE_COMMIT:0:8}"
installed=false
if kubectl -n ate-system get daemonset "atelet-${substrate_version}" >/dev/null 2>&1 \
  && kubectl get sandboxconfig gvisor-default >/dev/null 2>&1; then
  installed=true
fi
if [[ "${reinstall}" == true || "${installed}" == false ]]; then
  log "deploying Substrate ${substrate_version} (several minutes)"
  (cd "${SUBSTRATE_DIR}" && KIND_CLUSTER_NAME="${KIND_CLUSTER}" hack/install-ate-kind.sh --deploy-ate-system) \
    >"${LOG_DIR}/install-ate-system.log" 2>&1 || { tail -40 "${LOG_DIR}/install-ate-system.log"; fail "Substrate install failed; full log in ${LOG_DIR}/install-ate-system.log"; }
else
  log "Substrate ${substrate_version} already deployed; keeping it"
fi

log "Substrate status"
for d in ate-api-server ate-controller atenet-router atenet-egress; do
  kubectl -n ate-system rollout status "deployment/${d}" --timeout=300s
done
kubectl -n ate-system rollout status "daemonset/atelet-${substrate_version}" --timeout=300s
kubectl get sandboxconfig gvisor-default
docker port kind-registry
