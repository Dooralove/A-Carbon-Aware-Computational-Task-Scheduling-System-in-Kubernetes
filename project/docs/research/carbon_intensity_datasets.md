# Источники и форматы данных углеродной интенсивности

- **Версия:** 1.0.1
- **Статус:** draft (согласован с Gate 1)
- **Дата:** 2026-09-04
- **Владелец:** Data Engineer / Sustainable Computing Specialist (уровень L2)
- **Основание:** [governance.md](../ssot/governance.md), [engineering-specification.md](../ssot/engineering-specification.md) (§3.1, §32, §37–40, §52, §60–61, §84), SCOPE-IN-03, REPRO-01..03, VER-20
- **Машиночитаемые схемы:** [`experiments/datasets/schemas/`](../../experiments/datasets/schemas/)
- **Примерные Replay-срезы:** [`experiments/datasets/examples/`](../../experiments/datasets/examples/)

Документ задаёт канон данных для Carbon Data Service: Live-демонстрация, Replay-эксперименты и нормализация внешних источников во внутренние типы `Zone`, `Timestamp`, `CarbonIntensity`, `ForecastArray`. После ревью L1 контракт переносится в [interfaces.md](../ssot/interfaces.md); до этого SSOT не изменяется (SSOT-01).

---

## 1. Цель, границы и единицы

### 1.1. Зачем нужен отдельный канон данных

Алгоритм scoring использует не «текущее число с сайта», а временной ряд \(CI(t)\) в формуле

$$
\widehat{CF}=\sum_{i=1}^{N}
\frac{\widehat{P}_i\,\Delta t_i}{3.6\cdot10^6}\,\widehat{CI}_i
$$

где \(\widehat P_i\) — оценка мощности (W), \(\Delta t_i\) — длительность интервала (s),
\(\widehat{CI}_i\) — прогноз углеродной интенсивности (gCO2e/kWh), а делитель
\(3.6\cdot10^6\) переводит W·s в kWh. При отсутствии прогноза используется persistence
\(\widehat{CI}(t+\Delta)=CI(t)\) (SCOPE-EDGE-03, SCOPE-OUT-04). Без зафиксированной структуры ряда
эксперимент невоспроизводим: Live API завтра вернёт другие значения (REPRO-01, SCOPE-EDGE-06).

### 1.2. In-scope / out-of-scope для данных

| Входит | Не входит |
| --- | --- |
| Electricity Maps (live-адаптер + исторические срезы) | Live API как источник чисел диплома |
| NESO Carbon Intensity API (национальный GB, 30 мин) как вторичный эталон структуры | Собственный ML-прогноз CI |
| Синтетические профили Stable / Dynamic / Volatile | PostgreSQL / TimescaleDB / Redis для хранения рядов |
| JSON-схемы Replay-датасетов в `experiments/datasets/` | Сетевой, storage и embodied carbon (SCOPE-OUT-18) |
| Правила freshness / staleness для кэша | WattTime, cloud billing carbon APIs |

### 1.3. Каноническая единица

Во всех внутренних структурах, REST Carbon Data Service, датасетах и метриках единица одна:

```text
gCO2e/kWh
```

Это норма [DOD-DOC-05](../ssot/governance.md). Эквивалентные и **не** эквивалентные обозначения:

| Запись | Источник | Как трактовать |
| --- | --- | --- |
| `gCO2e/kWh` | канон проекта, governance | внутренняя единица |
| `gCO₂eq/kWh`, `gCO2eq/kWh` | Electricity Maps | **алиас** той же величины; адаптер записывает `unit: "gCO2e/kWh"` без пересчёта |
| `gCO2/kWh` | NESO | **не алиас.** Операционный CO₂ генерации, без GWP прочих газов и без lifecycle. Численно копируется в поле `carbonIntensity`, но `emissionFactorType` обязан быть `direct`, provenance сохраняется |

Пересчёт NESO → lifecycle **запрещён**: коэффициент не опубликован как часть API и был бы недокументированной гипотезой. Смешивать `lifecycle` и `direct` в одном экспериментальном сравнении запрещено.

Диапазон валидных значений: \(0 \le CI \le 2000\). Отрицательные, NaN, ±Inf — `invalid` (спецификация §63, DOD-TEST-04). Типовые европейские зоны лежат примерно в \(20\ldots 800\).

---

## 2. Семантика метрики Carbon Intensity

### 2.1. Direct vs lifecycle

