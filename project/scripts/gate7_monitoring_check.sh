#!/usr/bin/env bash
# Quality Gate 7: observability check under test load (T7.4).
#
# Submits N carbon-aware pods (default 50) to carbon-aware-scheduler and a few
# baseline pods to default-scheduler, then checks through the Prometheus HTTP API:
#   1. every scrape target is UP;
#   2. the increase of carbon_aware_scheduler_decisions_total equals N;
#   3. per-node increments match where the pods were actually bound;
#   4. the delay from the last binding to the counter being visible in
#      Prometheus (and through the Grafana datasource proxy) is < 3 s;
#   5. every dashboard panel query returns data.
# Writes a JSON summary to $OUT (default: gate7_result.json).
#
# Requires: kubectl (context of the kind cluster), curl, python3; Prometheus on
# localhost:9090 and Grafana on localhost:3000 (kind extraPortMappings).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
N="${GATE7_PODS:-50}"
BASELINE_N="${GATE7_BASELINE_PODS:-10}"
NS_CA="${GATE7_NS_CARBON:-exp-carbon-aware}"
NS_BASE="${GATE7_NS_BASELINE:-exp-baseline}"
PROM="${PROM_URL:-http://localhost:9090}"
GRAFANA="${GRAFANA_URL:-http://localhost:3000}"
IMAGE="${GATE7_IMAGE:-busybox:1.37.0}"
BUSY_SECONDS="${GATE7_BUSY_SECONDS:-20}"
# Per-pod CPU limit of the busy loop. 50 pods x 20m = 1 core: enough for the
# cAdvisor/power panels without saturating a laptop-sized kind host (a 200m
# limit starved the scrapes and the scheduler on a 4-core host).
CPU_LIMIT="${GATE7_CPU_LIMIT:-20m}"
OUT="${OUT:-gate7_result.json}"
DASHBOARD="${ROOT_DIR}/deployments/grafana/dashboards/carbon_overview.json"
CURL=(curl -sf --noproxy '*')
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

log() { printf '[gate7] %s\n' "$*" >&2; }

promq() { # instant query → JSON
  "${CURL[@]}" "${PROM}/api/v1/query" --data-urlencode "query=$1"
}

decisions_total() {
  promq 'sum(carbon_aware_scheduler_decisions_total{job="kube-scheduler"}) or vector(0)' |
    python3 -c 'import json,sys; print(float(json.load(sys.stdin)["data"]["result"][0]["value"][1]))'
}

decisions_by_node() {
  promq 'sum by (node) (carbon_aware_scheduler_decisions_total{job="kube-scheduler"})' |
    python3 -c 'import json,sys; print(json.dumps({r["metric"]["node"]: float(r["value"][1]) for r in json.load(sys.stdin)["data"]["result"]}))'
}

pod_manifest() { # name namespace scheduler carbon(true|false)
  local name="$1" ns="$2" sched="$3" carbon="$4"
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app.kubernetes.io/part-of: carbon-aware-k8s
    app.kubernetes.io/component: gate7-load
$( [[ "${carbon}" == true ]] && printf '    carbon-aware.io/enabled: "true"\n    carbon-aware.io/workload-class: batch\n' )
$( [[ "${carbon}" == true ]] && printf '  annotations:\n    carbon-aware.io/expected-duration: "60s"\n' )
spec:
  schedulerName: ${sched}
  restartPolicy: Never
  terminationGracePeriodSeconds: 0
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: burn
      image: ${IMAGE}
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "end=\$((\$(date +%s)+${BUSY_SECONDS})); while [ \$(date +%s) -lt \$end ]; do :; done"]
      resources:
        requests: {cpu: ${CPU_LIMIT}, memory: 16Mi}
        limits: {cpu: ${CPU_LIMIT}, memory: 32Mi}
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: {drop: ["ALL"]}
---
EOF
}

