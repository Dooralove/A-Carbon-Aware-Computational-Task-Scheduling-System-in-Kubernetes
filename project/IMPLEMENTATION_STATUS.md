# Состояние реализации проекта

> Срез на **2026-10-18**. Составлен по содержимому репозитория (код, `docs/audit/`, `docs/ssot/`) и прогону `go test ./...`.
> Канон требований и формул — [`docs/ssot/`](docs/ssot/README.md); этот файл только фиксирует, что уже сделано и что осталось.

## 1. Кратко

Дипломный проект **Carbon-Aware Kubernetes Scheduler** — плагин `CarbonAware` для kube-scheduler (Kubernetes 1.37) и сервис углеродных данных **Carbon Data Service (CDS)**. Плагин выбирает узел с наименьшим ожидаемым углеродным следом Pod'а с учётом прогноза углеродной интенсивности (CI) энергозоны.

| Stage | Содержание | Статус | Документ приёмки |
| --- | --- | --- | --- |
| 0 | Каркас репозитория, governance, ADR-000 | готово | `T0.1`, `T0.2` |
| 1 | Исследование: датасеты CI, модели мощности, scheduler framework | готово, **Gate 1 PASS** | `gate_1_research_review.md` |
| 2 | Математическая модель + численная валидация (48/48) | готово, **Gate 2 PASS**, SSOT LOCKED | `gate_2_math_review.md`, ADR-001 |
| 3 | Архитектура, REST/OpenAPI-контракты, политика сбоев/Fallback | готово, **Gate 3 PASS**, SSOT LOCKED | `gate_3_architecture_review.md`, ADR-002 |
| 4 | Carbon Data Service (провайдеры, Replay, кэш, REST) | готово, **Gate 4 PASS** | `gate_4_carbon_service_review.md` |
| 5 | Плагин планировщика `CarbonAware` | готово, **Gate 5 PASS** | `gate_5_scheduler_plugin_audit.md` |
| 6 | Интеграция в kind: образы, манифесты, e2e-binding | готово, **Gate 6 PASS** (ручной прогон 2026-09-04) | `gate_6_k8s_integration_report.md` |
| 7 | Мониторинг: Prometheus + Grafana | готово, **Gate 7 PASS** (kind-прогон 2026-10-18, 5×50 Pod) | `gate_7_monitoring_validation.md`, ADR-003 (proposed) |
| 8 | Автоматизированные e2e / integration / failure-тесты | готово, **Gate 8 PASS** (5 прогонов `make test-all`, 2026-10-18) | `gate_8_testing_audit.md` |
| 9 | Бенчмарки задержки, масштабируемость, pprof | готово, **Gate 9 PASS** (2026-10-18, in-process kube-scheduler) | `gate_9_performance_scalability_report.md`, `benchmarks/results_latency.json` |
| — | Эксперименты baseline vs carbon-aware, анализ результатов | **не начат** | — |
| — | Текст диплома | главы 1–3 (черновики) | `docs/diploma_text/` |

## 2. Реализованный код

Модуль Go `carbon-aware-k8s`, Go 1.26, зависимости `k8s.io/kubernetes v1.37.0`.

### 2.1. API-типы — `api/v1alpha1/`

- `types.go` — DTO контракта CDS (`CarbonRecord`, `ForecastInterval`, `CarbonSnapshot`), режимы провайдера (`live` / `replay` / `mock`), режимы скоринга (`expected-cf` — формула 2.19, `current-ci` — формула 2.23), режимы Fallback (fresh / stale / empty / dead / all-invalid / opt-out), аргументы плагина с `Validate()`, профиль нагрузки, константы путей REST.
- `openapi.yaml` — OpenAPI-спецификация REST CDS.
- Тесты: `types_test.go`.

### 2.2. Математическая модель — `pkg/energy`, `pkg/carbon`, `pkg/runtime`

