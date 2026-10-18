# Quality Gate 6 — интеграция с Kubernetes (kind)

- **Дата:** 2026-09-04
- **Роль:** Lead DevOps & Kubernetes Integration Specialist
- **Статус:** **PASS**
- **Решение:** **Stage 6 — COMPLETE**; **Stage 7 (мониторинг) и Stage 8 (тестирование) — UNLOCKED**
- **Проверяемый переход:** kind + образы + манифесты (T6.1–T6.3) → сквозной Binding тестового Pod кастомным планировщиком (T6.4 / `DOD-TEST-06`)
- **Канон:** [`01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) v1.1.0 **LOCKED** (`ARCH-08`: второй процесс kube-scheduler, kind `default-scheduler` не заменяется);
  [`02_math_model_ssot.md`](../ssot/02_math_model_ssot.md) v1.0.0; [`03_api_contracts_ssot.md`](../ssot/03_api_contracts_ssot.md) v1.0.1
- **Вход:** скрипты kind (`scripts/setup_kind.sh`, `deployments/kind/cluster-config.yaml`),
  образы `carbon-aware-scheduler:0.1.0` / `carbon-data-service:0.1.0`,
  манифесты [`deployments/kubernetes/`](../../deployments/kubernetes/),
  Gate 5 [`gate_5_scheduler_plugin_audit.md`](gate_5_scheduler_plugin_audit.md)

## 1. Итоговый вердикт

Локальный kind-кластер `carbon-aware` (Kubernetes **1.37.0**, kind **v0.33.0**) принял стек Carbon Data Service и второй процесс `kube-scheduler` с профилем `carbon-aware-scheduler`. Тестовый Pod с `schedulerName: carbon-aware-scheduler` назначен **только** кастомным планировщиком на узел логической «зелёной» зоны **north**. `default-scheduler` Pod не касался.

```text
kind (1 CP + 4 workers, labels zone=north|west)
        → CDS replay  :8080  (dataset synthetic_multizone_volatile_2h_v1)
        → Worker tick : CDS ClusterIP → plugin snapshot
        → Pod schedulerName=carbon-aware-scheduler
              Filter (standard) → PreScore CarbonAware → Score → DefaultBinder
              Binding: carbon-aware-worker (zone=north, CI=85 gCO2e/kWh)
              west (CI=410) не выбран
```

`kubectl describe pod` не содержит Warning/Error. Событие `Scheduled` исходит от `carbon-aware-scheduler`. Лог планировщика фиксирует `Successfully bound pod to node`.

Количество блокеров Stage 7/8: **0**.

**QUALITY GATE 6: PASS. Кластер с установленным шедулером работает. Stage 7 (Prometheus/Grafana) и Stage 8 (e2e / failure-сценарии) разблокированы.**

## 2. Проверенные материалы

| ID | Артефакт | Роль |
| --- | --- | --- |
| T6.1 | `scripts/setup_kind.sh`, `deployments/kind/cluster-config.yaml` | кластер, зоны, extraPortMappings |
| T6.2 | `build/Dockerfile.scheduler`, `build/Dockerfile.carbon-service` | образы `*:0.1.0` в containerd узлов |
| T6.3 | `deployments/kubernetes/*.yaml` | RBAC, CDS, ConfigMap профилей B/C, Deployment шедулера |
| T6.4 | `pod-carbon-aware.yaml` | e2e Binding (`DOD-TEST-06`) |
| SSOT | architecture §8, `ARCH-08` | второй процесс; default не патчится |
| QG5 | `gate_5_scheduler_plugin_audit.md` | вход: плагин готов к контейнеру |

LOCKED SSOT **не изменялся**.

## 3. Критерии приёмки

| # | Критерий | Результат |
| --- | --- | --- |
| 1 | kind-кластер запущен, узлы Ready | PASS: 5/5 Ready, v1.37.0 |
| 2 | Образы собраны и загружены в kind | PASS: `carbon-aware-scheduler:0.1.0` (84.3 MB в crictl), `carbon-data-service:0.1.0` (10.5 MB) |
| 3 | Применены RBAC, CDS, Custom Scheduler | PASS: `kubectl apply -k deployments/kubernetes` |
| 4 | Системные поды Running | PASS: `kube-system` без non-Running; `carbon-aware` Deployments 1/1 |
| 5 | Тестовый Pod с `schedulerName: carbon-aware-scheduler` | PASS |
| 6 | Binding кастомным шедулером, не `default-scheduler` | PASS: Event `From=carbon-aware-scheduler`; в логах kind `kube-scheduler` упоминаний Pod нет |
| 7 | Физическое размещение на узле «зелёной» зоны | PASS: `carbon-aware-worker`, label `carbon-aware.io/zone=north` |
| 8 | `kubectl describe pod` без ошибок | PASS: только Normal Scheduled / Pulled / Created / Started |

## 4. Стенд

| Параметр | Значение |
| --- | --- |
| Кластер | `carbon-aware` (reuse существующего; `KIND_RECREATE` не требовался) |
| kind | v0.33.0 |
| Kubernetes | client/server v1.37.0 |
| Топология | 1 control-plane + 4 workers |
| Node image | `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5` |
| Зоны | `carbon-aware-worker`, `worker2` → **north**; `worker3`, `worker4` → **west**; все workers `power-profile=standard` |
| Control-plane | без zone-label (taint `NoSchedule`, не в \(\mathcal{N}_{\mathrm{feas}}\)) |
| Namespace стека | `carbon-aware` (не `kube-system`; kind `kube-scheduler` остаётся на control-plane, `ARCH-08`) |
| Профиль C | `schedulerName: carbon-aware-scheduler`, `CarbonAware` multiPoint **weight 13**, `scoringMode=expected-cf`, \(w=(0.6,0.2,0.2)\) |
| CDS | replay, датасет `synthetic_multizone_volatile_2h_v1.json`, `replayLoop=linear` |
| Инструменты | `.tools/kubectl.exe` v1.37.0, `.tools/kind.exe` v0.33.0, `KUBECONFIG=%USERPROFILE%\.kube\config` |

Карта узлов на момент прогона:

| Node | Ready | zone | Internal-IP |
| --- | --- | --- | --- |
| carbon-aware-control-plane | Ready | — | 172.18.0.4 |
| carbon-aware-worker | Ready | north | 172.18.0.3 |
| carbon-aware-worker2 | Ready | north | 172.18.0.2 |
| carbon-aware-worker3 | Ready | west | 172.18.0.6 |
| carbon-aware-worker4 | Ready | west | 172.18.0.5 |

## 5. Цикл развёртывания

### 5.1. kind (T6.1)

Кластер уже существовал и был переиспользован скриптом `scripts/setup_kind.sh` (идемпотентный путь). Все узлы Ready, labels зон совпадают с каноном setup (`EXPECTED_ZONES=(north north west west)` в порядке `LC_ALL=C sort` имён workers).

### 5.2. Образы (T6.2)

Образы присутствуют на worker-узлах (`crictl images`). `imagePullPolicy: IfNotPresent`. Повторный `kind load` не требовался.

### 5.3. Манифесты (T6.3)

```text
kubectl apply -k deployments/kubernetes
```

| Объект | Состояние после apply |
| --- | --- |
| Namespace `carbon-aware` + RBAC (ClusterRole не `cluster-admin`) | созданы |
| ConfigMap `carbon-aware-scheduler-config` (профили B и C) | создан |
| Deployment/Service `carbon-data-service` NodePort `8080:30080` | Ready 1/1 |
| Deployment/Service `carbon-aware-scheduler` `:10259` | Ready 1/1 |

Поды стека (после rollout restart шедулера, см. §6.1):

| Pod | Node | IP | Ready |
| --- | --- | --- | --- |
| `carbon-data-service-689949f777-z5zq9` | carbon-aware-worker2 (north) | 10.244.1.2 | 1/1 |
| `carbon-aware-scheduler-55f6c67fdd-64rmq` | carbon-aware-worker3 (west) | 10.244.2.3 | 1/1 |

`kube-system`: CoreDNS, etcd, apiserver, controller-manager, kind `kube-scheduler`, kube-proxy ×5, kindnet ×5 — все **Running**, Restarts=0.

### 5.4. Пространство логов

Задание указывало `kubectl logs -n kube-system deploy/carbon-aware-scheduler`. Deployment установлен в namespace **`carbon-aware`**. Каноническая команда:

```text
kubectl logs -n carbon-aware deploy/carbon-aware-scheduler
```

kind `kube-scheduler` в `kube-system` — это **default-scheduler**; он не обрабатывает Pod с чужим `schedulerName`.

## 6. Стык CDS ↔ Worker и «зелёная» зона

### 6.1. Гонка первого тика

При одновременном старте CDS и шедулера (оба Ready ~14:48:03Z) первый `syncTick` получил `dial tcp 10.96.211.0:8080: connect: connection refused` (CDS ещё не слушал). Успешный тик **не логируется** (только отказ на `V(2)`). Пустой снимок на горячем пути = G-08 (`Score=50` всем) — размещение стало бы случайным относительно углерода.

Для Gate 6 выполнен `kubectl -n carbon-aware rollout restart deploy/carbon-aware-scheduler` **после** Ready CDS. После рестарта (14:52:13Z):

- `CarbonAware plugin initialized` для профилей C (`expected-cf`) и B (`current-ci`);
- `CarbonAware cache sync worker started` (`refreshIntervalSeconds=30`);
- строк `cache sync tick failed` — **0**.

### 6.2. Показания CDS в момент Binding

Запрос к Pod IP CDS `http://10.244.1.2:8080` (in-cluster). Replay `ScaledClock` от `modelStart=2026-09-01T10:00:00Z`, multiplier=1. На ~4-й минуте wall-time после старта CDS `modelTime ≈ 2026-09-01T10:04Z`.

| Зона | \(CI\) (gCO₂e/kWh) | quality | Узлы kind |
| --- | --- | --- | --- |
| **north** | **85** | fresh | worker, worker2 |
| west | 410 | fresh | worker3, worker4 |
| central | 260 (в датасете; узлов с label `central` нет) | fresh | — |

Первый reversal датасета — `2026-09-01T10:25:00Z` (north 420 / west 70). Binding выполнен **до** reversal: ожидаемая «зелёная» зона — **north**.

Healthz CDS: `status=ok`, `mode=replay`, `ready=true`, `zonesReady=3/3`.

## 7. Тестовый Pod и Binding

Манифест: [`deployments/kubernetes/pod-carbon-aware.yaml`](../../deployments/kubernetes/pod-carbon-aware.yaml).

```text
kubectl apply -f deployments/kubernetes/pod-carbon-aware.yaml
kubectl wait --for=condition=PodScheduled pod/pod-carbon-aware --timeout=90s
```

Поля объекта:

| Поле | Значение |
| --- | --- |
| `spec.schedulerName` | `carbon-aware-scheduler` |
| `spec.nodeName` | `carbon-aware-worker` |
| `status.phase` | Running |
| `status.podIP` | 10.244.3.2 |
| Ready | 1/1 |
| Start Time | 2026-09-04 17:52:29 +0300 (14:52:29Z) |
| opt-in | `carbon-aware.io/enabled=true`, class `batch` |

`kubectl get pod -o wide`:

```text
NAME               READY   STATUS    RESTARTS   AGE   IP           NODE
pod-carbon-aware   1/1     Running   0          15s   10.244.3.2   carbon-aware-worker
```

### 7.1. Событие Binding

`kubectl describe pod pod-carbon-aware` / `kubectl get events`:

```text
Type    Reason     From                    Message
Normal  Scheduled  carbon-aware-scheduler  Successfully assigned default/pod-carbon-aware to carbon-aware-worker
Normal  Pulled     kubelet                 Container image "registry.k8s.io/pause:3.10" already present...
Normal  Created    kubelet                 Container created
Normal  Started    kubelet                 Container started
```

Warning/Error: **нет**. FailedScheduling: **нет**. Источник `default-scheduler`: **нет**.

### 7.2. Лог кастомного планировщика

```text
I0904 14:52:29.449928  1 schedule_one.go:483]
  "Successfully bound pod to node"
  pod="default/pod-carbon-aware"
  node="carbon-aware-worker"
  evaluatedNodes=5
  feasibleNodes=4
```

Интерпретация: оценены все 5 узлов кластера; control-plane отсеян taint'ом (`feasibleNodes=4`); среди четырёх workers выбран `carbon-aware-worker` (**north**, минимальный \(CI=85\)).

Профиль C в том же логе (старт процесса): `Score.Enabled` включает `CarbonAware` с `"Weight":13` наряду со стандартными Score-плагинами. Bind выполняет `DefaultBinder`.

### 7.3. Изоляция от default-scheduler

```text
kubectl -n kube-system logs kube-scheduler-carbon-aware-control-plane | grep pod-carbon-aware
```

Пусто. Kind default-scheduler не ставит в очередь Pod с `schedulerName != default-scheduler`.

## 8. Соответствие архитектуре

| Норма | Наблюдение | Вердикт |
| --- | --- | --- |
| `ARCH-08` второй процесс | Deployment в `carbon-aware`; kind `kube-scheduler` на CP жив | PASS |
| `ARCH-04` только Score | Filter — стандартные плагины; CarbonAware в PreScore/Score профиля C | PASS |
| Opt-in `ARCH-16` | label `enabled=true` на тестовом Pod | PASS |
| `DOD-TEST-06` | ожидаемый узел при зафиксированном датасете (north / \(CI=85\)) | PASS |
| G-08 не сработал | 0 failed ticks после рестарта; выбор north, не случайный west | PASS |
| CDS не ранжирует узлы | REST отдаёт \(CI\); Binding делает kube-scheduler | PASS |

## 9. Замечания стенда (не блокеры Gate)

| ID | Находка | Влияние | Резолюция |
| --- | --- | --- | --- |
| QG6-F01 | Первый тик Worker при совместном старте: `connection refused` | риск G-08, если планировать Pod сразу | рестарт шедулера после Ready CDS; для Stage 8 — ждать тик / фиксировать порядок в e2e |
| QG6-F02 | Host `127.0.0.1:8080` (extraPortMappings → NodePort 30080) timeout на Docker Desktop / WSL2 | внешний scrape CDS с хоста | in-cluster (Pod IP / ClusterIP из пода) работает; Stage 7 — `kubectl port-forward` или scrape из Prometheus-пода |
| QG6-F03 | `/metrics` шедулера с host port-forward: `403 system:anonymous` | ожидаемо для kube-scheduler | Stage 7: bearer SA Prometheus, не anonymous curl |
| QG6-F04 | Образ шедулера ~84 MB (цель «<50 MB» из T6.2) | размер артефакта, не Binding | не блокер Gate 6; при желании — отдельный follow-up на strip/upx, не на формулы |

NetworkPolicy CDS присутствует в манифесте; kindnet её **не enforce** (заложено в комментарии манифеста / `ARCH` §8.4).

## 10. Follow-up для Stage 7 и Stage 8

### QG6-FU-01. Мониторинг (Stage 7)

Развернуть Prometheus + Grafana (`SCOPE-IN-12`): scrape `carbon_aware_*` с HTTPS `:10259` шедулера (с токеном) и `carbon_aware_cds_*` с CDS `:8080`. Host NodePort 9090/3000, скорее всего, потребует того же обхода, что QG6-F02.

### QG6-FU-02. Тесты (Stage 8)

Автоматизировать `DOD-TEST-06` (этот прогон — ручной e2e) и `DOD-TEST-05`/`DOD-TEST-07`: CDS down → планирование живо (G-08), stale, invalid. В e2e **не** создавать workload до успешного Worker tick (или рестартить шедулер после CDS Ready). Карточки эксперимента — после reversal `t=10:25Z` ожидаемая зона меняется на west.

### QG6-FU-03. Профиль B

Профиль `carbon-only-scheduler` загружен в том же процессе. Отдельный Pod с `schedulerName: carbon-only-scheduler` в этом Gate не гонялся — зона Stage 8 / экспериментальный протокол.

## 11. Формальное решение Gate

**QUALITY GATE 6: PASS.**

**Stage 6 завершён.** Kind-кластер, образы и манифесты образуют работающий стенд. Pod с `schedulerName: carbon-aware-scheduler` планируется кастомным планировщиком без участия `default-scheduler` и биндится на узел зоны с минимальным расчётным углеродным следом (north, \(CI=85\,\mathrm{gCO_2e}/\mathrm{kWh}\)).

**КОМАНДА НА ЗАПУСК:**

- **Stage 7 — мониторинг:** Prometheus + Grafana, scrape `carbon_aware_*` / `carbon_aware_cds_*`, дашборд стенда (`SCOPE-IN-12`, веха `v0.3.0`).
- **Stage 8 — тестирование:** автоматизированный e2e `DOD-TEST-06`, integration CDS↔plugin `DOD-TEST-05`, failure-матрица `DOD-TEST-07`.

Формулы (2.1)–(2.23) и автомат Fallback **не открываются**. Агенты L2 не изменяют LOCKED SSOT (`SSOT-08`).