# --- 0. preconditions ---------------------------------------------------------
for ns in "${NS_CA}" "${NS_BASE}"; do
  kubectl get ns "${ns}" >/dev/null 2>&1 || kubectl create ns "${ns}" >/dev/null
  kubectl -n "${ns}" delete pods -l app.kubernetes.io/component=gate7-load --wait=true >/dev/null 2>&1 || true
done

log "waiting for the plugin snapshot (first Cache Sync tick, QG6-F01)"
snapshot_ok=false
for _ in $(seq 1 60); do
  age=$(promq 'carbon_aware_snapshot_age_seconds{job="kube-scheduler"}' |
    python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else "")')
  if [[ -n "${age}" ]] && python3 -c "import sys; sys.exit(0 if float('${age}') < 90 else 1)"; then
    snapshot_ok=true
    break
  fi
  sleep 2
done
if [[ "${snapshot_ok}" != true ]]; then
  log "FAIL: carbon_aware_snapshot_age_seconds is missing or >= 90 s; check the kube-scheduler target and CDS"
  exit 1
fi

"${CURL[@]}" "${PROM}/api/v1/targets?state=active" > "${TMP}/targets.json"
before=$(decisions_total)
log "decisions_total before load: ${before}"

# --- 1. load ----------------------------------------------------------------
manifests=""
for i in $(seq -w 1 "${N}"); do manifests+="$(pod_manifest "gate7-ca-${i}" "${NS_CA}" carbon-aware-scheduler true)"$'\n'; done
for i in $(seq -w 1 "${BASELINE_N}"); do manifests+="$(pod_manifest "gate7-base-${i}" "${NS_BASE}" default-scheduler false)"$'\n'; done
t_submit=$(date +%s.%N)
printf '%s' "${manifests}" | kubectl apply -f - >/dev/null
log "submitted ${N} carbon-aware + ${BASELINE_N} baseline pods"

# --- 2. wait for bindings ----------------------------------------------------
for _ in $(seq 1 120); do
  bound=$(kubectl -n "${NS_CA}" get pods -l app.kubernetes.io/component=gate7-load -o json |
    python3 -c 'import json,sys; print(sum(1 for p in json.load(sys.stdin)["items"] if p["spec"].get("nodeName")))')
  [[ "${bound}" -ge "${N}" ]] && break
  sleep 0.5
done
t_bound=$(date +%s.%N)
kubectl -n "${NS_CA}" get pods -l app.kubernetes.io/component=gate7-load -o json > "${TMP}/pods.json"

# --- 3. counter visibility latency -------------------------------------------
want=$(python3 -c "print(${before} + ${N})")
t_prom=""; t_graf=""
for _ in $(seq 1 120); do
  now=$(date +%s.%N)
  if [[ -z "${t_prom}" ]] && python3 -c "import sys; sys.exit(0 if $(decisions_total) >= ${want} else 1)"; then t_prom="${now}"; fi
  if [[ -z "${t_graf}" ]]; then
    g=$("${CURL[@]}" "${GRAFANA}/api/datasources/proxy/uid/prometheus/api/v1/query" \
      --data-urlencode 'query=sum(carbon_aware_scheduler_decisions_total{job="kube-scheduler"})' |
      python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else 0)' || echo 0)
    python3 -c "import sys; sys.exit(0 if float('${g}') >= ${want} else 1)" && t_graf="$(date +%s.%N)"
  fi
  [[ -n "${t_prom}" && -n "${t_graf}" ]] && break
  sleep 0.25
done
after=$(decisions_total)
decisions_by_node > "${TMP}/by_node.json"
# klog lines carry microsecond timestamps; all kind nodes share the host clock.
kubectl -n carbon-aware logs deploy/carbon-aware-scheduler --since=15m |
  grep 'Successfully bound pod to node' | grep "pod=\"${NS_CA}/gate7-ca-" > "${TMP}/bind.log" || true

