# Grafana

Дашборд углеродо-ориентированного планирования (T7.3). Источник правды по именам метрик — `docs/ssot/metrics.md`.

```bash
kubectl apply -k deployments/prometheus   # namespace monitoring + Prometheus
kubectl apply -k deployments/grafana      # Grafana + datasource + дашборд
```

Открыть: <http://localhost:3000> (NodePort 30300 → host 3000). Анонимный доступ с ролью Viewer; дашборд `Carbon-Aware Scheduling Overview` открывается как домашний.

| Файл | Содержимое |
| --- | --- |
| `dashboards/carbon_overview.json` | JSON-модель дашборда (uid `carbon-overview`) |
| `grafana-deployment.yaml` | provisioning datasource `Prometheus` (uid `prometheus`) и провайдера дашбордов, Deployment, Service |
| `kustomization.yaml` | `configMapGenerator` собирает ConfigMap `grafana-dashboards` из `dashboards/*.json` |

Изменения JSON подхватываются без рестарта: `kubectl apply -k deployments/grafana`, Grafana перечитывает файлы раз в 30 с.

## Панели

| № | Панель | Запрос (суть) |
| --- | --- | --- |
| — | CDS data age, Plugin snapshot age, Pods placed, Fallbacks | `carbon_aware_data_age_seconds`, `carbon_aware_snapshot_age_seconds`, `sum(carbon_aware_scheduler_decisions_total)`, `increase(carbon_aware_scheduler_fallback_total[$__range])` |
| 1 | Carbon intensity by zone | `carbon_aware_zone_intensity_gco2_kwh` |
| 2 | Scheduler decisions by zone and node | `sum by (zone, node) (carbon_aware_scheduler_decisions_total)` |
| 3 | Estimated cumulative carbon footprint | ex-post: CPU пода из cAdvisor × (P_max − P_idle)/ядра × CI зоны узла, интеграл по диапазону, по namespace стратегии; ex-ante: `carbon_aware_pod_estimated_carbon_gco2_sum` |
| 4 | Estimated node power (W) | P(u) = P_idle + (P_max − P_idle)·u, u из cAdvisor |
| 5 | Scheduling latency p50/p95/p99 | `scheduler_scheduling_attempt_duration_seconds` по `scheduler` (default vs carbon-aware) |
| 5b | CarbonAware extension point p99 | `carbon_aware_plugin_duration_seconds` и `scheduler_framework_extension_point_duration_seconds` |
| 6 | Fallback events & data freshness | `carbon_aware_scheduler_fallback_total` по `reason` + возраст снимков с порогами 90/300 с |

## Соглашение для сравнения Baseline vs Carbon-Aware

Панель 3 разделяет стратегии по namespace. Подавайте нагрузку каждой стратегии в свой namespace:
`exp-baseline` (default-scheduler), `exp-carbon-only` (carbon-only-scheduler), `exp-carbon-aware` (carbon-aware-scheduler).
Переменная `workload_ns` (по умолчанию `exp-.*`) задаёт, какие namespace попадают на панель.

Константы `p_idle` = 40 W и `p_max` = 100 W совпадают с `pluginConfig` планировщика; при смене профиля мощности поменяйте их в JSON.
Оценка ex-post на панели 3 учитывает только маргинальную мощность подов; полный \(CF_{\mathrm{cluster}}\) с idle считается офлайн (metrics.md §4).