Electricity Maps считает CI как произведение flow-traced mix технологий на emission factors. Параметр `emissionFactorType`:

| Тип | Что входит | Когда использовать |
| --- | --- | --- |
| `lifecycle` (default EM, **default диплома**) | сооружение станций, добыча топлива, эксплуатация, вывод из эксплуатации; GWP → CO₂e | Live-демо и Replay, если источник — Electricity Maps или synthetic, калиброванный под EM |
| `direct` | только операционные выбросы сгорания / процесса | NESO; чувствительность «direct vs lifecycle» — отдельный прогон, не смешивается с основным |

NESO явно: «CO₂ emissions related to electricity generation only» (крупные станции, импорт по interconnector, потери сети, embedded wind/solar). Это ближе к `direct`, чем к `lifecycle`.

### 2.2. Consumption-based vs production-based

Electricity Maps по умолчанию `flowTraced=true`: потреблённая электроэнергия с учётом перетоков. Production-based (`flowTraced=false`) описывает генерацию внутри зоны и **не** является каноном диплома: нагрузка потребляет импортированную энергию.

NESO regional модель тоже consumption-oriented (перетоки между DNO-регионами). После нормализации поле `flowTraced` для NESO ставится `true`.

### 2.3. Average, не marginal

Оба промышленных источника дают **average** grid intensity, не marginal (WattTime-стиль). Scoring диплома использует average CI. Утверждение «алгоритм снижает marginal emissions» в тексте диплома запрещено.

### 2.4. Interval semantics (критично для Replay)

Electricity Maps: точка с `datetime = 10:00` покрывает **полуинтервал** \([10:00,\,11:00)\) для hourly и \([10:00,\,10:05)\) для `5_minutes`.

NESO: явные `from` / `to` на 30-минутных settlement periods.

Канон проекта — **left-closed, right-open**, без интерполяции по умолчанию:

$$
CI(t)=CI[t_k]\quad\text{для }t\in[t_k,\,t_k+\Delta t)
$$

Линейная интерполяция допустима только как отдельный sensitivity-прогон и должна быть названа в карточке эксперимента. Default Replay Engine интерполяцию не делает: иначе два прогона с разным кодом интерполяции дадут разные \(\widehat{CF}\).

---

## 3. Источники

### 3.1. Electricity Maps (основной внешний)

| Свойство | Значение |
| --- | --- |
| Base URL | `https://api.electricitymap.org/v3` |
| Auth | заголовок `auth-token` (Secret / env; DOD-CODE-07) |
| Роль | `ElectricityMapsProvider` — **только Live-демо** (SCOPE-EDGE-06) |
| Исторические срезы | academic CSV / `past-range` API → файл в `experiments/datasets/`, затем только Replay |
| Гранулярность | `5_minutes`, `15_minutes`, `hourly` (default), грубее — только past |
| Прогноз | до 24–72 ч, параметр `horizonHours` ∈ {6, 24, 48, 72} |
| История API | `/carbon-intensity/history` — последние 24 ч |
| Зоны | ключи вида `DE`, `FR`, `GB`, `DK-DK2`, `NO-NO1`, `US-CAL-CISO` |
| Лицензия датасетов | academic CSV sunset для general users; академический доступ — отдельно |

Эндпоинты, которые обязан понимать адаптер:

```text
GET /carbon-intensity/latest
GET /carbon-intensity/history
GET /carbon-intensity/forecast
GET /carbon-intensity/past
GET /carbon-intensity/past-range
```

Query: `zone` **или** (`lat`,`lon`); `temporalGranularity`; `emissionFactorType`; `flowTraced`; `disableEstimations`.

Ответ `latest` (поля официальной документации):

```json
{
  "zone": "DK-DK2",
  "carbonIntensity": 318,
  "datetime": "2024-05-19T03:00:00.000Z",
  "updatedAt": "2025-07-25T12:32:42.039Z",
  "createdAt": "2024-05-16T03:48:50.419Z",
  "emissionFactorType": "lifecycle",
  "flowTraced": true,
  "isEstimated": false,
  "estimationMethod": "FORECASTS_HIERARCHY",
  "temporalGranularity": "hourly"
}
```

`history`: `{ "zone", "history": [ <тот же объект> ], "temporalGranularity" }`.

`forecast`: `{ "zone", "forecast": [ { "carbonIntensity", "datetime" } ], "updatedAt" }`. Точки прогноза **не** содержат `emissionFactorType` — наследуется от запроса (default `lifecycle`).

