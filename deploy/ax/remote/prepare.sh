#!/usr/bin/env bash
# Installs the pinned host tools on an x86-64 Ubuntu 24.04 host: Docker from Docker's apt
# repository, Go, kubectl, kind and ko, plus git, make, curl, jq and openssl. Safe to run
# again: a tool already at its pinned version is left alone.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

[[ "$(uname -m)" == "x86_64" ]] || fail "this deployment needs an x86-64 host"
# shellcheck disable=SC1091
. /etc/os-release
[[ "${ID}" == "ubuntu" && "${VERSION_ID}" == "24.04" ]] || fail "this deployment needs Ubuntu 24.04"

log "base packages"
sudo apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl git make jq openssl gnupg >/dev/null

if [[ "$(dpkg-query -W -f='${Version}' docker-ce 2>/dev/null || true)" != "${DOCKER_CE_VERSION}" ]]; then
  log "Docker ${DOCKER_CE_VERSION}"
  sudo install -m 0755 -d /etc/apt/keyrings
  sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  sudo chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" \
    | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
  sudo apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --allow-downgrades \
    "docker-ce=${DOCKER_CE_VERSION}" "docker-ce-cli=${DOCKER_CE_VERSION}" \
    "containerd.io=${CONTAINERD_VERSION}" "docker-buildx-plugin=${BUILDX_VERSION}" >/dev/null
fi
if ! id -nG "${USER}" | tr ' ' '\n' | grep -qx docker; then
  sudo usermod -aG docker "${USER}"
  log "added ${USER} to the docker group; it applies to the next SSH session"
fi

if [[ "$(/usr/local/go/bin/go version 2>/dev/null || true)" != "go version go${GO_VERSION} linux/amd64" ]]; then
  log "Go ${GO_VERSION}"
  tmp="$(mktemp -d)"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o "${tmp}/go.tgz"
  echo "${GO_SHA256}  ${tmp}/go.tgz" | sha256sum -c --quiet -
  sudo rm -rf /usr/local/go
  sudo tar -C /usr/local -xzf "${tmp}/go.tgz"
  rm -rf "${tmp}"
fi

if [[ "$(kubectl version --client -o json 2>/dev/null | jq -r .clientVersion.gitVersion || true)" != "${KUBECTL_VERSION}" ]]; then
  log "kubectl ${KUBECTL_VERSION}"
  tmp="$(mktemp -d)"
  curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o "${tmp}/kubectl"
  echo "${KUBECTL_SHA256}  ${tmp}/kubectl" | sha256sum -c --quiet -
  sudo install -m 0755 "${tmp}/kubectl" /usr/local/bin/kubectl
  rm -rf "${tmp}"
fi

if [[ "$(kind version 2>/dev/null | awk '{print $2}' || true)" != "${KIND_VERSION}" ]]; then
  log "kind ${KIND_VERSION}"
  GOFLAGS= go install "sigs.k8s.io/kind@${KIND_VERSION}"
fi
if [[ "$(ko version 2>/dev/null || true)" != "${KO_VERSION}" ]]; then
  log "ko ${KO_VERSION}"
  GOFLAGS= go install "github.com/google/ko@${KO_VERSION}"
fi

log "tool versions"
dpkg-query -W -f='docker-ce ${Version}\n' docker-ce
go version
kubectl version --client | head -1
kind version
echo "ko $(ko version)"
git --version
make --version | head -1
