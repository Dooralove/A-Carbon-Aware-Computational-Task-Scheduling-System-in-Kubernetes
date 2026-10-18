#!/usr/bin/env bash
# Create (or reuse) the local kind cluster and label logical energy zones.
# Usage:
#   bash scripts/setup_kind.sh
#   KIND_RECREATE=1 bash scripts/setup_kind.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

CLUSTER_NAME="${KIND_CLUSTER_NAME:-carbon-aware}"
KIND_CONFIG="${KIND_CONFIG:-${ROOT_DIR}/deployments/kind/cluster-config.yaml}"
WAIT_TIMEOUT="${KIND_WAIT:-10m}"
WORKER_COUNT="${KIND_WORKER_COUNT:-4}"
ZONE_LABEL="carbon-aware.io/zone"
POWER_LABEL="carbon-aware.io/power-profile"
POWER_PROFILE="${KIND_POWER_PROFILE:-standard}"
# Logical worker-01..04 in architecture SSOT §8.1 / spec §46.
EXPECTED_ZONES=(north north west west)

export PATH="${ROOT_DIR}/.tools:${PATH}"

log() { printf '%s\n' "$*"; }
err() { printf 'error: %s\n' "$*" >&2; }

die() {
  err "$*"
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
  die "${name} not found in PATH or ${ROOT_DIR}/.tools (install kind v0.33+ and kubectl 1.37.x)"
}

strip_cr() {
  tr -d '\r'
}

first_line() {
  local text="${1:-}"
  printf '%s\n' "${text%%$'\n'*}"
}

cluster_exists() {
  "${KIND}" get clusters 2>/dev/null | strip_cr | grep -Fxq "${CLUSTER_NAME}"
}

node_label() {
  local node="$1" key="$2"
  "${KUBECTL}" get node "${node}" -o go-template="{{index .metadata.labels \"${key}\"}}" 2>/dev/null | strip_cr || true
}

node_ready() {
  local node="$1"
  "${KUBECTL}" get node "${node}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' | strip_cr
}

list_worker_nodes() {
  "${KUBECTL}" get nodes \
    --selector='!node-role.kubernetes.io/control-plane' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' \
    | strip_cr | sed '/^$/d' | LC_ALL=C sort
}

dump_debug() {
  err "--- cluster debug ---"
  "${KUBECTL}" get nodes -o wide || true
  "${KUBECTL}" get pods -A -o wide || true
}

require_docker() {
  if ! command -v docker >/dev/null 2>&1 && ! command -v docker.exe >/dev/null 2>&1; then
    die "docker not found; kind needs a running Docker engine"
  fi
  if ! docker info >/dev/null 2>&1; then
    die "docker is installed but the engine is not running (start Docker Desktop / dockerd)"
  fi
}

[[ -f "${KIND_CONFIG}" ]] || die "kind config not found: ${KIND_CONFIG}"

KIND="$(resolve_bin kind "${KIND:-}")"
KUBECTL="$(resolve_bin kubectl "${KUBECTL:-}")"

# Git Bash on Windows often has a different HOME than the kubeconfig kind writes.
if [[ -z "${KUBECONFIG:-}" ]]; then
  if [[ -n "${USERPROFILE:-}" ]]; then
    export KUBECONFIG="${USERPROFILE}/.kube/config"
  else
    export KUBECONFIG="${HOME}/.kube/config"
  fi
fi

kind_ver="$(first_line "$("${KIND}" version 2>/dev/null | strip_cr || true)")"
kubectl_ver="$(first_line "$("${KUBECTL}" version --client 2>/dev/null | strip_cr || true)")"
log "kind:    ${KIND} (${kind_ver:-unknown})"
log "kubectl: ${KUBECTL} (${kubectl_ver:-unknown})"
log "config:  ${KIND_CONFIG}"
log "cluster: ${CLUSTER_NAME}"

require_docker

