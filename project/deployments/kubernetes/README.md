# Kubernetes manifests for the carbon-aware stand (Stage 6).

Images must already be loaded (`make kind-load`). Kind `kube-scheduler` is not replaced.

## Apply order

```bash
kubectl apply -k deployments/kubernetes
```

or file-by-file:

Test pods (after both Deployments are Ready):

```bash
kubectl apply -f deployments/kubernetes/pod-baseline.yaml
kubectl apply -f deployments/kubernetes/pod-carbon-aware.yaml
```

Client-side validation:

```bash
kubectl apply --dry-run=client --validate=true -k deployments/kubernetes
kubectl apply --dry-run=client --validate=true -f deployments/kubernetes/pod-baseline.yaml
kubectl apply --dry-run=client --validate=true -f deployments/kubernetes/pod-carbon-aware.yaml
```

## Files

| File | Contents |
| --- | --- |
| `rbac.yaml` | Namespace `carbon-aware`, ServiceAccounts, ClusterRole (not `cluster-admin`), bindings |
| `scheduler-config.yaml` | ConfigMap with `KubeSchedulerConfiguration` profiles C (`carbon-aware-scheduler`, multiPoint) and B (`carbon-only-scheduler`) |
| `scheduler-deployment.yaml` | Custom kube-scheduler Deployment, `--leader-elect=false`, metrics Service `:10259` |
| `carbon-service-deployment.yaml` | CDS Deployment/Service NodePort `30080`, ConfigMap replay, NetworkPolicy |
| `pod-baseline.yaml` | `schedulerName: default-scheduler` |
| `pod-carbon-aware.yaml` | `schedulerName: carbon-aware-scheduler` + `carbon-aware.io/*` |

CDS in-cluster URL: `http://carbon-data-service.carbon-aware.svc:8080`.
