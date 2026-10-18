# Контракты API, DTO и метаданных Carbon-Aware Scheduler

- **Версия:** 1.0.1
- **Статус:** accepted (LOCKED)
- **Дата утверждения:** 2026-09-04
- **Владелец:** API Architect / Go Systems Engineer
- **Уровень изменяемости:** L1
- **Основание:** [01_architecture_ssot.md](01_architecture_ssot.md) v1.1.0 LOCKED (`ARCH-*`, §5.4, §6); [02_math_model_ssot.md](02_math_model_ssot.md) v1.0.0 LOCKED (единицы, G-01…G-21); [failure_and_fallback_policy.md](failure_and_fallback_policy.md) v1.1.0 (`fwk.Status`, SM-*, args свежести); [governance.md](governance.md) (`SCOPE-IN-02`, `SCOPE-IN-10`, `SCOPE-OUT-12`, `SCOPE-OUT-17`, `GO-08`…`16`, `VER-05`, `VER-06`, `DOD-DOC-05`); ADR [`002_system_architecture.md`](../decisions_adr/002_system_architecture.md); Freeze [`002_system_architecture_and_api_freeze.md`](../decisions_adr/002_system_architecture_and_api_freeze.md); Quality Gate 3
- **Код:** [`api/v1alpha1/types.go`](../../api/v1alpha1/types.go), OpenAPI [`api/v1alpha1/openapi.yaml`](../../api/v1alpha1/openapi.yaml)
- **Следующие потребители:** Stage 4 (CDS REST, Cache Sync Worker), Stage 5 (`PreScore`)

> **LOCKED / SSOT Freeze (Quality Gate 3).** Настоящий файл — единственный нормативный источник REST-контракта Carbon Data Service, Go-DTO группы `carbon-aware.io/v1alpha1`, ключей Pod/Node `carbon-aware.io/*`, `pluginConfig.args` и payload `CycleState`. Нормы `API-*` **заблокированы**. Формулы (2.1)–(2.23) **не дублируются**. Границы процессов **не пересматриваются**: нет endpoint «посчитать Score»; `PreScore`/`Score` не вызывают REST (`ARCH-01`, `ARCH-03`). Изменение wire-контракта — только новым ADR (`VER-06`).

Указатель [interfaces.md](interfaces.md) ссылается сюда и **не** является вторым каноном.

---

## 0. Нормативные идентификаторы

| ID | Норма (сжато) |
| --- | --- |
| API-TIME-01 | Единственный wire-формат времени — RFC 3339 UTC, секундная точность, суффикс `Z`. Unix-секунды — внутреннее представление модели, не поле JSON |
| API-UNIT-01 | Единственная единица \(CI\) на проводе — `gCO2e/kWh`. Поле `unit` обязательно в `CarbonRecord` и `CarbonForecast` |
| API-REST-01 | CDS REST: `GET /api/v1/healthz` (= `GET /api/v1/health`), `GET /api/v1/zones`, `GET /api/v1/zones/{zone}/current`, `GET /api/v1/zones/{zone}/forecast?duration=` |
| API-REST-02 | Коды: `200`, `400`, `404`, `503`. Тело ошибки — Kubernetes `Status`. GET идемпотентен и **не** двигает replay-часы |
| API-REST-03 | Запрещены endpoint выбора узла, расчёта \(\operatorname{Score}\), \(\widehat{CF}\), \(P(u)\) |
| API-PROV-01 | `CarbonProvider` один для Live / Replay / Mock; различие — `Mode()` и поля `source`/`mode`, не отдельный протокол |
| API-META-01 | Каждый ключ `carbon-aware.io/*` имеет ровно одну поверхность (label **или** annotation) и закрытый словарь значений |
| API-META-02 | `carbon-aware.io/enabled` — **opt-in**. Нет ключа или значение `"false"` → углеродный скоринг выключен (`Score=50`, `Success`), это **не** G-08 |
| API-SLA-01 | `carbon-aware.io/sla-deadline` — **относительная** длительность \(T_{\mathrm{deadline}}\) (`1800s`), не абсолютный timestamp |
| API-DUR-01 | Длительности — строка Go/`time.ParseDuration` с суффиксом единицы. Голое число (`300`) запрещено |
| API-STATE-01 | `CycleState` ключ `carbon-aware.io/CarbonAware`; payload неизменяем после `Write`; `Clone()` возвращает тот же указатель |
| API-GMAP-01 | Таблица `fwk.Status` **копирует** [failure_and_fallback_policy.md](failure_and_fallback_policy.md) §9. Проблемы \(CI\) не кодируются как `Error` |

