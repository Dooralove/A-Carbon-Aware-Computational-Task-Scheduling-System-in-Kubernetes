# Протокол экспериментов

> Как гонять стенд, чтобы результаты можно было защищать в дипломе.

## Стенд

- Кластер: kind, имя `carbon-aware`, манифест `deployments/kind/cluster.yaml`
- Наблюдаемость: `deployments/prometheus`, `deployments/grafana`
- Подъём: `make kind-up` / снос: `make kind-down`

## Артефакты репозитория

| Каталог | Содержимое |
| --- | --- |
| `experiments/workloads/` | Манифесты нагрузок |
| `experiments/datasets/` | Входные ряды интенсивности (не сырые гигабайты логов) |
| `experiments/scenarios/` | Описание прогонов (baseline vs carbon-aware) |
| `experiments/results/` | Краткие отчёты; объёмные файлы в git не складывать |
| `analysis/` | Ноутбуки и скрипты постобработки |

## Базовые сценарии

TBD (T0.2): baseline kube-scheduler, carbon-aware, чувствительность к прогнозу, отказ источника углерода.

## Критерии успеха прогона

TBD: полнота метрик, отсутствие ручных правок кластера вне сценария, фиксированные сиды данных.