- `pkg/energy` — аффинная/линейная модель мощности узла \(P(u)\), маргинальная мощность Pod'а, атрибуция, утилизация CPU/памяти, `ResourceScore`.
- `pkg/carbon/integral.go`, `footprint.go` — интеграл ожидаемого углеродного следа \(\widehat{CF}\) по горизонту \(\hat T\) с кусочно-постоянным прогнозом CI.
- `pkg/carbon/score.go` — частные скоринги (`CarbonScore`, `CurrentCIScore`, `PerformanceScore`), взвешенная сумма, перевод в fixed-point и диапазон фреймворка `[0,100]`.
- `pkg/runtime/hatt.go` — оценка ожидаемого времени работы \(\hat T\) (явная аннотация → таблица классов → класс по умолчанию).
- Проверено оракулами из валидации T2.2 (`analysis/scripts/validate_math_model.py`, графики в `docs/research/figures/math_model_validation/`).

### 2.3. Carbon Data Service — `cmd/carbon-service`, `pkg/carbon/{config,provider,replay,cache,api}`

- **Конфиг** (`config`): YAML + переменные окружения; API-ключ live-режима только из `CARBON_LIVE_API_KEY`.
- **Провайдеры** (`provider`): фабрика `NewProvider`; live-клиент Electricity Maps; исторический/replay-провайдер; mock.
- **Replay Engine** (`replay`): загрузка датасетов JSON/CSV, масштабируемые часы (`ScaledClock`, `FakeClock`), режимы проигрывания.
- **Кэш** (`cache`): in-memory снимок на `atomic.Pointer`, фоновое обновление, last-good при отказе провайдера, метрики `carbon_aware_cds_*` (Get ≈ 155 ns/op).
- **REST** (`api`): `GET /health`, `/healthz`, `/zones`, `/current`, `/forecast`, `/metrics`; контрактные тесты против OpenAPI.

### 2.4. Плагин планировщика — `cmd/scheduler`, `pkg/scheduler`

- `cmd/scheduler` — kube-scheduler с зарегистрированным out-of-tree плагином `CarbonAware`.
- `plugin.go`, `plugin_args.go` — конструктор, валидация аргументов (fail-closed).
- `cache_sync.go`, `worker.go`, `cds_client.go` — Cache Sync Worker: периодически опрашивает CDS и атомарно публикует локальный снимок; при отказе сохраняет last-good.
- `prescore.go` — разбор Pod'а, opt-in по label, \(\hat T\)/SLA, классификация снимка (fresh / stale / empty / dead), расчёт \(\widehat{CF}\) или текущей CI по узлам, запись в `CycleState`.
- `score.go`, `normalize.go` — Score (≈ 94 ns/op) и NormalizeScore; при отсутствии данных — нейтральный Score = 50 всем узлам (G-08).
- `metrics.go` — метрики `carbon_aware_*` (sync failures, sync duration, fallback) в legacy-registry kube-scheduler.

### 2.5. Тесты

`go test ./...` на 2026-10-18 — **все пакеты PASS** (Go 1.26.5).

| Пакет | Покрытие |
| --- | --- |
| `pkg/scheduler` | 96.7 % |
| `pkg/carbon/api` | 96.9 % |
| `pkg/carbon/cache` | 94.7 % |
| `pkg/carbon/replay` | 86.9 % |
| `pkg/carbon` | 71.2 % |
| `pkg/runtime` | 66.7 % |
| `pkg/carbon/provider` | 65.7 % |
| `pkg/energy` | 63.0 % |
| `pkg/carbon/config` | 58.4 % |

Дополнительно: `tests/unit/` — кросс-пакетные тесты (Replay-файл → кэш → REST; скоринг планировщика); бенчмарк `score_bench_test.go`.
`tests/integration/` и `tests/e2e/` — **пустые** (только `.gitkeep`).

## 3. Инфраструктура и стенд

- `Makefile`: `lint`, `test`, `build`, `docker-build`, `kind-load`, `kind-up`, `kind-down`, `clean`.
- `build/Dockerfile.scheduler`, `build/Dockerfile.carbon-service` — distroless-образы `carbon-aware-scheduler:0.1.0` (~84 MB), `carbon-data-service:0.1.0` (~10.5 MB).
- `deployments/kind/` + `scripts/setup_kind.sh`, `teardown_kind.sh` — кластер `carbon-aware`: 1 control-plane + 4 worker, зоны `north` ×2 и `west` ×2.
- `deployments/kubernetes/` (kustomize) — namespace `carbon-aware`, RBAC (не cluster-admin), Deployment/Service CDS (NodePort 30080) с NetworkPolicy, ConfigMap профилей планировщика B (`carbon-only-scheduler`, `current-ci`) и C (`carbon-aware-scheduler`, `expected-cf`, веса 0.6/0.2/0.2), Deployment второго kube-scheduler, тестовые Pod'ы `pod-baseline.yaml` и `pod-carbon-aware.yaml`.
- `.tools/` — локальные `go`, `kind`, `kubectl`, `golangci-lint`.
- **Подтверждено Gate 6:** Pod с `schedulerName: carbon-aware-scheduler` назначен только кастомным планировщиком на узел зоны `north` (CI = 85) вместо `west` (CI = 410).

