#!/usr/bin/env bash
# Delete the local kind cluster created by scripts/setup_kind.sh.
# Usage:
#   bash scripts/teardown_kind.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

CLUSTER_NAME="${KIND_CLUSTER_NAME:-carbon-aware}"
export PATH="${ROOT_DIR}/.tools:${PATH}"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

resolve_bin() {
  local name="$1"
  local override="${2:-}"
  local candidate
  if [[ -n "${override}" ]] && command -v "${override}" >/dev/null 2>&1; then
    command -v "${override}"
    return 0
  fi
  for candidate in "${name}" "${name}.exe" "${ROOT_DIR}/.tools/${name}" "${ROOT_DIR}/.tools/${name}.exe"; do
    if command -v "${candidate}" >/dev/null 2>&1; then
      command -v "${candidate}"
      return 0
    fi
    if [[ -x "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  done
  die "${name} not found in PATH or ${ROOT_DIR}/.tools"
}

KIND="$(resolve_bin kind "${KIND:-}")"

if ! "${KIND}" get clusters 2>/dev/null | tr -d '\r' | grep -Fxq "${CLUSTER_NAME}"; then
  printf 'cluster %s not found; nothing to delete\n' "${CLUSTER_NAME}"
  exit 0
fi

printf 'deleting kind cluster %s\n' "${CLUSTER_NAME}"
"${KIND}" delete cluster --name "${CLUSTER_NAME}"
printf 'cluster %s deleted\n' "${CLUSTER_NAME}"