`past-range`: `{ "zone", "data": [ <latest-like> ], "temporalGranularity", "aggregationPeriod" }`.

JSON Schema внешнего конверта: [`electricity_maps.schema.json`](../../experiments/datasets/schemas/electricity_maps.schema.json).

Рекомендуемые зоны для **демонстрации** (высокий контраст, не для зачётных цифр): `NO-NO1` (гидро), `FR` (АЭС), `DE` или `PL` (смесь / уголь). Маппинг на логические `north` / `central` / `west` — только в конфигурации провайдера, не в коде scoring.

### 3.2. NESO / National Grid ESO (вторичный)

| Свойство | Значение |
| --- | --- |
| Base URL | `https://api.carbonintensity.org.uk` |
| Auth | не требуется |
| Лицензия | CC BY 4.0 |
| Гранулярность | **30 минут** (settlement period), не 5 и не 60 |
| Единица | `gCO2/kWh` generation-only |
| Прогноз | до 48 ч national / regional |
| Actual | появляется в конце периода; может быть **ретроспективно пересмотрен** |
| Индекс | `very low` \| `low` \| `moderate` \| `high` \| `very high` — качественный, в scoring не используется |

National current:

```json
{
  "data": [
    {
      "from": "2018-01-20T12:00Z",
      "to": "2018-01-20T12:30Z",
      "intensity": {
        "forecast": 266,
        "actual": 263,
        "index": "moderate"
      }
    }
  ]
}
```

Правило выбора значения при нормализации: `actual`, если не `null`, иначе `forecast`. Региональный API (17 DNO-регионов) в ряде ответов даёт только `forecast` — тогда берётся `forecast`.

NESO **не** является третьим live-провайдером SCOPE-IN-03. Три обязательных провайдера: `MockProvider`, `HistoricalDatasetProvider`, `ElectricityMapsProvider`. NESO — источник для построения Replay-файла либо эталон структуры half-hour ряда. Ресемплинг 30 мин → 5 или 60 мин: piecewise-constant на каноническую сетку, без линейной интерполяции.

Schema: [`neso.schema.json`](../../experiments/datasets/schemas/neso.schema.json).

### 3.3. Синтетические профили (основной путь экспериментов v1)

Пока academic-срез Electricity Maps не зафиксирован sha256, зачётные сценарии используют синтетику с явным `seed` (REPRO-04). Формулы — §7. Синтетика обязана проходить те же JSON Schema и те же правила единиц, что и исторический срез.

---

## 4. Канонические внутренние типы

Имена полей REST Carbon Data Service — **camelCase**, как в спецификации §3.1. Go-идентификаторы — с единицей в имени (`carbonIntensityGCO2PerKWh`, GO-11). Replay JSON использует тот же camelCase, чтобы Live и Replay отдавали идентичный снимок.

### 4.1. `Zone`

Строка DNS-1123 label, совпадает с `carbon-aware.io/zone`. Не является cloud region. Примеры: `north`, `central`, `west`. Внешний ключ источника хранится отдельно в `sourceZone` (`DK-DK2`, `GB`, …).

Узел без label не исключается (SCOPE-EDGE-02): carbon score нейтральный, запрос CI по зоне не выполняется.

### 4.2. `Timestamp`

RFC 3339 UTC, начало интервала наблюдения. Это **не** время HTTP-запроса.

Двое часов, которые нельзя смешивать:

| Часы | Поле | Смысл |
| --- | --- | --- |
| Observation | `timestamp` / EM `datetime` / NESO `from` | какой интервал сетки описывает CI |
| Ingestion | `fetchedAt` | когда Carbon Data Service положил значение в кэш |
| Source update | `updatedAt` | когда провайдер пересчитал точку (EM; NESO actual revisions) |
| Coverage end | `expiresAt` | `timestamp + intervalSeconds` — конец полуинтервала наблюдения |

### 4.3. `CarbonIntensity`

Число ≥ 0, единица всегда `gCO2e/kWh`. В JSON — `number` (дробные десятые допустимы; EM часто отдаёт integer).

### 4.4. `ForecastArray`

Упорядоченный по возрастанию `timestamp` список `{ timestamp, carbonIntensity, horizonSeconds }`. Пустой массив = нет прогноза → persistence. Максимум 288 точек (24 ч при 5 мин). Дубликаты timestamp запрещены.

**Запрещено** в default-эксперименте подставлять в `ForecastArray` будущие *actual* того же файла (perfect foresight): это завышает качество алгоритма. Допустимые режимы `forecastMode`:

| Режим | Поведение Replay Engine | Когда |
| --- | --- | --- |
| `none` | пустой ForecastArray | тест persistence / SCOPE-EDGE-03 |
| `persistence` | ForecastArray заполняется текущим CI на весь горизонт | default синтетики Stable/Dynamic |
| `recorded` | снимки `forecastSnapshots` на момент `issuedAt` | EM forecast, сохранённый в момент захвата; Volatile-пример |

Режим `oracle` (будущие actuals) в schema v1 отсутствует.

### 4.5. `CarbonRecord`

Расширение модели §32 спецификации:

| Поле | Обязательно | Примечание |
| --- | --- | --- |
| `zone` | да | логическая зона |
| `timestamp` | да | observation start |
| `carbonIntensity` | да | gCO2e/kWh |
| `unit` | да | const `gCO2e/kWh` |
| `source` | да | `electricity-maps` \| `neso` \| `historical-dataset` \| `synthetic` \| `mock` |
| `emissionFactorType` | да | `lifecycle` \| `direct` |
| `temporalGranularity` | да | `5_minutes` \| `15_minutes` \| `30_minutes` \| `60_minutes` |
| `carbonFreePercentage` | нет | EM CFE %; не используется в \(CF\) |
| `quality` | live | `fresh` \| `stale` \| `estimated` \| `invalid` \| `missing` |
| `expiresAt` | рекомендуется | coverage end |
| `fetchedAt` | live/replay cache | для freshness |

Schema: [`canonical.schema.json`](../../experiments/datasets/schemas/canonical.schema.json).

---

## 5. Нормализация: внешний ответ → внутренние типы

```text
Source JSON
    → parse + HTTP/schema validate
    → unit alias (gCO2eq → gCO2e; NESO gCO2 → gCO2e + emissionFactorType=direct)
    → timestamps → UTC RFC3339
    → zone map (sourceZone → Zone) из конфигурации, не из кода
    → granularity map (EM "hourly" → "60_minutes"; NESO → "30_minutes")
    → resample onto target grid (piecewise constant, optional)
    → validate: CI∈[0,2000], monotonic unique (zone,timestamp), no future vs series end
    → CarbonRecord / ForecastArray
```

Маппинг Electricity Maps `latest` → live `CarbonRecord`:

| EM | Канон |
| --- | --- |
| `zone` | `sourceZone`; логический `zone` — из config |
| `carbonIntensity` | `carbonIntensity` (float) |
| `datetime` | `timestamp` |
| `updatedAt` | `updatedAt` |
| `emissionFactorType` | `emissionFactorType` |
| `isEstimated` | `isEstimated`; если true, `quality=estimated` пока age свежий |
| `flowTraced` | `flowTraced` |
| `temporalGranularity: "hourly"` | `60_minutes`, `intervalSeconds=3600` |
| `temporalGranularity: "5_minutes"` | `5_minutes`, `intervalSeconds=300` |
| — | `fetchedAt = now` (wall в live, model в replay) |
| — | `expiresAt = timestamp + intervalSeconds` |
| — | `unit = "gCO2e/kWh"` |

Маппинг NESO точки:

| NESO | Канон |
| --- | --- |
| `from` | `timestamp` (нормализовать `2018-01-20T12:00Z` → `2018-01-20T12:00:00Z`) |
| `to` | `expiresAt` |
| `intensity.actual ?? intensity.forecast` | `carbonIntensity` |
| — | `emissionFactorType = "direct"` |
| — | `temporalGranularity = "30_minutes"` |
| `intensity.index` | отбрасывается (не вход scoring) |

Если `disableEstimations=true` и точка estimated — адаптер не кэширует её как fresh; это `missing` и запуск fallback.

---

## 6. Форматы Live и Replay

### 6.1. Live: Carbon Data Service REST

Канон путей — governance / спецификация §52, не черновик `/v1/carbon/...` из §3.1:

```text
GET /api/v1/zones
GET /api/v1/zones/{zone}/current
GET /api/v1/zones/{zone}/forecast
GET /api/v1/health
```

`GET .../current` — [`live_current.schema.json`](../../experiments/datasets/schemas/live_current.schema.json), пример [`live_current.example.json`](../../experiments/datasets/examples/live_current.example.json):

```json
{
  "zone": "north",
  "carbonIntensity": 82.4,
  "unit": "gCO2e/kWh",
  "timestamp": "2026-09-01T10:00:00Z",
  "source": "electricity-maps",
  "freshnessSeconds": 35,
  "quality": "fresh",
  "emissionFactorType": "lifecycle"
}
```