## 4. Данные и анализ

- `experiments/datasets/schemas/` — JSON Schema: канонический формат, Electricity Maps, NESO, live current/forecast, replay-датасет.
- `experiments/datasets/examples/` — примеры ответов API и синтетические мультизонные датасеты (stable 24h, dynamic 24h, volatile 2h); генератор `_generate_and_validate.py`.
- `analysis/scripts/validate_math_model.py` — численная валидация модели (результаты в `docs/research/math_model_validation_report.md`).

## 5. Документация

- `docs/ssot/` — инженерная спецификация, governance (DoD, scope, code style), LOCKED-документы: математическая модель v1.0.0, архитектура v1.1.0, API-контракты v1.0.1, политика сбоев v1.1.0, метрики v1.0.0; глоссарий, требования.
- `docs/decisions_adr/` — ADR-000 (scope/governance), ADR-001 (утверждение модели), ADR-002 (архитектура и заморозка API).
- `docs/research/` — обзоры датасетов CI, моделей мощности, scheduler framework; отчёт валидации модели.
- `docs/audit/` — журналы задач T0.1–T5.5 и Quality Gates 1–6.
- `docs/diploma_text/` — черновики глав: 1 (анализ предметной области), 2 (математическая модель), 3 (архитектура).

## 6. Что не сделано

1. **Stage 7 — мониторинг.** Сделано (Gate 7 PASS). Остаётся подтвердить ADR-003 (PostBind и поправка `metrics.md` v1.1.0) и прогнать стек на машине с cgroup v2.
2. **Stage 8 — тестирование.** Автоматизировать e2e-binding (`DOD-TEST-06`), integration CDS↔плагин (`DOD-TEST-05`), failure-матрицу (`DOD-TEST-07`: CDS недоступен, stale, invalid). Профиль B (`carbon-only-scheduler`) на стенде не прогонялся.
3. **Эксперименты.** T10.1–T10.4 сделаны: `raw_data_matrix.csv` (1500 строк) и `raw_data_stress.csv` (2550 строк), отчёт [`docs/audit/T10-experiments-report.md`](docs/audit/T10-experiments-report.md). Остаётся статистический анализ (Stage 11).
4. **Текст диплома.** Нет глав о реализации, экспериментах и выводах.

## 7. Известные замечания

- **Гонка первого тика (QG6-F01):** если шедулер стартует раньше CDS, первый sync падает, и до следующего тика Pod'ы получают нейтральный Score. На стенде обходилось рестартом шедулера после готовности CDS; в e2e нужно ждать первого успешного тика.
- **Доступ с хоста (QG6-F02/F03):** NodePort CDS с хоста на Docker Desktop/WSL2 не отвечает; `/metrics` шедулера требует аутентификации — для Prometheus нужен in-cluster scrape с токеном.
- **Размер образа шедулера** ~84 MB при цели < 50 MB (QG6-F04, не блокер).
- **Git:** репозиторий инициализирован, но **нет ни одного коммита** — все файлы неотслеживаемые.
- **Мусор в корне:** `carbon-service.exe`, `coverage.out`, `sched_coverage.out` игнорируются `.gitignore`, а файл `sched_coverage` (без расширения) — **нет** и попадёт в первый коммит.
- В `docs/decisions_adr/` два файла ADR-002 (`002_system_architecture.md` и `002_system_architecture_and_api_freeze.md`); первый ссылается на второй как на актуальный.
- Журналы `T3.3` и `T4.5` в `docs/audit/` отсутствуют (их роль, судя по индексу, выполняют gate-отчёты).
