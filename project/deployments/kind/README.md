# kind

Локальный Kubernetes-кластер для воспроизводимых экспериментов (логические энергозоны `north` / `west`).

## Файлы

- [`cluster-config.yaml`](cluster-config.yaml) — рабочий конфиг Stage 6 (1 control-plane, 4 worker, проброс портов, образ `kindest/node:v1.37.0` с digest).
- [`cluster.yaml`](cluster.yaml) — тот же манифест (путь SSOT §8 / `experiment-protocol.md`). Файлы нужно держать идентичными.

Имя кластера: `carbon-aware`.

## Подъём одной командой

```bash
bash scripts/setup_kind.sh
```

Либо `make kind-up`. Скрипт:

1. Создаёт кластер через `kind create cluster` (или переиспользует существующий).
2. Ждёт `Ready` на всех узлах.
3. Вешает labels (порядок worker-узлов kind: `-worker`, `-worker2`, `-worker3`, `-worker4`):

   | Логический узел | Зона | power-profile |
   | --- | --- | --- |
   | worker-01 | `north` | `standard` |
   | worker-02 | `north` | `standard` |
   | worker-03 | `west` | `standard` |
   | worker-04 | `west` | `standard` |

4. Проверяет labels и печатает `kubectl get nodes --show-labels`.

Полный пересоздание: `KIND_RECREATE=1 bash scripts/setup_kind.sh`.

Снос: `bash scripts/teardown_kind.sh` или `make kind-down`.

## Порты (host → NodePort на control-plane)

Зарезервированы для манифестов T6.2+:

| Host | NodePort | Назначение |
| --- | --- | --- |
| 8080 | 30080 | Carbon Data Service |
| 9090 | 30090 | Prometheus |
| 3000 | 30300 | Grafana |

## Требования

- Docker Engine (Docker Desktop / dockerd)
- [kind](https://kind.sigs.k8s.io/) **v0.33+** (образ `v1.37.0`)
- `kubectl` 1.37.x предпочтителен (клиент 1.32+ допустим для `get nodes`)
- RAM Docker: ориентир **≥ 8 GiB** на 5 узлов

Локальные бинарники можно положить в `.tools/` (каталог в `.gitignore`).