`freshnessSeconds = floor(now − fetchedAt)` в секундах. Не `now − timestamp`: иначе часовой ряд через 5 минут после начала часа ложно объявляется stale при `maxDataAgeSeconds=300`.

`GET .../forecast` — [`live_forecast.schema.json`](../../experiments/datasets/schemas/live_forecast.schema.json). Поле `forecast` есть `ForecastArray`.

Плагин **не** вызывает эти HTTP-ручки из `Score()` (SCOPE-OUT-17). Carbon Data Service обновляет in-memory cache; плагин читает уже нормализованный снимок.

### 6.2. Replay: файл датасета

Один JSON-файл на сценарий, все логические зоны вместе (иначе REPRO-12 нарушается: разные файлы разъедутся по версиям). Имя (VER-20):

```text
<source>_<zone>_<period>_v<N>.json
```

Для мультизоны: `zone = multizone`. Примеры:

| Файл | Сценарий | Гранулярность | Окно |
| --- | --- | --- | --- |
| `synthetic_multizone_stable_24h_v1.json` | Stable | 60_minutes | 24 ч |
| `synthetic_multizone_dynamic_24h_v1.json` | Dynamic | 60_minutes | 24 ч |
| `synthetic_multizone_volatile_2h_v1.json` | Volatile | 5_minutes | 2 ч |

Schema: [`replay_dataset.schema.json`](../../experiments/datasets/schemas/replay_dataset.schema.json).

Обязательные поля конверта: `schemaVersion`, `datasetId`, `source`, `scenario`, `unit` (= `gCO2e/kWh`), `emissionFactorType`, `temporalGranularity`, `intervalSeconds`, `modelStart`, `modelEnd`, `zones`, `series`, `forecastMode`.

`timeBase` всегда `"model"`: Replay Engine двигает модельные часы сценария, wall-clock игнорируется (REPRO-03). Стартовая точка = `modelStart` карточки эксперимента, либо смещение внутри окна, зафиксированное до прогона.

`series`: уникальные пары `(zone, timestamp)`, сортировка timestamp затем zone. Длина:

$$
|series|=\frac{t_{\mathrm{end}}-t_{\mathrm{start}}}{\Delta t}\times |zones|
$$

После первого зачётного прогона файл не редактируется; sha256 пишется в `environment.yaml` (REPRO-02, VER-20). Файлы > 10 МБ в git не кладутся (VER-14).

CSV/Parquet допустимы governance как хранение, но **вход HistoricalDatasetProvider v1 — JSON ReplayDataset**. Конвертация CSV EM → JSON — офлайн-шаг с тем же schema validate.

### 6.3. Поведение Replay Engine

```text
model_now
    → найти точку series: timestamp <= model_now < timestamp+Δt
    → CarbonRecord.fetchedAt := model_now   (кэш «обновлён» тиком)
    → freshnessSeconds := 0 на тике; растёт, если тик пропущен
    → ForecastArray := persistence | recorded snapshot с max issuedAt <= model_now
    → отдать тот же JSON, что и Live current/forecast
```

Если ряд 60-минутный, а poll/тик реже `maxDataAgeSeconds`, Replay обязан либо тикать чаще порога, либо выставлять `fetchedAt = model_now` при каждом чтении. Иначе hourly-датасет через 301 с модельного времени выключит carbon scoring — это баг часов, а не свойств сетки.

Провайдеры (SCOPE-IN-03) снаружи выглядят одинаково: `Current(zone) → CarbonRecord`, `Forecast(zone) → ForecastArray`.

---

## 7. Три типовых сценария Replay

Критерии — свойства \(CI(t)\), изолирующие гипотезы SCOPE-IN-14 (волатильность CI). Нагрузки, веса и топология в этом документе не задаются.

Общие зоны стенда: `north`, `central`, `west`. Модельная эпоха примеров: `2026-09-01T00:00:00Z`. Генератор: [`experiments/datasets/_generate_and_validate.py`](../../experiments/datasets/_generate_and_validate.py) (seed в конверте).

### 7.1. Scenario 1 — Stable

**Гипотеза:** при почти постоянном CI и фиксированном ранжировании зон carbon-aware выигрыш (если есть) объясняется **уровнем** CI между зонами, а не таймингом внутри суток. Default-scheduler и carbon-aware расходятся слабо по makespan и сильно по \(\widehat{CF}\), если зоны изначально разнесены.