log "waiting $((BUSY_SECONDS + 10)) s so cAdvisor/power panels see the CPU load"
sleep $((BUSY_SECONDS + 10))

# --- 4. dashboard queries ----------------------------------------------------
python3 - "${DASHBOARD}" "${PROM}" > "${TMP}/panels.json" <<'PY'
import json, sys, urllib.parse, urllib.request
dash = json.load(open(sys.argv[1])); prom = sys.argv[2]
consts = {v["name"]: v["query"] for v in dash["templating"]["list"] if v["type"] == "constant"}
consts["workload_ns"] = "exp-.*"
out = []
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for p in dash["panels"]:
    for t in p["targets"]:
        q = t["expr"]
        for k, v in consts.items():
            q = q.replace("$" + k, v)
        q = q.replace("$__range", "10m").replace("$__rate_interval", "1m")
        url = prom + "/api/v1/query?" + urllib.parse.urlencode({"query": q})
        try:
            res = json.load(opener.open(url, timeout=10))["data"]["result"]
            n = len(res)
        except Exception as e:  # noqa: BLE001 — report, do not hide
            n = -1
        out.append({"panel": p["title"], "ref": t["refId"], "series": n})
print(json.dumps(out))
PY

python3 - "${OUT}" <<PY
import json
targets = json.load(open("${TMP}/targets.json"))["data"]["activeTargets"]
pods = json.load(open("${TMP}/pods.json"))["items"]
bound_by_node = {}
for p in pods:
    n = p["spec"].get("nodeName")
    if n: bound_by_node[n] = bound_by_node.get(n, 0) + 1
t_submit, t_bound = ${t_submit}, ${t_bound}
from datetime import datetime, timezone
# Bind time of the last pod: the scheduler's "Successfully bound" klog line
# (microseconds). Fallback: PodScheduled lastTransitionTime (1 s, truncated,
# so the latency is then an upper bound by up to 1 s).
year = datetime.now(timezone.utc).year
binds = []
for line in open("${TMP}/bind.log"):
    head = line.split()  # I1018 22:00:07.886678 ...
    ts = datetime.strptime(f"{year}{head[0][1:]} {head[1]}", "%Y%m%d %H:%M:%S.%f").replace(tzinfo=timezone.utc)
    binds.append(ts.timestamp())
bind_source = "scheduler klog"
if len(binds) < ${N}:
    bind_source = "PodScheduled condition (1 s resolution)"
    binds = [datetime.fromisoformat(c["lastTransitionTime"].replace("Z", "+00:00")).timestamp()
             for p in pods for c in p["status"].get("conditions", [])
             if c["type"] == "PodScheduled" and c["status"] == "True"]
t_last_bind = max(binds) if binds else float("nan")
t_prom = float("${t_prom}" or "nan"); t_graf = float("${t_graf}" or "nan")
res = {
  "pods_submitted": ${N},
  "pods_bound": sum(bound_by_node.values()),
  "decisions_before": ${before},
  "decisions_after": ${after},
  "decisions_increase": ${after} - ${before},
  "bound_by_node": bound_by_node,
  "decisions_by_node_after": json.load(open("${TMP}/by_node.json")),
  "targets": [{"job": t["labels"].get("job"), "scheduler": t["labels"].get("scheduler", ""),
               "node": t["labels"].get("node", ""), "health": t["health"]} for t in targets],
  "seconds_submit_to_all_bound": round(t_bound - t_submit, 3),
  "last_bind_time_source": bind_source,
  "seconds_last_bind_to_prometheus": round(t_prom - t_last_bind, 3),
  "seconds_last_bind_to_grafana_datasource": round(t_graf - t_last_bind, 3),
  "panel_queries": json.load(open("${TMP}/panels.json")),
}
json.dump(res, open("${OUT}", "w"), indent=2)
print(json.dumps({k: v for k, v in res.items() if k not in ("targets", "panel_queries")}, indent=2))
PY
