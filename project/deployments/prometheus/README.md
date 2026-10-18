# Prometheus

Стек мониторинга стенда kind (T7.2). Применение:

```bash
kubectl apply -k deployments/prometheus
```

| Файл | Содержимое |
| --- | --- |
| `prometheus-rbac.yaml` | namespace `monitoring`, ServiceAccount и ClusterRole только на чтение (discovery, `nodes/metrics`, non-resource `/metrics`) |
| `prometheus-config.yaml` | ConfigMap `prometheus.yml`: scrape interval 2s, jobs `kube-scheduler`, `carbon-data-service`, `cadvisor`, `node-exporter` |
| `prometheus-deployment.yaml` | Deployment + Service NodePort 30090 (host 9090) |
| `node-exporter.yaml` | DaemonSet node-exporter |

Job `kube-scheduler` собирает оба планировщика и ставит лейбл `scheduler=default-scheduler|carbon-aware-scheduler`.
Встроенный kube-scheduler kind слушает `0.0.0.0` благодаря `kubeadmConfigPatches` в `deployments/kind/cluster-config.yaml`; без пересоздания кластера этот таргет будет DOWN.

Проверка: `http://localhost:9090/targets` — все таргеты UP. Имена метрик — `docs/ssot/metrics.md`.