---

## 1. Назначение и границы

Документ фиксирует **что** передаётся между процессами и **как** это типизировано в Go. Он не задаёт алгоритмы `pkg/energy` / `pkg/carbon` и не вводит CRD (`SCOPE-OUT-12`).

Потребители REST: Cache Sync Worker (процесс custom kube-scheduler), Prometheus (только `/metrics`, вне этой OpenAPI), человек/демо. Плагин в горячем пути читает только `CarbonSnapshot` и `CarbonCycleState`.

---

## 2. Соглашения Kubernetes API и Go

### 2.1. Группа и пакет

| | Значение |
| --- | --- |
| Group | `carbon-aware.io` |
| Version | `v1alpha1` (`VER-05`) |
| Go package | `carbon-aware-k8s/api/v1alpha1` |
| JSON | `camelCase`, `additionalProperties` запрещены на REST-схемах |
| YAML | те же имена полей, что JSON (plugin args) |
| Имена физических величин в Go | единица в идентификаторе (`GO-11`): `CarbonIntensityGCO2ePerKWh`, json-ключ `carbonIntensity` |

Поля `apiVersion` / `kind` CDS **обязан** выставлять на ответах 200. Worker **обязан** принимать тела без них (совместимость с фикстурами T1.1).

### 2.2. Время (API-TIME-01)

Выбор T3.2 (архитектура §17.1 требовала выбрать одно): **RFC 3339 UTC**.

| Часы | Поле | Смысл | Не путать с |
| --- | --- | --- | --- |
| Observation | `timestamp` | начало полуинтервала сетки \(CI\) \([t, t+\Delta t)\) | временем HTTP |
| Coverage end | `expiresAt` | `timestamp + intervalSeconds` | TTL кэша |
| Ingestion | `fetchedAt` | когда CDS положил значение в снимок | `timestamp` |
| Freshness | `freshnessSeconds` | \(\lfloor t_{\mathrm{now}}-t_{\mathrm{fetchedAt}}\rfloor\), с | \(\lfloor t_{\mathrm{now}}-t_{\mathrm{timestamp}}\rfloor\) |
| Forecast issue | `issuedAt` | момент выпуска прогноза | `timestamp` первого куска |

`freshnessSeconds` **не** считается от `timestamp`: иначе часовой ряд через 5 мин после начала часа ложно объявляется stale при `maxDataAgeSeconds=300` (T1.1 §6.1). G-08 на стороне плагина использует `CarbonSnapshot.FetchedAt` часов планировщика.

Тип Go: `RFC3339Time`. Метод `UnixSeconds()` — мост к канону модели (секунды Unix UTC).

### 2.3. Длительности (API-DUR-01)

| Контекст | Форма | Пример | Запрещено |
| --- | --- | --- | --- |
| Query `duration`, Pod annotations | строка с единицей | `300s`, `1800s`, `2h` | `300`, `1,5h`, RFC 3339 |
| JSON-поля `*Seconds`, plugin args | целое int64, единица секунда | `7200` | строки в args |

Голое целое отвергается: `time.ParseDuration("300")` = 300 **нс**. Функция `ParseDurationSeconds` требует суффикс и кратность 1 s.

Значение `0s` легально (G-11, G-14). Отрицательные длительности — ошибка контракта.

### 2.4. Единицы (API-UNIT-01, DOD-DOC-05)

| Величина | JSON-поле | Единица |
| --- | --- | --- |
| \(CI\), \(\widehat{CI}\) | `carbonIntensity` | `gCO2e/kWh` |
| \(\widehat{CF}\) | `estimatedCarbonGCO2e` | `gCO2e` |
| \(P\) | `powerIdleWatts`, `powerMaxWatts` | `W` |
| \(E\) | `estimatedEnergyKWh` | `kWh` |
| время / длительность | `*Seconds`, RFC 3339 | `s` |
| CPU | `*Millicores` | millicores |
| RAM | `*Bytes` | byte |
| \(S_{\bullet}\) | `carbonScore` и др. | \(1\), \([0,1]\) |
| \(\operatorname{Score}\) | `pluginScore` | \(1\), \([0,100]\) |

`emissionFactorType` для скоринга — только `lifecycle`. `direct` (NESO) на горячем пути — invalid (G-10 / `ErrNotLifecycle`). Смешение в одном снимке запрещено (модель §6.5).

---

## 3. REST API Carbon Data Service

Порт стенда: **8080**. Базовый URL: `http://carbon-data-service.carbon-aware.svc:8080`. Аутентификация: ClusterIP + NetworkPolicy; секрет Electricity Maps не проходит через эти ручки.