| Параметр | Значение |
| --- | --- |
| Гранулярность | `60_minutes` (достаточно: спектр почти DC) |
| Окно | 24 ч |
| Приёмка | CV всех зон \(< 0.05\); \(\max\|\Delta CI\| < 10\) gCO2e/kWh за шаг; `rankingReversals = 0` |
| Fixture | CV ∈ [0.0103, 0.0249], max step \(1.6\) gCO2e/kWh, reversals \(0\) |
| `forecastMode` | `persistence` |

Профиль (seed = 1):

$$
CI_z(h)=\mu_z+a_z\sin\left(\frac{2\pi h}{24}\right)
$$

\(\mu=(85,240,410)\), \(a=(3,5,6)\) для north / central / west. Ранжирование постоянно: north < central < west.

### 7.2. Scenario 2 — Dynamic

**Гипотеза:** суточный солнечный/ветровой цикл создаёт окно, в котором **другая** зона становится минимальной по CI. Выигрыш carbon-aware vs default зависит от того, попадает ли runtime Pod в это окно; forecast vs persistence здесь впервые материален для длинных batch.

| Параметр | Значение |
| --- | --- |
| Гранулярность | `60_minutes` (суточная гармоника); 5-min вариант — тот же профиль, upsample piecewise-constant |
| Окно | 24 ч |
| Приёмка | CV наиболее переменной зоны \(\ge 0.15\); \(\ge 1\) ranking reversal; шаг типично \(20\ldots 80\) gCO2e/kWh (не «обрыв») |
| Fixture | CV max \(0.3975\), max step \(53.9\) gCO2e/kWh, reversals \(2\) |
| `forecastMode` | `persistence` (recorded — когда появится архив EM forecast) |

Профиль (seed = 2), \(h\) — час UTC:

$$
s(h)=\max\bigl(0,\cos(\pi(h-12)/12)\bigr),\quad
w(h)=0.5+0.5\sin(2\pi(h-3)/24)
$$

$$
\begin{aligned}
CI_{\mathrm{north}}&=90+50s-40w\\
CI_{\mathrm{central}}&=280-200s-20w\\
CI_{\mathrm{west}}&=400-40s-30w
\end{aligned}
$$

Контрольные точки fixture (gCO2e/kWh):

| Время | north | central | west | min zone |
| --- | --- | --- | --- | --- |
| 00:00 | 84.1 | 277.1 | 395.6 | north (ветер ночью) |
| 12:00 | 105.9 | 62.9 | 334.4 | central (солнце днём) |

Это намеренно воспроизводит логику защиты из спецификации §114, но на гладком суточном цикле, а не на ступеньке.

### 7.3. Scenario 3 — Volatile

**Гипотеза:** резкие скачки CI меняют выбранный узел для **новых** Pod; уже running Pod не мигрирует (SCOPE-OUT-01). Fallback по freshness **не** должен срабатывать: данные свежие, но ряд жёсткий. Нужна 5-минутная сетка, иначе ступенька размазывается по часу.

| Параметр | Значение |
| --- | --- |
| Гранулярность | `5_minutes` |
| Окно | 2 ч (`10:00Z`–`12:00Z`) |
| Приёмка | \(\max\|\Delta CI\| \ge 300\) gCO2e/kWh за один шаг; \(\ge 2\) ranking reversals |
| Fixture | max step \(340.0\) gCO2e/kWh, reversals \(3\), CV ∈ [0.4458, 0.6446] |
| `forecastMode` | `recorded` (снимок на `modelStart`, горизонт 1 ч) |

Кусочно-постоянный профиль (seed = 3), gCO2e/kWh:

| Интервал (model) | north | central | west | min |
| --- | --- | --- | --- | --- |
| 10:00–10:25 | 85 | 260 | 410 | north |
| 10:25–11:00 | 420 | 250 | 70 | west |
| 11:00–11:35 | 90 | 400 | 380 | north |
| 11:35–12:00 | 380 | 70 | 410 | central |

Скачок north \(85 \to 420\) на границе 10:25 — \(335\) gCO2e/kWh за 5 мин. Плагин использует новое значение только для последующих решений.

### 7.4. Какую гранулярность брать в зачётных прогонах

