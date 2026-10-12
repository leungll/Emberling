#!/usr/bin/env bash
# Keeps a kubectl port-forward from the loopback port to the Substrate router's HTTP port
# open, starting it again whenever it exits. The runner reads sandbox results through this
# address; a brief gap while it restarts is a transient failure the runner tolerates.
# start.sh runs this in the background; it is not meant to be run by hand.
set -uo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

while true; do
  printf '%s port-forward starting\n' "$(date -u +%FT%TZ)"
  kubectl --context "${KUBE_CONTEXT}" port-forward --address 127.0.0.1 -n ate-system \
    svc/atenet-router "${ROUTER_LOCAL_PORT}:80"
  printf '%s port-forward exited with status %s\n' "$(date -u +%FT%TZ)" "$?"
  sleep 1
done