Машиночитаемая спецификация: [`api/v1alpha1/openapi.yaml`](../../api/v1alpha1/openapi.yaml) (OpenAPI 3.0.3).

`GET /metrics` существует (ARCH §5.4), формат Prometheus text; имена — [metrics.md](metrics.md), не часть настоящей OpenAPI.

### 3.1. Эндпоинты

| Метод | Путь | Назначение | Успех | Ошибки |
| --- | --- | --- | --- | --- |
| GET | `/api/v1/healthz` | liveness/readiness + freshness | 200 `HealthStatus` (`ok`/`degraded`) | 503 `HealthStatus` (`unavailable`) |
| GET | `/api/v1/health` | **тот же обработчик**, путь SCOPE-IN-02 | как `healthz` | как `healthz` |
| GET | `/api/v1/zones` | каталог зон текущего снимка | 200 `ZoneProfileList` | 503 `Status` |
| GET | `/api/v1/zones/{zone}/current` | текущий `CarbonRecord` | 200 | 400, 404, 503 |
| GET | `/api/v1/zones/{zone}/forecast` | ряд прогноза | 200 (в т.ч. пустой `forecast`) | 400, 404, 503 |

`{zone}` — DNS-1123 label, совпадает с `carbon-aware.io/zone`. Примеры: `north`, `west`. Не cloud region.

### 3.2. Query `duration`

`GET /api/v1/zones/{zone}/forecast?duration={duration}`

| | Норма |
| --- | --- |
| Имя | `duration` |
| Обязателен | нет |
| Default | `7200s` (верхняя граница горизонта скоринга, модель §11) |
| Max | `86400s` (24 h; 288 точек при шаге 300 s) |
| Семантика | вернуть куски с `timestamp ∈ [issuedAt, issuedAt+duration)` |
| Пустой ряд | **200**, `forecast: []`, `forecastMode: none` или `persistence` — плагин делает (2.11) |

