#!/usr/bin/env bash
# T10.1 calibration: run the workload image in the kind cluster with the
# T ∈ {30, 60, 300} s settings and collect the task's own JSON result lines.
# Usage (from project/): bash experiments/workloads/calibrate.sh [out.jsonl]
set -euo pipefail
NS=${NS:-carbon-exp}
IMAGE=${IMAGE:-carbon-workload-runner:0.1.0}
OUT=${1:-experiments/results/calibration_t10_1.jsonl}
mkdir -p "$(dirname "$OUT")"

# name | args (default-scheduler: calibration does not depend on placement)
CASES=(
  "cal-60s-1000m-sha256|--duration=60s --cpu=1 --kernel=sha256"
  "cal-60s-1000m-matmul|--duration=60s --cpu=1 --kernel=matmul"
  "cal-60s-100m-sha256|--duration=60s --cpu=0.1 --kernel=sha256"
  "cal-30s-50m-sha256|--duration=30s --cpu=0.05 --kernel=sha256 --memory-mib=32"
  "cal-300s-100m-sha256|--duration=300s --cpu=0.1 --kernel=sha256 --memory-mib=64"
)

kubectl -n "$NS" delete pod -l app.kubernetes.io/name=carbon-calibration --ignore-not-found --wait=true >/dev/null
for c in "${CASES[@]}"; do
  name=${c%%|*}; args=${c#*|}
  argjson=$(printf '%s\n' $args --task-id="$name" | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))')
  cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $name
  namespace: $NS
  labels: {app.kubernetes.io/name: carbon-calibration}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: task
      image: $IMAGE
      imagePullPolicy: IfNotPresent
      args: $argjson
      resources:
        requests: {cpu: 1000m, memory: 128Mi}
        limits: {memory: 128Mi}
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
YAML
done
echo "waiting for ${#CASES[@]} calibration pods (longest: 300 s)"
kubectl -n "$NS" wait pod -l app.kubernetes.io/name=carbon-calibration --for=jsonpath='{.status.phase}'=Succeeded --timeout=420s >/dev/null
: > "$OUT"
for c in "${CASES[@]}"; do
  kubectl -n "$NS" logs "${c%%|*}" -c task | tail -n 1 >> "$OUT"
done
kubectl -n "$NS" delete pod -l app.kubernetes.io/name=carbon-calibration --wait=false >/dev/null
python3 - "$OUT" <<'PY'
import json, sys
print(f"{'task':28} {'T_req,s':>8} {'T_act,s':>8} {'dev,s':>7} {'cpu':>5} {'util':>6}")
for line in open(sys.argv[1]):
    r = json.loads(line)
    dev = r["actual_duration_s"] - r["requested_duration_s"]
    print(f"{r['task_id']:28} {r['requested_duration_s']:8.0f} {r['actual_duration_s']:8.3f} {dev:7.3f} {r['cpu_cores']:5} {r['cpu_utilization']:6.3f}")
PY
