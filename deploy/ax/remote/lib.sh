# shellcheck shell=bash
# Shared settings and helpers for the AX sandbox deployment scripts. Every script in this
# directory runs on the sandbox host and sources this file first.

# Pinned upstream sources. The AX commit is the one whose Workspace carries inline files;
# the Substrate commit is the one that AX commit's go.mod requires. Results are only
# claimed for this pair.
AX_REPO=https://github.com/google/ax.git
AX_COMMIT=ac2332829f22360ff97b0ba34d94dd0dd782f17e
SUBSTRATE_REPO=https://github.com/agent-substrate/substrate.git
SUBSTRATE_COMMIT=944abe3278b895ccbf5d45555a49dd0f2f6ceae7

# Pinned host tools. Go must satisfy the AX commit's go.mod (go 1.27.1); kind and ko match
# the versions the Substrate commit's own tool modules use.
GO_VERSION=1.27.2
GO_SHA256=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
KUBECTL_VERSION=v1.37.1
KUBECTL_SHA256=65691ff77eb6fa44c908b77a1082c9f092c3b9733b5cefabec0d1104890e21a8
KIND_VERSION=v0.33.0
KO_VERSION=v0.19.1
DOCKER_CE_VERSION='5:29.9.0-1~ubuntu.24.04~noble'
CONTAINERD_VERSION='2.4.1-2~ubuntu.24.04~noble'
BUILDX_VERSION='0.38.0-1~ubuntu.24.04~noble'

# Layout on the sandbox host. Everything the scripts create lives under DEPLOY_ROOT.
DEPLOY_ROOT="${HOME}/emberling-ax"
SCRIPT_DIR="${DEPLOY_ROOT}/deploy"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
UPSTREAM_DIR="${DEPLOY_ROOT}/upstream"
AX_DIR="${UPSTREAM_DIR}/ax"
SUBSTRATE_DIR="${UPSTREAM_DIR}/substrate"
EMBERLING_SRC="${DEPLOY_ROOT}/emberling"
RUN_DIR="${DEPLOY_ROOT}/run"
BIN_DIR="${RUN_DIR}/bin"
LOG_DIR="${RUN_DIR}/logs"
PID_DIR="${RUN_DIR}/pids"
STATE_DIR="${RUN_DIR}/state"
RECORD_DIR="${RUN_DIR}/records"

# Cluster and sandbox names. One runner owns the atespace: its startup cleanup deletes
# every resource with the prefix there.
KIND_CLUSTER=kind
KUBE_CONTEXT=kind-kind
REGISTRY=localhost:5001
ATESPACE=emberling-sandbox
RESOURCE_PREFIX=emberling-sbx-
SUPERVISOR_REPO="${REGISTRY}/emberling-sandboxsupervisor"
SUPERVISOR_REF_FILE="${STATE_DIR}/supervisor-image.ref"

# Host-local service ports. Every service binds to the loopback interface only; reach
# Studio and the Backend through SSH port forwarding.
BACKEND_PORT=8080
MOCKPROVIDER_PORT=9101
MOCKPRODUCTION_PORT=9102
RUNNER_PORT=9103
ROUTER_LOCAL_PORT=8000
STUDIO_PORT=5173
POSTGRES_PORT=55432
POSTGRES_CONTAINER=emberling-ax-postgres
POSTGRES_VOLUME=emberling-ax-postgres-data
STUDIO_CONTAINER=emberling-ax-studio

export PATH="/usr/local/go/bin:${HOME}/go/bin:${BIN_DIR}:${PATH}"
export GOTOOLCHAIN=local

log() { printf '==> %s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

# require_cmd fails naming the first missing command.
require_cmd() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || fail "$c is not installed; run the prepare step first"
  done
}

# use_kind_context points kubectl, kubectl-ate and ax at the kind cluster.
use_kind_context() {
  kubectl config use-context "${KUBE_CONTEXT}" >/dev/null
}

# checkout_pinned clones repo into dir if needed and checks out commit detached. A checkout
# with local modifications is refused rather than overwritten.
checkout_pinned() {
  local repo="$1" dir="$2" commit="$3"
  if [[ ! -d "${dir}/.git" ]]; then
    mkdir -p "$(dirname "${dir}")"
    git clone --quiet "${repo}" "${dir}"
  fi
  if [[ -n "$(git -C "${dir}" status --porcelain)" ]]; then
    fail "${dir} has local modifications; refusing to change it"
  fi
  if [[ "$(git -C "${dir}" rev-parse HEAD)" != "${commit}" ]]; then
    git -C "${dir}" fetch --quiet origin
    git -C "${dir}" checkout --quiet --detach "${commit}"
  fi
  [[ "$(git -C "${dir}" rev-parse HEAD)" == "${commit}" ]] || fail "${dir} is not at ${commit}"
  log "$(basename "${dir}") at $(git -C "${dir}" rev-parse HEAD)"
}

# pid_alive reports whether the process recorded under name is running.
pid_alive() {
  local f="${PID_DIR}/$1.pid"
  [[ -f "$f" ]] && kill -0 "$(cat "$f")" 2>/dev/null
}

# stop_process stops the process group recorded under name and removes its pid file.
stop_process() {
  local name="$1" f="${PID_DIR}/$1.pid" pid i
  [[ -f "$f" ]] || return 0
  pid="$(cat "$f")"
  if kill -0 "${pid}" 2>/dev/null; then
    kill -TERM -- "-${pid}" 2>/dev/null || kill -TERM "${pid}" 2>/dev/null || true
    for i in $(seq 1 50); do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 0.2
    done
    kill -KILL -- "-${pid}" 2>/dev/null || true
    log "stopped ${name}"
  fi
  rm -f "$f"
}

# wait_http polls url until it answers 200 or attempts run out.
wait_http() {
  local url="$1" attempts="${2:-60}" i
  for i in $(seq 1 "${attempts}"); do
    if [[ "$(curl -s -o /dev/null -m 2 -w '%{http_code}' "${url}")" == "200" ]]; then
      return 0
    fi
    sleep 1
  done
  return 1
}