CDS **не** подставляет будущие actual из того же файла (`perfect foresight` запрещён). Режим `forecastMode: persistence` на REST **обязан** иметь пустой массив: копирование `CI_{\mathrm{now}}\) в forecast создало бы ложный recorded-прогноз.

### 3.3. Коды, заголовки, идемпотентность (API-REST-02)

| HTTP | Когда | Тело |
| --- | --- | --- |
| 200 | снимок есть; для `current`/`forecast` зона известна; quality ∈ {`fresh`,`stale`,`estimated`} | ресурс |
| 400 | `{zone}` не DNS-1123; `duration` без суффикса / > max / отрицательный | `Status` `BadRequest` |
| 404 | зона отсутствует в снимке | `Status` `NotFound` |
| 503 | снимок никогда не публиковался (`ErrEmptyCache`) | `Status` `ServiceUnavailable` или `HealthStatus` на health* |

Stale, но валидный снимок → **200** с `quality: stale` / health `degraded`. Worker при 5xx/timeout **не** затирает свой последний хороший snapshot (ARCH §6.2). 404 по зоне → G-09 на узлах этой зоны, не падение планировщика.

Заголовки на 200 `current` (информационные, дублируют тело):

- `Content-Type: application/json`
- `Cache-Control: no-store`
- `X-Carbon-Data-Age-Seconds`
- `X-Carbon-Quality`
- `X-Carbon-Source`

GET без побочных эффектов на решение и на \(t_{\mathrm{model}}\). Тик replay — отдельный управляющий контур CDS, не эти ручки (ARCH §15).

Таймаут на стороне CDS и Worker задаётся конфигурацией (`GO-09`), не магическим числом в обработчике. Канон стенда Worker: `httpTimeoutSeconds=2`.

### 3.4. Тело ошибки

```json
{
  "apiVersion": "v1",
  "kind": "Status",
  "status": "Failure",
  "message": "zone \"central\" is not in the carbon snapshot",
  "reason": "NotFound",
  "code": 404,
  "details": { "name": "central", "kind": "Zone" }
}
```

### 3.5. Примеры успешных тел

`GET /api/v1/zones/north/current`:

```json
{
  "apiVersion": "carbon-aware.io/v1alpha1",
  "kind": "CarbonRecord",
  "zone": "north",
  "timestamp": "2026-09-01T10:00:00Z",
  "carbonIntensity": 82.4,
  "unit": "gCO2e/kWh",
  "source": "electricity-maps",
  "sourceZone": "NO-NO1",
  "emissionFactorType": "lifecycle",
  "temporalGranularity": "5_minutes",
  "intervalSeconds": 300,
  "quality": "fresh",
  "freshnessSeconds": 35,
  "fetchedAt": "2026-09-01T10:00:35Z",
  "expiresAt": "2026-09-01T10:05:00Z"
}
```

`GET /api/v1/zones/north/forecast?duration=7200s` без внешнего прогноза:

```json
{
  "apiVersion": "carbon-aware.io/v1alpha1",
  "kind": "CarbonForecast",
  "zone": "north",
  "unit": "gCO2e/kWh",
  "issuedAt": "2026-09-01T10:00:00Z",
  "forecastMode": "none",
  "horizon": "7200s",
  "forecast": []
}
```

---

## 4. DTO (Go / JSON)

Канонические структуры: пакет `v1alpha1`. Ниже — норма полей, не копия формул.

### 4.1. `CarbonRecord`

Текущее наблюдение зоны. Обязательные JSON-поля: `zone`, `timestamp`, `carbonIntensity`, `unit`, `source`, `emissionFactorType`, `quality`, `freshnessSeconds`.

Домен \(CI\in[0,2000]\) gCO2e/kWh, не NaN, не \(\pm\infty\) (G-10). `Validate()` / `IsUsableForScoring()` кодируют это в Go.

`carbonFreePercentage` — наблюдательное, **не** вход (2.10).

На 200 `current` запрещены `quality: invalid|missing` (такие записи не публикуются в снимок).

### 4.2. `ForecastInterval`

Кусок кусочно-постоянного \(\widehat{CI}\) на полуинтервале `[timestamp, expiresAt)`. JSON-массив в поле `forecast` конверта `CarbonForecast`.

Обязательны `timestamp`, `carbonIntensity`. Рекомендуются `expiresAt`, `durationSeconds`, `horizonSeconds` (`timestamp - issuedAt`).

Упорядочение: строго возрастающий `timestamp`, без дубликатов, ≤ 288 элементов. \(\Delta t_k\le 0\) — invalid (G-15 отбрасывает кусок в калькуляторе; CDS такой кусок не отдаёт).

### 4.3. `ZoneProfile` / `ZoneProfileList`

Каталог Worker'а: какие зоны есть, есть ли current/forecast, freshness. Не содержит ряда \(CI\) (его Worker забирает отдельными GET).

### 4.4. `NodeEnergyProfile`

Вход `EnergyModel`. Зона и `power-profile` — с Node labels; \(P_{\mathrm{idle}},P_{\max}\) — из `pluginConfig.args` (имя профиля или default). Отсутствие зоны: `hasZone=false`, поле `zone` пустое — это G-09, не ошибка валидации профиля.

Утилизация \(u_n^{\pm}\) **не** хранится в профиле: её считает `PreScore` из `NodeInfo.GetRequested()` / allocatable.

### 4.5. `WorkloadProfile`

Результат разбора Pod labels/annotations плюс CPU/RAM request из `PodSpec`. Конструктор `NewWorkloadProfile` читает **только** канонические ключи §6.

| Поле | Смысл |
| --- | --- |
| `enabled` | opt-in углеродного скоринга |
| `workloadClass` | ключ таблицы \(\hat T=T_{\mathrm{class}}\) (2.12) |
| `expectedDurationSeconds` | явный горизонт; если задан, **замещает** таблицу класса |
| `slaDeadlineSeconds` | \(T_{\mathrm{deadline}}\); `null` = \(+\infty\) (G-13) |
| `carbonWeight` | опциональный \(w_C\) пода; см. §6.5 |

### 4.6. `SchedulingDecisionRecord`

Аудит одной попытки: выбранный узел, \(S_C,S_R,S_P\), \(\widehat{CF}\), \(\widehat{E}\), флаги G-08 / SLA / opt-in, список кандидатов. Пишется в лог/метрики **после** `PreScore` (и сопоставляется с Binding ex-post). Это **не** REST-ресурс CDS (API-REST-03).

### 4.7. `CarbonAwarePluginArgs`

Схема `pluginConfig.args` (имена JSON = YAML):

| Поле | Тип | Канон стенда | Связь |
| --- | --- | --- | --- |
| `scoringMode` | `expected-cf` \| `current-ci` | C: `expected-cf`; B: `current-ci` | (2.19) vs (2.23) |
| `carbonWeight` | float64 | `0.6` | \(w_C\) |
| `resourceWeight` | float64 | `0.2` | \(w_R\) |
| `performanceWeight` | float64 | `0.2` | \(w_P\) |
| `lambda` | float64 ∈ [0,1] | `1` | (2.16) |
| `powerIdleWatts` | float64 | `40` | default \(P_{\mathrm{idle}}\) |
| `powerMaxWatts` | float64 | `100` | default \(P_{\max}\) |
| `powerProfiles` | map name → spec | `standard` | label `power-profile` |
| `runtimeClassSeconds` | map class → s | калибровка стенда | (2.12) |
| `defaultWorkloadClass` | class или пусто | пусто = fail-closed G-20 | |
| `maxDataAgeSeconds` | int64 | `300` | G-08 / SM-DEAD |
| `staleWarnSeconds` | int64 | `90` | порог SM-STALE; строго `< maxDataAgeSeconds` |
| `clockSkewSeconds` | int64 | `60` | допуск «будущего» `timestamp` (FF-FUT) |
| `refreshIntervalSeconds` | int64 | `30` | Worker; должно быть \(\ll\) maxDataAge |
| `httpTimeoutSeconds` | int64 | `2` | Worker HTTP |
| `carbonServiceURL` | string | in-cluster URL CDS | |
| `allowPodCarbonWeightOverride` | bool | `false` | §6.5 |
| `epsilonCFGCO2e` | float64 | `1e-6` | G-04 |
| `epsilonCIGCO2ePerKWh` | float64 | `1e-6` | (2.23) |

`Validate()` отказывает в старте плагина при нарушении (2.2a)/(2.20), пустом URL, `refresh ≥ maxDataAge`, `staleWarn ≥ maxDataAge`, неизвестном `scoringMode` (G-03, G-18). Молчаливая перенормировка весов запрещена.

Не путать `carbonWeight` (\(w_C\), сумма 1) с `plugins.score.weight` / `w_{\mathrm{CA}}^{\mathrm{fw}}\) (целые веса фреймворка, (2.22)).

