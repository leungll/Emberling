#!/usr/bin/env bash
# Drives the AX sandbox deployment on a remote host over SSH. Every step ships the scripts
# in remote/ to ~/emberling-ax/deploy on the host and runs one of them there, in its own
# SSH session. The host is named only by the SSH alias given as the first argument; this
# script holds no host name, address or credential.
#
# Usage: axctl.sh <ssh-alias> <command> [options]
#
# Commands, in deployment order:
#   prepare                  install the pinned Docker, Go, kubectl, kind, ko, git and make
#   cluster [--recreate]     check out the pinned sources, create the kind cluster and
#           [--reinstall]    install Substrate
#   control-plane            deploy Redis, ax-server and the WorkerPool, build ax and
#                            kubectl-ate, create the atespace
#   image [--source DIR]     send DIR (default: this repository) at its HEAD commit, build
#                            and push the sandbox supervisor image, print its digest
#   start [--env-file FILE]  start PostgreSQL, the mock services, the router port-forward,
#         [--studio]         the sandbox runner and the Backend (and Studio)
#   kill-restart NAME        SIGKILL backend or sandboxrunner and start it again
#   status                   show the host processes and their health checks
#   stop                     stop the host processes and PostgreSQL
#   inventory [--delete]     list (and delete) runner-owned resources in the atespace
#   teardown [--purge-data]  stop everything and delete the kind cluster and registry
#   all                      prepare, cluster, control-plane, image, start, inventory
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "${here}/../.." && pwd)"

usage() { sed -n '7,24p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
[[ $# -ge 2 ]] || usage
host="$1"
cmd="$2"
shift 2

# remote runs a script from the shipped directory with its arguments quoted for the
# remote shell.
remote() {
  local script="$1" quoted=""
  shift
  if [[ $# -gt 0 ]]; then
    quoted="$(printf ' %q' "$@")"
  fi
  ssh "${host}" "bash emberling-ax/deploy/${script}${quoted}"
}

# The host's GNU tar is told to ignore the extended attributes a macOS tar adds.
ship_scripts() {
  COPYFILE_DISABLE=1 tar -C "${here}/remote" -czf - . |
    ssh "${host}" 'mkdir -p emberling-ax/deploy && tar --warning=no-unknown-keyword -C emberling-ax/deploy -xzf - && chmod +x emberling-ax/deploy/*.sh'
}

# ship_source replaces the host copy of the Emberling source with the tree of the given
# repository at HEAD. Only committed content is sent, and no Git credentials are needed
# on the host. It prints the short revision for the image tag.
ship_source() {
  local src="$1" rev
  git -C "${src}" rev-parse --verify HEAD >/dev/null
  rev="$(git -C "${src}" rev-parse --short=12 HEAD)"
  if [[ -n "$(git -C "${src}" status --porcelain --untracked-files=no)" ]]; then
    echo "note: ${src} has uncommitted changes; only HEAD ${rev} is sent" >&2
  fi
  git -C "${src}" archive --format=tar HEAD |
    ssh "${host}" 'rm -rf emberling-ax/emberling && mkdir -p emberling-ax/emberling && tar -C emberling-ax/emberling -xf -'
  echo "${rev}"
}

ship_env_file() {
  local file="$1"
  [[ -f "${file}" ]] || { echo "error: env file ${file} not found" >&2; exit 2; }
  ssh "${host}" 'mkdir -p emberling-ax/run/state && umask 077 && cat > emberling-ax/run/state/operator.env' <"${file}"
}

run_image() {
  local src="${repo_root}" rev
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --source) src="$2"; shift 2 ;;
      *) echo "error: unknown option $1" >&2; exit 2 ;;
    esac
  done
  rev="$(ship_source "${src}")"
  remote image.sh "${rev}"
}

run_start() {
  local env_file="" args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --env-file) env_file="$2"; shift 2 ;;
      --studio) args+=(--studio); shift ;;
      *) echo "error: unknown option $1" >&2; exit 2 ;;
    esac
  done
  if [[ -z "${env_file}" && -f "${here}/.env" ]]; then
    env_file="${here}/.env"
  fi
  if [[ -n "${env_file}" ]]; then
    ship_env_file "${env_file}"
  else
    ssh "${host}" 'rm -f emberling-ax/run/state/operator.env'
  fi
  remote start.sh "${args[@]+"${args[@]}"}"
}

ship_scripts
case "${cmd}" in
  prepare) remote prepare.sh "$@" ;;
  cluster) remote cluster.sh "$@" ;;
  control-plane) remote control-plane.sh "$@" ;;
  image) run_image "$@" ;;
  start) run_start "$@" ;;
  kill-restart) remote start.sh --kill-restart "$@" ;;
  status) remote status.sh "$@" ;;
  stop) remote stop.sh "$@" ;;
  inventory) remote inventory.sh "$@" ;;
  teardown) remote teardown.sh "$@" ;;
  all)
    remote prepare.sh
    remote cluster.sh
    remote control-plane.sh
    run_image
    run_start "$@"
    remote inventory.sh
    ;;
  *) usage ;;
esac
