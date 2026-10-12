#!/usr/bin/env bash
# Lists every runner-owned resource in the atespace: AX Tasks and Workspaces, and the
# Substrate actors and actor templates behind them. AX only lists what its own store
# holds, so an actor whose Task record was lost is visible only through kubectl ate.
#
# Usage: inventory.sh [--delete]
#
# Exits 0 when nothing with the runner's prefix is left, and 3 when something is. With
# --delete it removes what it finds first: Tasks and Workspaces through ax, then any actor
# still present from any state, then templates. An actor must go before its template;
# a template deleted first leaves an actor that can no longer be suspended.
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

delete=false
for arg in "$@"; do
  case "${arg}" in
    --delete) delete=true ;;
    *) fail "unknown option ${arg}" ;;
  esac
done
require_cmd ax kubectl kubectl-ate
use_kind_context

# names prints the prefixed names in the given table column, skipping the header.
names() {
  local column="$1"
  awk -v c="${column}" -v p="${RESOURCE_PREFIX}" 'NR > 1 && index($c, p) == 1 { print $c }'
}

tasks() { ax get tasks -a "${ATESPACE}" | names 1; }
workspaces() { ax get workspaces -a "${ATESPACE}" | names 1; }
actors() { kubectl ate get actors -a "${ATESPACE}" | names 2; }
templates() { kubectl ate get actor-templates -a "${ATESPACE}" | names 2; }

if [[ "${delete}" == true ]]; then
  for t in $(tasks); do ax delete task "${t}" -a "${ATESPACE}" || true; done
  for w in $(workspaces); do ax delete workspace "${w}" -a "${ATESPACE}" || true; done
  for a in $(actors); do kubectl ate delete actor "${a}" -a "${ATESPACE}" --any-state || true; done
  for t in $(templates); do kubectl ate delete actor-template "${t}" -a "${ATESPACE}" || true; done
fi

left=0
report() {
  local kind="$1" list="$2"
  if [[ -n "${list}" ]]; then
    left=1
    printf '%s:\n%s\n' "${kind}" "$(printf '%s\n' "${list}" | sed 's/^/  /')"
  else
    printf '%s: none\n' "${kind}"
  fi
}
log "resources named ${RESOURCE_PREFIX}* in atespace ${ATESPACE}"
report "AX Tasks" "$(tasks)"
report "AX Workspaces" "$(workspaces)"
report "Substrate actors" "$(actors)"
report "Substrate actor templates" "$(templates)"
if [[ "${left}" -ne 0 ]]; then
  echo "resources remain; stop the runner, then run inventory with --delete"
  exit 3
fi
echo "inventory empty"