| Вопрос | 5 минут | 60 минут |
| --- | --- | --- |
| Объём на 24 ч × 3 зоны | 864 точки | 72 точки |
| Stable | избыточна | **рекомендуется** |
| Dynamic (суточный цикл) | уточняет рампы | **достаточна** для гармоники 24 ч |
| Volatile | **обязательна** | скрывает скачки внутри часа |
| Согласование с `maxDataAgeSeconds=300` | Δt = порогу freshness | Δt ≫ порога → см. §6.3 (не путать coverage и cache age) |
| Согласование с EM | `temporalGranularity=5_minutes` (не везде Tier A) | default API `hourly` |

Для сопоставимости A/B/C на одном факторе волатильности: Stable и Dynamic — 60 мин; Volatile — 5 мин. Не сравнивать проценты снижения CO₂ между сценариями с разной сеткой без оговорки.

---

## 8. Freshness, staleness, fallback

### 8.1. Определение

Спецификация §84:

$$
\mathrm{Freshness}=t_{\mathrm{now}}-t_{\mathrm{data}}
$$

В каноне \(t_{\mathrm{data}} \equiv\) `fetchedAt` (ingestion), не observation `timestamp`.

$$
\mathrm{freshnessSeconds}=\lfloor t_{\mathrm{now}}-t_{\mathrm{fetchedAt}}\rfloor
$$

Live: \(t_{\mathrm{now}}\) — wall-clock UTC сервиса. Replay: модельные часы.

### 8.2. Пороги

Governance фиксирует `maxDataAgeSeconds = 300` (SCOPE-EDGE-04) и цепочку SCOPE-IN-09:

```text
fresh cache → stale cache → отключение carbon scoring → baseline
```

SCOPE-EDGE-04 формулирует «старше 300 с → scoring выключается», что сжимает окно stale почти до нуля. **Рекомендация L2 (не применяется, пока L1 не примет SSOT CR):**

| Состояние | Условие | `quality` | Поведение plugin |
| --- | --- | --- | --- |
| fresh | `freshnessSeconds ≤ 300` | `fresh` (или `estimated`, если `isEstimated`) | carbon scoring |
| stale | `300 < freshnessSeconds ≤ 1800` | `stale` | scoring на последнем кэше; инкремент stale/fallback метрики |
| expired | `freshnessSeconds > 1800` или запись `invalid`/`missing` | `missing` | carbon scoring off, нейтральный score, `fallback_total++` |

До принятия CR реализация должна следовать буквальному SCOPE-EDGE-04: единственный порог 300 с, «stale cache» = кэш при недоступности API, пока age ≤ 300 с. Рекомендация двух порогов нужна, чтобы SCOPE-IN-09 не был мёртвым кодом.

`expiresAt` (конец интервала наблюдения) **не** включает fallback. Часовая точка 10:00 в 10:04 всё ещё описывает текущий час.

### 8.3. Невалидные данные (§63)

Любое из: `CI < 0`, NaN/Inf, `timestamp` в будущем относительно `now` более чем на один интервал, неизвестный `zone`, нарушение schema → запись не попадает в fresh cache. Предыдущий валидный кэш используется по правилам §8.2.

Резкий скачок CI (Scenario 3) **валиден**, если schema и freshness в норме. Нельзя трактовать \(|\Delta CI| > X\) как ошибку источника.

### 8.4. Метрики качества данных (для metrics.md, не правка SSOT)

Рекомендуемые имена (GO-11 / DOD-CODE-13, вносит L1):

- `carbon_aware_data_freshness_seconds{zone,source}`
- `carbon_aware_data_quality{zone,quality}`
- `carbon_aware_fallback_total{reason}` где `reason∈{stale,expired,invalid,source_down,unknown_zone}`

---

## 9. Валидация

Проверено 2026-09-04 скриптом [`_generate_and_validate.py`](../../experiments/datasets/_generate_and_validate.py) (stdlib, без внешних пакетов): JSON Schema draft 2020-12 по subset `type/const/enum/required/properties/additionalProperties/minimum/maximum/pattern/format/minItems/items` плюс семантические инварианты Replay.

| Проверка | Результат |
| --- | --- |
| Три Replay-файла против `replay_dataset.schema.json` | OK |
| `unit` везде const `gCO2e/kWh` | OK |
| `intervalSeconds` согласован с `temporalGranularity` | OK |
| Длина `series` = окно / Δt × \|zones\| | 72 / 72 / 72 |
| Scenario gates Stable / Dynamic / Volatile | OK |
| Live current / forecast examples | OK |
| NESO example | OK |
| EM latest example: семантика `gCO2eq`, поле integer 318, `emissionFactorType=lifecycle` | OK |
| Отрицательный CI, NaN | отвергаются (`number` + `minimum: 0`; NaN в JSON нет) |

