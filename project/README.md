# Carbon-Aware Kubernetes Scheduler

Дипломный проект: **углеродо-ориентированное планирование вычислительных задач в Kubernetes**.

Репозиторий организован по [Standard Go Project Layout](https://github.com/golang-standards/project-layout). Модуль Go: `carbon-aware-k8s`.

Точки входа: `cmd/scheduler` (kube-scheduler + плагин `CarbonAware`) и `cmd/carbon-service`. Образы стенда собираются из `build/Dockerfile.*`.

## Структура

```
cmd/                  # точки входа (scheduler, carbon-service)
pkg/                  # переиспользуемые пакеты
api/v1alpha1/         # CRD / API-типы
deployments/          # Kubernetes, kind, Prometheus, Grafana
experiments/          # нагрузки, датасеты, сценарии, результаты
analysis/             # ноутбуки и скрипты анализа
tests/                # unit, integration, e2e
docs/ssot/            # единый источник правды
docs/research/        # исследовательские заметки
docs/diploma_text/    # текст диплома
docs/decisions_adr/   # архитектурные решения
docs/audit/           # журналы проверок
```

## Сборка и стенд

Требуются GNU Make, Go 1.26+, `golangci-lint`, [kind](https://kind.sigs.k8s.io/) v0.33+, Docker.

| Цель | Действие |
| --- | --- |
| `make lint` | Линтер Go (`golangci-lint`) |
| `make test` | `go test ./...` |
| `make build` | Бинарники `scheduler` и `carbon-service` в `bin/` |
| `make kind-up` | Кластер kind `carbon-aware` (4 worker, зоны north/west) |
| `make kind-down` | Удаление кластера |
| `make docker-build` | Образы `carbon-aware-scheduler:0.1.0` и `carbon-data-service:0.1.0` |
| `make kind-load` | Сборка и `kind load docker-image` в кластер `carbon-aware` |

Эквивалент без Make: `bash scripts/setup_kind.sh` / `bash scripts/teardown_kind.sh`.

## Документация

- Инженерная спецификация: [`docs/ssot/engineering-specification.md`](docs/ssot/engineering-specification.md)
- **Границы проекта, DoD, code style, регламент SSOT: [`docs/ssot/governance.md`](docs/ssot/governance.md)** — обязательно к прочтению до начала любой задачи
- SSOT (пакет Stage 3 **LOCKED**, Quality Gate 3): [`docs/ssot/README.md`](docs/ssot/README.md)
- ADR: [`docs/decisions_adr/README.md`](docs/decisions_adr/README.md)