### 4.8. `CarbonSnapshot` / `ZoneCarbonState`

Второй кэш (процесс планировщика). Immutable после `Store`. Worker собирает новый map целиком. `PreScore` делает один `Load()` (`ARCH-06`).

---

## 5. Доменные интерфейсы

Объявлены в `api/v1alpha1` как контракт; реализации — на стороне потребителя в `pkg/provider`, `pkg/energy`, `pkg/carbon` (`GO-16`, `ARCH-14`). `EnergyModel` и `CarbonCalculator` — **чистые**: без `context.Context`, без I/O. `CarbonProvider` — I/O-интерфейс CDS: `context.Context` обязателен (`GO-08`, `GO-09`). `Clock` не выполняет I/O.

### 5.1. `CarbonProvider` (Live vs Replay)

```text
Name() string
Mode() ProviderMode                    // live | replay | mock
ListZones(ctx) ([]ZoneProfile, error)
Current(ctx, zone, at) (CarbonRecord, error)
Forecast(ctx, zone, at, horizon) ([]ForecastInterval, error)
```

| Реализация | `Mode()` | I/O |
| --- | --- | --- |
| `ElectricityMapsProvider` | `live` | HTTPS (только demo) |
| `HistoricalDatasetProvider` + Replay Engine | `replay` | локальный датасет, \(t_{\mathrm{model}}=at\) |
| `MockProvider` | `mock` | нет |

Replay Engine **выглядит** как `CarbonProvider` (ARCH §5.2). Пустой forecast ≠ ошибка: возвращается `[]ForecastInterval{}`; `ErrEmptyForecast` допустим как явный сигнал, эквивалентный пустому срезу. `ErrUnknownZone` → CDS 404.

`at` — модельное или wall время **вызывающего** `Clock`, не `time.Now()` внутри адаптера.

### 5.2. `EnergyModel`

```text
NodePowerWatts(profile, u) (W, error)                          // (2.2), clip u в [0,1]
PodMarginalPowerWatts(profile, uMinus, uPlus) (W, error)       // (2.4), IDLE-NONE
```

Нет HTTP, нет глобалов, нет чтения Prometheus.

### 5.3. `CarbonCalculator`

```text
ExpectedFootprintGCO2e(P_pod W, horizon s, current, forecast) (gCO2e, error)  // (2.10s)
```

Если `forecast` пуст — persistence (2.11) **здесь**, не в CDS. Perfect foresight запрещён.

### 5.4. `Clock`

```text
Now() time.Time
```

Реализации: `SystemClock` (wall, не в unit-тестах скоринга), `FakeClock` (replay и `DOD-TEST-01`). CDS replay и `fetchedAt` плагина используют **разные** экземпляры: модельные часы сценария vs часы процесса планировщика.

---

## 6. Контракт Pod labels и annotations

