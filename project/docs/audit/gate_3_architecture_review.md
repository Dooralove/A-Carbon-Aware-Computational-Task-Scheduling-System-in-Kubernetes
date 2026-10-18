# Quality Gate 3 — аудит архитектуры и заморозка SSOT

- **Дата:** 2026-09-04
- **Роль:** Lead System Architect & Scientific Project Director
- **Статус:** **PASS**
- **Решение:** **Stage 4 и Stage 5 — UNLOCKED (параллельно)**
- **Проверяемый переход:** Stage 3 Architectural Design → реализация Carbon Data Service и Scheduler Plugin
- **Канон:** [`01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) v1.1.0 **LOCKED**;
  [`03_api_contracts_ssot.md`](../ssot/03_api_contracts_ssot.md) v1.0.1 **LOCKED**;
  [`failure_and_fallback_policy.md`](../ssot/failure_and_fallback_policy.md) v1.1.0 **LOCKED**;
  [`metrics.md`](../ssot/metrics.md) v1.0.0 **LOCKED**;
  формулы [`02_math_model_ssot.md`](../ssot/02_math_model_ssot.md) v1.0.0 **LOCKED** (без изменений)
- **ADR:** [`002_system_architecture.md`](../decisions_adr/002_system_architecture.md) — accepted;
  Freeze [`002_system_architecture_and_api_freeze.md`](../decisions_adr/002_system_architecture_and_api_freeze.md) — accepted

## 1. Итоговый вердикт

Компонентная модель, REST-контракт Carbon Data Service, автомат Fallback и имена метрик
согласованы между собой и с заблокированной математической моделью. Горячий путь планировщика
не содержит скрытых синхронных блокировок. Углерод остаётся критерием предпочтения.

```text
Electricity Maps / dataset  →  CDS (normalize, replay, cache, REST)
                                    ↑ async pull
                          Cache Sync Worker  →  plugin snapshot (atomic)
                                                    ↓ Load() once
                          Filter (standard) → PreScore O(NK) → Score O(1) → NormalizeScore
                          G-08 / opt-out → Score=50, Success
                          SLA fail → PreScore Error (not Score=0)
```

Блокирующих противоречий API ↔ модель ↔ архитектура ↔ Scheduler Framework нет.
Количество блокеров Stage 4/5: **0**.

**QUALITY GATE 3: PASS. Пакет SSOT Stage 3 заморожен. Stage 4 (Carbon Service) и Stage 5 (Scheduler Plugin) разблокированы для параллельного запуска.**

## 2. Проверенные материалы

| ID | Файл | Роль |
| --- | --- | --- |
| T3.1 | [01_architecture_ssot.md](../ssot/01_architecture_ssot.md) | компонентная модель, PlantUML, профили |
| T3.1 | [T3.1-architecture-ssot.md](T3.1-architecture-ssot.md) | отчёт L1 |
| T3.1 | [002_system_architecture.md](../decisions_adr/002_system_architecture.md) | ADR границ процессов |
| T3.2 | [03_api_contracts_ssot.md](../ssot/03_api_contracts_ssot.md) | REST, DTO, annotations, CycleState |
| T3.2 | [T3.2-api-contracts.md](T3.2-api-contracts.md) | отчёт |
| T3.2 | `api/v1alpha1/types.go`, `openapi.yaml` | машиночитаемый контракт |
| T3.3 | [metrics.md](../ssot/metrics.md) | имена `carbon_aware_*` (консолидация L1 на Gate 3) |
| T3.4 | [failure_and_fallback_policy.md](../ssot/failure_and_fallback_policy.md) | FF-*, SM-*, Status, AT-* |
| T3.4 | [T3.4-failure-fallback-policy.md](T3.4-failure-fallback-policy.md) | отчёт |
| — | [02_math_model_ssot.md](../ssot/02_math_model_ssot.md) | LOCKED формулы |
| — | [scheduler_framework_analysis.md](../research/scheduler_framework_analysis.md) | 1.37, multiPoint, CycleState |
| — | [governance.md](../ssot/governance.md) v1.4.0 | `SSOT-08` |
| QG2 | [gate_2_math_review.md](gate_2_math_review.md) §10 | вход Stage 3 |
| L1 | [chapter_3_system_design.md](../diploma_text/chapter_3_system_design.md) | черновик главы 3 |
| L1 | Freeze ADR-002 | заморозка пакета |

## 3. Критерии аудита

1. Соответствие API, математической модели и архитектуры (единицы, G-*, фазы, Status).
2. Ни одно требование не вводит синхронный I/O в `PreScore`/`Score`/`NormalizeScore`.
3. Соответствие принципам Scheduler Framework 1.37 (точки расширения, `CycleState`, `multiPoint`).
4. Цепочка Fallback `SCOPE-IN-09` без `Unschedulable` на отказе \(CI\).
5. Изоляция A/B/C через `schedulerName`; ловушка B/`multiPoint` закрыта.
6. Нет endpoint «compute score»; CDS не ранжирует узлы.
7. Глава 3 трассируется к SSOT; формулы не переопределены.
8. Пакет LOCKED; несанкционированные правки L2/L3 запрещены.

## 4. Перекрёстная проверка API ↔ модель ↔ архитектура

| Тема | Модель | Архитектура | API / Fallback | Вердикт |
| --- | --- | --- | --- | --- |
| \(CI\) единица | `gCO2e/kWh`, lifecycle | адаптер нормализует | `unit` обязателен; `direct` invalid | PASS |
| (2.10s) где | `PreScore` \(O(NK)\) | §6.4, §13 | `CarbonCalculator` без I/O | PASS |
| `Score` \(O(1)\) | §11 модели | `ARCH-05` | lookup `scorePerNode` | PASS |
| Hard SLA | (2.13), G-12 ≠ `Score=0` | `ARCH-10` | PreScore Error | PASS |
| G-08 | Score=50 **всем** | `ARCH-09` | SM-DEAD/EMPTY; не `Skip` | PASS |
| G-09 | \(S_C=1/2\), узел кандидат | не `Filter` | 404 зоны → G-09 | PASS |
| G-06 | persistence (2.11) | replay не подставляет actual | `forecast: []` → 200 | PASS |
| Стратегия B | (2.23) ≠ \(w_C=1\) на (2.10) | `current-ci` | `scoringMode` enum | PASS |
| \(w_C\) vs \(w^{\mathrm{fw}}\) | (2.20) vs (2.22) | YAML `weight: 13` | `carbonWeight` float | PASS |
| Время | Unix s внутри | Worker `fetchedAt` | RFC 3339 на проводе | PASS |
| \(\hat T\) | (2.12) класс | PreScore | duration / class / default | PASS |
| \(T_{\mathrm{deadline}}\) | относительная в (2.13) | SLA в PreScore | annotation `1800s`, не timestamp | PASS |
| Opt-in | н/п (конфиг) | `ARCH-16` | `API-META-02`; не G-08 | PASS (закрыто на Freeze) |
| Health | н/п | `/health` | `/health` = `/healthz` | PASS |
| Нет Extender | `SCOPE-OUT-08` | ADR-002 | нет webhook | PASS |
| CycleState | один snapshot \(CI\) | `ARCH-06` | immutable, `Clone` = тот же указатель | PASS |

### 4.1. Расхождения, устранённые на Freeze (не блокеры)

| ID | Наблюдение | Резолюция L1 |
| --- | --- | --- |
| QG3-P01 | T3.4 начинал PreScore с SLA; T3.2 ввёл opt-in | единый порядок: профиль → opt-out → SLA → `Load()` (`ARCH-16`, `FF-INV-13`) |
| QG3-P02 | YAML профилей без `staleWarnSeconds` / `clockSkewSeconds` | внесены 90 s / 60 s |
| QG3-P03 | `metrics.md` был TBD (`DOD-CODE-13`) | имена LOCKED v1.0.0 |
| QG3-P04 | API §5 объявлял все интерфейсы «без ctx» | уточнено: `CarbonProvider` — I/O с `context` |

Формулы (2.1)–(2.23) не менялись.

## 5. Проверка Scheduler Framework (нет скрытых блокировок)

| Риск | Как закрыт | Вердикт |
| --- | --- | --- |
| HTTP в `Score` | `ARCH-01`, Worker вне цикла, OpenAPI клиент только Worker | PASS |
| HTTP в `PreScore` «один раз» | отвергнут ADR-002; Freeze подтверждает | PASS |
| `Wait`/`Permit` на зелёное окно | `ARCH-04`, `SCOPE-OUT-16` | PASS |
| `Filter` по \(CI\) | `ARCH-07` | PASS |
| `QueueSort` по углероду | запрещён; сломал бы A | PASS |
| `multiPoint`+`disabled: '*'` для B | явная регистрация в `preScore`/`score` | PASS |
| Гонка snapshot vs Score | immutable snapshot + один `Load` + `CycleState` | PASS |
| `Error` при stale CDS | запрещён; G-08 Success | PASS |
| Хранение `CycleState` в плагине | запрет; KEP-624 | PASS |
| Семплируемые метрики overhead | канон `scheduler_framework_extension_point_duration_seconds` | PASS |
| `GO-08` Context в struct | фабрика не кладёт ctx в поля; Worker отмена по ctx фабрики | PASS |

Диаграммы §4 / §10 / §11 архитектуры: нет ребра `Score → CDS`, нет `PreScore → REST`, нет
Electricity Maps → scheduler.

OpenAPI: пути только health/zones/current/forecast/metrics. Endpoint Score отсутствует.

## 6. Соответствие главы 3 утверждённому SSOT

Артефакт: [chapter_3_system_design.md](../diploma_text/chapter_3_system_design.md).

| Требование | Результат |
| --- | --- |
| Два процесса, два кэша, Worker вне цикла | ✅ §3.2–3.5 |
| Только PreScore/Score/NormalizeScore | ✅ §3.1.3, §3.4 |
| A/B/C через `schedulerName`; ловушка B закрыта | ✅ §3.6 |
| REST без compute-score; RFC 3339; opt-in | ✅ §3.7 |
| G-08 / SLA / Status | ✅ §3.8 |
| Формулы (2.x) ссылаются, не переопределяются | ✅ |
| Нет расширения Scope (Extender, CRD, Redis, ML) | ✅ |
| Нет обещанного процента CO₂ | ✅ |
| Академический стиль | ✅ |

## 7. Проверка Scope и DoD

### Scope

- ✅ plugin PreScore/Score/NormalizeScore; CDS REST `/zones` `/current` `/forecast` `/health`;
- ✅ три провайдера; kind ≥4 worker; три `schedulerName`;
- ✅ fallback fresh → stale-in-window → G-08 → baseline;
- ✅ нет Extender, Redis, Kafka, PostgreSQL, CRD-оператора, service mesh, HA CDS;
- ✅ нет HTTP в Score; нет углеродного Filter;
- ✅ live Electricity Maps только demo;
- ✅ глава 3 не расширяет Scope.

### Применимый DoD

| ID | Статус |
| --- | --- |
| `DOD-DOC-01` | ✅ пакет без TBD в каноне архитектуры/API/Fallback/метрик |
| `DOD-DOC-02` | ✅ ADR-002 и Freeze по шаблону |
| `DOD-DOC-03` | ✅ индекс ADR |
| `DOD-DOC-04` | ✅ glossary |
| `DOD-DOC-05` | ✅ единицы на REST и в метриках |
| `DOD-DOC-06` | ✅ глава 3, ссылки на (2.x) |
| `DOD-DOC-07` | ✅ относительные ссылки |
| `DOD-DOC-08` | ✅ русский язык, идентификаторы как в коде |
| `DOD-DOC-09` | ✅ вес `13` и эффект A/B/C — гипотезы |
| `DOD-DOC-11` | ✅ настоящий отчёт |
| `SSOT-01` / `SSOT-08` | ✅ LOCKED |
| `DOD-CODE-12` / `ARCH-01` | ✅ норма горячего пути |
| `SCOPE-OUT-*` | ✅ нарушений не обнаружено |

Код сервисов и runtime-тесты не требовались (DOD-CODE/TEST runtime / DOD-EXP — н/п до Stage 4+).
Типы `api/v1alpha1` уже существуют как контракт T3.2.

`experiment-protocol.md` остаётся заглушкой сценариев — это протокол Stage 6, не архитектурный
контракт. Не блокер Gate 3.

## 8. Что утверждено и что отклонено

| Решение | Статус |
| --- | --- |
| Два процесса, два кэша, async Worker | утверждено, LOCKED |
| Только PreScore/Score/NormalizeScore | утверждено |
| A = kind `default-scheduler` | утверждено |
| B = третий профиль, явная регистрация | утверждено |
| RFC 3339 на проводе | утверждено |
| Opt-in label; Score=50 ≠ G-08 | утверждено |
| SM-STALE без множителя \(\alpha\) | утверждено |
| Имена `carbon_aware_*` | утверждено |
| Extender / HTTP в Score / carbon Filter | отклонено |
| Redis / Kafka / CRD / Helm-обязательство | отклонено |
| Perfect foresight | отклонено |
| Слияние CDS в бинарь планировщика | отклонено |

## 9. Follow-up, не блокирующие Stage 4/5

### QG3-FU-01. Инфраструктурный toolchain (перенос QG2-FU-03)

- **Затронуто:** `go.mod` (Go 1.23 vs Kubernetes 1.37 → Go 1.26).
- **Действие:** отдельный ADR до сборки `cmd/scheduler` против node image стенда.
- **На контракты не влияет.**

### QG3-FU-02. Манифесты и Grafana JSON

- **Затронуто:** `deployments/kubernetes`, `deployments/prometheus`, `deployments/grafana`.
- **Действие:** писать вместе с кодом Stage 4/5; имена метрик уже LOCKED; запрет `latest` (`CFG-03`).

### QG3-FU-03. Протокол экспериментов

- **Затронуто:** `experiment-protocol.md` (TBD сценариев).
- **Действие:** Stage 6; карточки обязаны фиксировать `w_{\mathrm{CA}}^{\mathrm{fw}}\) и не смешивать
  Fallback rate со stale/opt-out. Sweep весов — QG2-FU-05.