Повтор: `python experiments/datasets/_generate_and_validate.py`.

---

## 10. Выводы для архитектурного проектирования

1. **Один внутренний контракт, три адаптера.** `CarbonProvider` отдаёт `CarbonRecord` + `ForecastArray`. Electricity Maps, NESO (офлайн) и synthetic/historical — только нормализаторы. Scheduler не знает источника.
2. **Двое часов обязательны в модели кэша.** `timestamp` (сетка) vs `fetchedAt` (свежесть). Смешение ломает hourly Replay при `maxDataAgeSeconds=300`.
3. **Replay — не «проиграть CSV».** Это тот же REST-снимок, что и Live, но `now` модельный. Иначе e2e и эксперимент тестируют разные JSON.
4. **Единица и фактор эмиссии — часть ключа датасета.** Нельзя сравнивать прогон `lifecycle` с прогоном `direct` как повтор. Поле в `environment.yaml` рядом с sha256.
5. **Default forecast = persistence.** Recorded forecast — когда снимок сохранён в момент \(t\). Perfect foresight не default.
6. **Три сценария закрывают ортогональные оси:** уровень (Stable), суточная гармоника и смена ранжирования (Dynamic), разрыв и 5-min (Volatile). Это минимальный набор для главы «волатильность CI».
7. **5 мин и 60 мин сосуществуют**, ресемплинг только piecewise-constant. NESO 30 мин — промежуточный ingest, не третий канон сетки.
8. **Нет БД.** Файл JSON + in-memory cache. Соответствует SCOPE-OUT-05/10.
9. **Live EM остаётся за отдельным профилем деплоя.** Экспериментальный ConfigMap указывает `provider: historical` и `datasetId`.
10. **Открытый вопрос к L1:** двухпороговое freshness (§8.2) vs буквальный SCOPE-EDGE-04. Без решения `stale cache` в SCOPE-IN-09 не специфицирован. До решения действует один порог 300 с: валидный кэш при недоступности API используется, пока `freshnessSeconds ≤ 300`; иначе carbon scoring выключается.
11. **Связь со стратегиями эксперимента.** Стратегия B (`SCOPE-IN-13`) ранжирует по текущему average \(CI\) зоны (persistence текущего значения, без интеграла по runtime). Стратегия C подставляет в \(\widehat{CF}\) ряд \(\widehat{CI}_i\) на горизонте expected runtime: `ForecastArray`, иначе persistence. Perfect foresight (`oracle`) не используется.

---

## 11. SSOT Change Request (для L1)

См. также аудит `docs/audit/T1.1-carbon-intensity-datasets.md`. Кратко:

- Заполнить [interfaces.md](../ssot/interfaces.md) (модель данных, SLA freshness) ссылкой на этот документ после accept.
- Развести `maxDataAgeSeconds` (ingestion) и покрытие интервала наблюдения.
- Каноническая единица в glossary: `gCO2e/kWh`, алиас `gCO2eq/kWh`.

Альтернатива без правки SSOT: держать канон в `docs/research/` до заполнения `interfaces.md` архитектором.

---

## 12. Ссылки

- Electricity Maps API: [latest](https://app.electricitymaps.com/docs/carbon-intensity/latest), [history](https://app.electricitymaps.com/docs/carbon-intensity/history), [forecast](https://app.electricitymaps.com/docs/carbon-intensity/forecast), [past-range](https://app.electricitymaps.com/docs/carbon-intensity/past-range), [concepts (granularity, emission factors)](https://app.electricitymaps.com/api/docs/concepts-and-parameters)
- Electricity Maps academic datasets: [help.electricitymaps.com — academic access](https://help.electricitymaps.com/en/articles/13168512-academic-data-access-and-availability)
- NESO Carbon Intensity API: [carbon-intensity.github.io/api-definitions](https://carbon-intensity.github.io/api-definitions/), [api.carbonintensity.org.uk](https://api.carbonintensity.org.uk/), methodology [neso.energy regional report](https://www.neso.energy/document/373936/download)
- Governance: [docs/ssot/governance.md](../ssot/governance.md) SCOPE-IN-03, SCOPE-EDGE-04/06, REPRO-01..03, VER-20, DOD-DOC-05
- Инженерная спецификация: §3.1, §32 CarbonRecord, §37–40 Replay, §52 REST, §60–61, §84