Группа ключей: `carbon-aware.io/*`. CRD нет. `spec.schedulerName` — **не** annotation; это поле PodSpec (изоляция A/B/C).

### 6.1. Одна поверхность на ключ (API-META-01)

| Ключ | Поверхность | Допустимые значения | Default при отсутствии |
| --- | --- | --- | --- |
| `carbon-aware.io/enabled` | **label** | `"true"` \| `"false"` (строго нижний регистр) | как `"false"` (opt-in) |
| `carbon-aware.io/workload-class` | **label** | `"batch"` \| `"flexible"` \| `"latency-sensitive"` | нет класса |
| `carbon-aware.io/expected-duration` | **annotation** | длительность API-DUR-01, например `"300s"` | таблица класса / defaultClass |
| `carbon-aware.io/sla-deadline` | **annotation** | длительность, например `"1800s"` | \(T_{\mathrm{deadline}}=+\infty\) (G-13) |
| `carbon-aware.io/carbon-weight` | **annotation** | десятичная в `[0,1]`, `.` как разделитель, без экспоненты; `"0.7"` | \(w_C\) из plugin args |

Запрещено:

- читать ключ с «чужой» поверхности (label `expected-duration` игнорируется; annotation `enabled` → `ErrInvalidAnnotation` / PreScore Error);
- алиасы из входной спецификации: `carbon-aware.io/class`, `carbon-aware.io/sla-seconds` **не существуют**;
- `"True"`, `"TRUE"`, `"yes"`, `"1"` для enabled;
- `"0.7"` с запятой; `"7e-1"`;
- RFC 3339 в `sla-deadline` (это не абсолютный дедлайн).

`NewWorkloadProfile` реализует эту таблицу.

### 6.2. `enabled` (API-META-02)

Двухуровневый opt-in:

1. `spec.schedulerName` должен совпасть с профилем, содержащим `CarbonAware`. Иначе плагин не вызывается.
2. Label `carbon-aware.io/enabled="true"` включает углеродный скоринг.

| Label | Поведение `PreScore` | `fwk.Status` | Метрика (имена — [metrics.md](metrics.md)) |
| --- | --- | --- | --- |
| `"true"` | полный расчёт (если данные свежие) | Success или Error по SLA | обычный путь |
| `"false"` или отсутствует | всем узлам `Score=50`, **не** G-08 | Success | opt-in disabled |
| иное | не скорим | **Error** (конфиг workload) | как SLA-конфиг |

`latency-sensitive` **не** выключает углерод сам по себе. Для opt-out нужно `enabled: "false"`. Класс задаёт только \(\hat T\).

### 6.3. Класс и `expected-duration`

Горизонт \(\hat T\):

1. Если annotation `expected-duration` валидна — \(\hat T\) равен ей, независимо от \(n\) (2.12).
2. Иначе если label класса известен и есть `runtimeClassSeconds[class]` — берётся таблица.
3. Иначе если задан `defaultWorkloadClass` с таблицей — он.
4. Иначе при `enabled=true` — G-20, PreScore Error `SLA precondition failed`.
5. При `enabled=false` отсутствие класса **не** Error.

Неизвестный класс при `enabled=true` — Error, даже если duration задан? **Нет:** явный `expected-duration` делает \(\hat T\) определённым; класс тогда опционален (информационный). Если duration нет и класс неизвестен — G-20.

### 6.4. `sla-deadline` (API-SLA-01)

Значение `"1800s"` есть \(T_{\mathrm{deadline}}=1800\,\mathrm{s}\) относительно момента попытки (`Clock.Now()` плагина / \(t_{\mathrm{now}}\) модели), **не** `t_deadline` абсолютный.

Hard SLA (2.13): \(\hat T \le T_{\mathrm{deadline}}\). При каноне (2.12) условие одинаково для всех узлов. Нарушение → PreScore Error, `Score` не вызывается, Pod `Pending` (ARCH-10). Это **не** `Score=0` и не `Filter`.

`"0s"` легален: при \(\hat T=0\) — G-14, \(S_P=1\); при \(\hat T>0\) — G-12.

### 6.5. `carbon-weight`

Зачётные прогоны фиксируют \(w_C\) в карточке сценария (`REPRO-06`) → plugin args, `allowPodCarbonWeightOverride: false` (default).

| Args override | Annotation | Эффект |
| --- | --- | --- |
| `false` | любая валидная | записывается в `SchedulingDecisionRecord`, **Score не меняет** |
| `true` | отсутствует | \(w_C\) из args |
| `true` | `"0.7"` | \(w_C'=0{,}7\); \(w_R',w_P'\) масштабируются пропорционально, чтобы сумма была 1; если \(w_R=w_P=0\) и \(w_C'\ne 1\) — Error |
| — | невалидная | PreScore Error (конфиг workload) |