### QG3-FU-04. Patch Kubernetes kind

Точный patch 1.37.x node image — в `environment.yaml` до зачётного прогона (уже норма архитектуры §6).

## 10. Передача в Stage 4 и Stage 5

Реализация **обязана** использовать только замороженный пакет. Вход:

### Stage 4 — Carbon Data Service (параллельно)

1. OpenAPI `api/v1alpha1/openapi.yaml`; пути `/health` = `/healthz`, `/zones`, `/current`, `/forecast`.
2. `CarbonProvider`: Mock, Historical+Replay, Electricity Maps (demo).
3. In-memory cache, all-or-nothing, last-good + `quality=stale`.
4. GET не двигает \(t_{\mathrm{model}}\); пустой forecast → 200 `[]`.
5. Нет endpoint Score. Метрики §3 `metrics.md`.
6. Не ходить в kube-apiserver для решений.

### Stage 5 — Scheduler Plugin (параллельно)

1. `app.WithPlugin("CarbonAware", …)`; compile-time не `FilterPlugin`.
2. Worker — единственный HTTP-клиент; `httpTimeoutSeconds` из args.
3. `PreScore` порядок архитектуры §6.4; чистые `pkg/energy` / `pkg/carbon` / `pkg/runtime`.
4. `Score` = lookup, всегда Success на углеродном отказе.
5. Оракулы модели §14; матрица Fallback `DOD-TEST-07`.
6. Профили B и C по YAML §12.4; не объявлять `default-scheduler`.

Стык: REST + неизменяемый `CarbonSnapshot`. Контракт менять нельзя без ADR.

## 11. Формальное решение Gate

**QUALITY GATE 3: PASS.**

**Stage 3 завершён. Архитектура, API, Fallback и имена метрик заблокированы (SSOT Freeze).**

**КОМАНДА НА ЗАПУСК:** Stage 4 (Carbon Data Service) и Stage 5 (Scheduler Plugin) стартуют
**параллельно** по входу §10. Агенты L2 не изменяют LOCKED SSOT (`SSOT-08`).