if [[ "${KIND_RECREATE:-0}" == "1" ]] && cluster_exists; then
  log "KIND_RECREATE=1: deleting existing cluster ${CLUSTER_NAME}"
  "${KIND}" delete cluster --name "${CLUSTER_NAME}"
fi

if cluster_exists; then
  log "cluster ${CLUSTER_NAME} already exists; reusing (KIND_RECREATE=1 to recreate)"
else
  create_args=(create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG}" --wait "${WAIT_TIMEOUT}")
  if [[ -n "${KIND_NODE_IMAGE:-}" ]]; then
    create_args+=(--image "${KIND_NODE_IMAGE}")
  fi
  log "creating kind cluster (${create_args[*]})"
  "${KIND}" "${create_args[@]}"
fi

log "exporting kubeconfig for ${CLUSTER_NAME}"
"${KIND}" export kubeconfig --name "${CLUSTER_NAME}"
log "kubeconfig: ${KUBECONFIG}"

log "waiting for all nodes to be Ready (timeout ${WAIT_TIMEOUT})"
if ! "${KUBECTL}" wait --for=condition=Ready node --all --timeout="${WAIT_TIMEOUT}"; then
  dump_debug
  die "nodes did not become Ready"
fi

mapfile -t WORKERS < <(list_worker_nodes)
if [[ "${#WORKERS[@]}" -ne "${WORKER_COUNT}" ]]; then
  dump_debug
  die "expected ${WORKER_COUNT} worker nodes, found ${#WORKERS[@]}: ${WORKERS[*]:-none}"
fi

log "labelling logical energy zones (worker-01..0${WORKER_COUNT})"
failed=0
for i in "${!WORKERS[@]}"; do
  node="${WORKERS[$i]}"
  logical="$(printf 'worker-%02d' "$((i + 1))")"
  zone="${EXPECTED_ZONES[$i]}"
  log "  ${logical}  ${node}  ${ZONE_LABEL}=${zone}  ${POWER_LABEL}=${POWER_PROFILE}"
  "${KUBECTL}" label node "${node}" \
    "${ZONE_LABEL}=${zone}" \
    "${POWER_LABEL}=${POWER_PROFILE}" \
    --overwrite >/dev/null
done

log "verifying labels"
for i in "${!WORKERS[@]}"; do
  node="${WORKERS[$i]}"
  logical="$(printf 'worker-%02d' "$((i + 1))")"
  zone="${EXPECTED_ZONES[$i]}"
  got_zone="$(node_label "${node}" "${ZONE_LABEL}")"
  got_power="$(node_label "${node}" "${POWER_LABEL}")"
  ready="$(node_ready "${node}")"
  if [[ "${ready}" != "True" ]]; then
    err "  FAIL ${logical} (${node}): Ready=${ready} want True"
    failed=1
  fi
  if [[ "${got_zone}" != "${zone}" ]]; then
    err "  FAIL ${logical} (${node}): ${ZONE_LABEL}=${got_zone} want ${zone}"
    failed=1
  fi
  if [[ "${got_power}" != "${POWER_PROFILE}" ]]; then
    err "  FAIL ${logical} (${node}): ${POWER_LABEL}=${got_power} want ${POWER_PROFILE}"
    failed=1
  fi
  if [[ "${ready}" == "True" && "${got_zone}" == "${zone}" && "${got_power}" == "${POWER_PROFILE}" ]]; then
    log "  OK   ${logical} (${node}) Ready ${ZONE_LABEL}=${got_zone} ${POWER_LABEL}=${got_power}"
  fi
done

if [[ "${failed}" -ne 0 ]]; then
  dump_debug
  die "node label verification failed"
fi

log ""
log "kubectl get nodes --show-labels"
"${KUBECTL}" get nodes --show-labels
log ""
log "cluster ${CLUSTER_NAME} is ready (4 workers labelled north/north/west/west)."
log "teardown: bash scripts/teardown_kind.sh"