Перенормировка в горячем пути plugin args **запрещена**; перенормировка по Pod — только при явном флаге.

### 6.6. Пример Pod (стратегия C, batch)

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: batch-job-1
  labels:
    carbon-aware.io/enabled: "true"
    carbon-aware.io/workload-class: "batch"
  annotations:
    carbon-aware.io/expected-duration: "300s"
    carbon-aware.io/sla-deadline: "1800s"
    carbon-aware.io/carbon-weight: "0.7"
spec:
  schedulerName: carbon-aware-scheduler
  # requests/limits — из workload-манифеста; не дублируются annotation'ами
```

### 6.7. Node labels

| Ключ | Поверхность | Значения | Отсутствие |
| --- | --- | --- | --- |
| `carbon-aware.io/zone` | label узла | DNS-1123 (`north`, `west`, …) | G-09: \(S_C=1/2\), узел не в (2.14), не Unschedulable |
| `carbon-aware.io/power-profile` | label узла | имя из `powerProfiles`; канон `standard` | default \(P_{\mathrm{idle}},P_{\max}\) из args |

Невалидный zone id на узле трактуется как отсутствие зоны (G-09), а не как Error планирования.

Канон стенда (ARCH §8.1): worker-01/02 `north`, worker-03/04 `west`.

---

## 7. `CycleState` (API-STATE-01)

Канон поведения Fallback — [failure_and_fallback_policy.md](failure_and_fallback_policy.md) §5–§7. Здесь — типы и ключ.

| | Норма |
| --- | --- |
| Ключ | `carbon-aware.io/CarbonAware` (`CarbonCycleStateKey`) |
| Тип | `CarbonCycleState` |
| Запись | только `PreScore`, целиком |
| Чтение | `Score` / `NormalizeScore`; параллельно, без мьютекса |
| Мутации после `Write` | запрещены |
| `Clone()` | возвращает тот же указатель (immutable ⇒ shallow identity) |
| Жизнь | одна попытка планирования; при retry — новый объект |
| Запрет | кэш \(CI\) в `CycleState` не хранить; HTTP из `Score` нет |

Поля (минимум T3.4 §7.5 плюс T3.2):

| Поле | Назначение |
| --- | --- |
| `scorePerNode` | имя узла → \(s^{\mathrm{fp}}=\operatorname{rnd}(100\cdot\operatorname{Score})\in\{0,\ldots,10000\}\) |
| `fallbackMode` | `fresh` \| `stale` \| `dead` \| `empty` \| `all_invalid` \| `opt_out` |
| `dataStale` | `true` для SM-STALE, SM-DEAD, SM-EMPTY |
| `zonesMissing` | число NM-UNKNOWN |
| `invalidZones` | число NM-INVALID |
| `scoringMode` | копия args |
| `optInDisabled` | label `enabled` не `"true"` |
| `slaRejected` | G-12/G-20 (для аудита; при Error `Score` не читает state) |

G-08 / SM-DEAD / SM-EMPTY / `opt_out`: всем узлам `NeutralFixedPointScore()=5000`. После `NormalizeScore` это `50`. SM-STALE **не** подменяет Score константой 50 (полный (2.19)/(2.23), Warning).

`Score` при отсутствии ключа возвращает `(50, Success)` — не `Error` (FF-STATE).

---

## 8. Отображение предохранителей (API-GMAP-01)

Нормативный контракт `fwk.Status` — [failure_and_fallback_policy.md](failure_and_fallback_policy.md) §9. Ниже — проекция на HTTP CDS и уточнение opt-in. Формулы не повторяются.

### 8.1. CDS HTTP

| Состояние | HTTP | Примечание |
| --- | --- | --- |
| Валидный current/forecast | 200 | quality fresh/stale/estimated; stale **не** 5xx (Worker иначе не Store) |
| Нет прогноза | 200, `forecast: []` | G-06 в плагине |
| Неизвестная зона | 404 | Worker: зона из `/zones` не должна 404; плагин — G-09 |
| Невалидный CI от провайдера | не публикуется; last-good + `quality=stale` | G-10 на входе кэша |
| Кэш пуст (рестарт) | 503 | Worker не затирает snapshot |
| Некорректный duration/zone syntax | 400 | не G-* |
| Health ok | 200 | `status=ok` |
| Health last-good stale | 200 | `status=degraded`, `ready=true` |
| Health нет снимка | 503 | `status=unavailable`, `ready=false` |

Запрещено отдавать 500 на `/current` из-за ошибки провайдера, если last-good ещё есть (T3.4 §8.1).

### 8.2. `fwk.Status` плагина (копия T3.4 §9)

| Метод | Углеродный отказ | Hard SLA / битые annotations при enabled | Resource exhaustion |
| --- | --- | --- | --- |
| `PreScore` | `Success` | `Error` `"SLA precondition failed"` | не вызывается, если \(\mathcal{N}_{\mathrm{feas}}=\varnothing\) |
| `Score` | всегда `Success` + int64 | не вызывается | не вызывается |
| `NormalizeScore` | `Success`, значения в \([0,100]\) | не вызывается | не вызывается |

| Условие | Score | Не делать |
| --- | --- | --- |
| SM-FRESH | (2.19)/(2.23) | HTTP |
| SM-STALE | **тот же** полный Score | множитель \(\alpha\); путать со SM-DEAD |
| SM-DEAD / SM-EMPTY / all_invalid | 50 всем (G-08) | Error; гасить только \(S_C\); `Skip` |
| G-09 нет зоны / 404 зоны / invalid CI узла | \(S_C=1/2\); узел кандидат | Filter Unschedulable |
| G-06 пустой forecast | persistence (2.11) | подставлять future actual |
| G-12 / G-20 / невалидные ключи §6 при enabled | PreScore Error | `Score=0` |
| enabled ≠ `"true"` | 50 всем, `fallbackMode=opt_out` | считать G-08 / `fallback_total` |
| NaN в \(S_{\bullet}\) (G-21) | как G-09/G-08 по источнику | panic |

`Unschedulable` плагин **никогда** не возвращает.

---

## 9. Совместимость с исследовательскими JSON Schema

Фикстуры `experiments/datasets/schemas/live_current.schema.json` и `live_forecast.schema.json` остаются **входным** каноном T1.1 (`CONF-05`). Настоящий контракт:

- сохраняет обязательные поля и имена `carbonIntensity`, `forecast`;
- добавляет `apiVersion`/`kind` (omitempty на приёме);
- добавляет query `duration` и поля интервала `expiresAt`/`durationSeconds`;
- запрещает непустой `forecast` при `forecastMode` ∈ {`none`,`persistence`}.

При расхождении REST runtime с research schema прав **этот файл**. Правка schema датасетов — отдельная задача replay, не блокер T3.2.

---

## 10. Трассировка к архитектуре и модели

| Задание T3.2 (ARCH §17.1) | Где закрыто |
| --- | --- |
| JSON `CarbonRecord` / forecast, единицы, время, quality, source | §2, §4.1–4.2, `types.go` |
| Коды 200/404/400/503, тело ошибки, идемпотентность GET | §3.3–3.4 |
| `pluginConfig.args`, `scoringMode`, `staleWarnSeconds`, `clockSkewSeconds` | §4.7 |
| Pod annotations / Node labels | §6 |
| `CycleState` ключ, immutability, `Clone` | §7 |
| G-* → HTTP; `fwk.Status` копирует T3.4 §9 | §8 |
| `Clock` | §5.4 |
| Нет compute-score endpoint | API-REST-03, §3.1 |

---

## 11. Передача Stage 4 / Stage 5

### 11.1. Stage 4 (CDS)

Probes CDS: HTTP GET `/api/v1/healthz` (liveness и readiness смотрят на `ready`/`status`). Имена `carbon_aware_*` — в [metrics.md](metrics.md). Не менять пути REST.

1. Реализовать OpenAPI; адаптеры → `CarbonProvider`; кэш атомарный.
2. GET идемпотентен и не двигает replay-часы.

### 11.2. Stage 5 (плагин)

1. Worker: клиент по §3.1, timeout из args, сборка `CarbonSnapshot`.
2. `PreScore`: `NewWorkloadProfile`, `ParseNodeEnergyIdentity`, `EnergyModel`, `CarbonCalculator`, запись `CarbonCycleState`. Порядок — [01_architecture_ssot.md](01_architecture_ssot.md) §6.4.
3. Unit-тесты оракулов §14 модели — в `pkg/energy` / `pkg/carbon`, не в HTTP.

### 11.3. Что нельзя переоткрывать

- Синхронный HTTP в `Score`/`PreScore`.
- Углеродный `Filter`.
- Смена wire-формата времени на Unix без ADR (`VER-06`).
- Алиасы annotation-ключей.
- Endpoint «выбрать node» / «посчитать Score».
- Правка (2.1)–(2.23).
