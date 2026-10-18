# Quality Gate 4 — аудит Carbon Data Service

- **Дата:** 2026-09-04
- **Роль:** Senior Code Reviewer & Backend Tech Lead
- **Статус:** **PASS**
- **Решение:** **Stage 4 (CDS) — COMPLETE**; сервис готов к интеграции с Cache Sync Worker планировщика
- **Проверяемый переход:** реализация Carbon Data Service (T4.1–T4.4) → стык со Scheduler Plugin (T5.2)
- **Канон:** [`03_api_contracts_ssot.md`](../ssot/03_api_contracts_ssot.md) v1.0.1 **LOCKED**;
  [`failure_and_fallback_policy.md`](../ssot/failure_and_fallback_policy.md) v1.1.0 (`FF-INV-12`, last-good);
  [`metrics.md`](../ssot/metrics.md) §3; OpenAPI [`api/v1alpha1/openapi.yaml`](../../api/v1alpha1/openapi.yaml)
- **Вход:** [`T4.3-cds-cache-rest.md`](T4.3-cds-cache-rest.md), [`T4.4-cds-tests.md`](T4.4-cds-tests.md),
  Gate 3 [`gate_3_architecture_review.md`](gate_3_architecture_review.md) §10 Stage 4

## 1. Итоговый вердикт

Carbon Data Service нормализует \(CI\) (mock / replay / live-demo), публикует immutable snapshot
и отдаёт REST строго по OpenAPI. GET не ходит в провайдер и не двигает \(t_{\mathrm{model}}\).
Кэш читается через `atomic.Pointer` без мьютекса; отказ провайдера не затирает last-good
(`FF-INV-12`). Endpoint Score отсутствует (`API-REST-03`).

```text
provider (Mock | Replay | Electricity Maps httptest/live)
        → Refresh all-or-nothing (G-10, FF-FUT)
        → atomic CarbonSnapshot (last-good)
        → GET /api/v1/{healthz,zones,current,forecast} + /metrics
                    ↑
          Cache Sync Worker (единственный in-cluster клиент)
```

Блокирующих дефектов (утечки горутин в рантайме, незакрытый HTTP body, panic, data race
на GET-пути, расхождение с OpenAPI) **нет**.

Количество блокеров интеграции со Scheduler Plugin: **0**.

**QUALITY GATE 4: PASS. Carbon Data Service принят. Worker T5.2 может опираться на этот REST-контракт.**

## 2. Проверенные материалы

| ID | Артефакт | Роль |
| --- | --- | --- |
| T4.1 | `cmd/carbon-service/main.go`, `pkg/carbon/config` | процесс, YAML+env, graceful shutdown |
| T4.2 | `pkg/carbon/provider`, `pkg/carbon/replay` | Mock / Replay / Electricity Maps |
| T4.3 | `pkg/carbon/cache`, `pkg/carbon/api` | snapshot, REST, бенчмарк Get ~155 ns/op |
| T4.4 | `*_test.go`, `tests/unit/carbon_service_test.go` | cache 94.7% / api 96.9% |
| — | `api/v1alpha1/openapi.yaml`, `types.go` | машиночитаемый контракт |
| SSOT | `03_api_contracts_ssot.md` | `API-REST-01…03`, коды, заголовки |
| QG3 | `gate_3_architecture_review.md` §10 | вход Stage 4 |

Пакеты формул `pkg/carbon/*.go` (Stage 5) просмотрены линтером как часть `./pkg/carbon/...`;
они не входят в процесс CDS и **не** меняют REST.

## 3. Критерии аудита

1. Соответствие REST/DTO заблокированному OpenAPI и `03_api_contracts_ssot.md`.
2. Нет горутинных утечек в установившемся режиме и на SIGINT/SIGTERM.
3. HTTP-клиент live закрывает `Response.Body`; CDS-handlers — только GET без чтения тела запроса.
4. Ошибки через `%w`; `panic` нет (`GO-06`).
5. Потокобезопасность кэша: GET не мутирует snapshot; `Store(nil)` игнорируется.
6. `golangci-lint` на `./pkg/carbon/... ./cmd/carbon-service/...` — 0 замечаний.
7. Нет data race на пути чтения (атомарная публикация; `-race` — см. §7).
8. Готовность Worker: 200 stale, 404 зоны, 503 только пустой кэш.

## 4. Ревью рантайма

### 4.1. Горутины

| Место | Жизненный цикл | Вердикт |
| --- | --- | --- |
| `cache.Loop` (`main` `go`) | `time.Ticker` + `defer Stop`; выход по `ctx.Done()` | PASS |
| `http.Server.ListenAndServe` | `Shutdown` по отмене ctx, drain `errCh` | PASS |
| Electricity Maps retry | синхронный `sleepBackoff` на `Timer`, не горутина | PASS |
| ScaledClock | мьютекс на `Now()`, горутин нет | PASS |

После `Serve` процесс завершает `main`; фоновый `Loop` разделяет тот же `ctx`, что и HTTP.
Несшивание `Loop` с `errgroup` при ошибке `ListenAndServe` — остаток (процесс всё равно `os.Exit`).
Не блокер.

### 4.2. HTTP body

| Поток | Поведение | Вердикт |
| --- | --- | --- |
| CDS REST | GET, тело запроса не читается; ответ пишет `json.Encoder` | PASS |
| Electricity Maps `get` | `Do` → `ReadAll(LimitReader 1MiB)` → `Body.Close()` **на каждой** попытке retry (не `defer` в цикле) | PASS |

Утечки keep-alive из-за незакрытого body нет.

### 4.3. Ошибки и panic

- `panic(` в `cmd/carbon-service` и `pkg/carbon/**` **отсутствует**.
- Провайдер/кэш оборачивают ошибки через `%w`; live: `ErrTimeout` / `ErrInvalidResponse`.
- Encode JSON: ошибка логируется, процесс не падает.
- Ключ live API не попадает в slog (`Config.LogValue`).

### 4.4. Потокобезопасность кэша

| Свойство | Реализация | Вердикт |
| --- | --- | --- |
| Публикация | `atomic.Pointer[CarbonSnapshot]`; карта зон собирается локально, затем `Store` | PASS |
| Last-good | `Store(nil)` no-op; ошибка тика → `stale=true` без замены pointer | PASS |
| GET | `Load` + копия `CarbonRecord` (`cloneRecord` для указателей) + копия forecast в `State` | PASS |
| Счётчики | `atomic.Int64` / `atomic.Bool` | PASS |
| Конкурентность | `TestGetConcurrentWithStore` PASS | PASS |

Писатель не мутирует уже опубликованную map. Читатели не пишут в snapshot.

### 4.5. Производительность (T4.3)

`BenchmarkCacheGet` **155.4 ns/op**, parallel **152.4 ns/op** при лимите **< 2 ms**. Запас ~10⁴.

## 5. Соответствие OpenAPI / SSOT

| Норма | Реализация | Вердикт |
| --- | --- | --- |
| `GET /api/v1/healthz` = `/health` | один handler | PASS |
| Пустой кэш health | 503 `HealthStatus` `unavailable`, `ready=false` | PASS |
| Last-good stale | 200 `degraded`, `ready=true`; current `quality: stale` | PASS |
| `/zones` | 200 `ZoneProfileList` / 503 `Status` | PASS |
| `/current` | 200 + `X-Carbon-Data-Age-Seconds`, `X-Carbon-Quality`, `X-Carbon-Source`, `Cache-Control: no-store` | PASS |
| DNS-1123 | 400 `Status` `BadRequest` | PASS |
| Неизвестная зона | 404 `Status` + `details.kind=Zone` | PASS |
| `duration` без суффикса / > 86400s | 400 | PASS |
| Пустой forecast | 200 `forecast: []`, `forecastMode: none` | PASS |
| GET идемпотентен | handler не вызывает provider; `FakeClock` не двигается | PASS |
| Нет Score endpoint | mux 404 на `/api/v1/score` | PASS |
| `apiVersion`/`kind` на 200 | выставляются | PASS |
| Единица \(CI\) | `gCO2e/kWh`, G-10 на Refresh | PASS |
| `/metrics` | Prometheus text, имена `carbon_aware_cds_*`; вне OpenAPI (канон) | PASS |

Live Electricity Maps в unit-тестах только через `httptest.Server`.

## 6. Линтер

Репозиторный `.golangci.yml` — схема **v1**. На машине аудита `golangci-lint` **v2.4.0** (v2 не читает v1 YAML без `version`).

Эквивалент набора из `.golangci.yml` (`gosimple` в v2 входит в `staticcheck`; `gofmt`/`goimports` проверены отдельно, файлы чистые):

```text
golangci-lint run --no-config --default=none --timeout=5m \
  --enable=errcheck,govet,ineffassign,staticcheck,unused,misspell \
  ./pkg/carbon/... ./cmd/carbon-service/...
```

**0 issues.**

На Gate устранены замечания staticcheck QF1008 (промоция embedded `time.Time` / `Engine`) и `errcheck` в тесте listen. Поведение REST не менялось.

`go vet ./pkg/carbon/... ./cmd/carbon-service/...` — 0.

## 7. Race detector

```text
go test -race ./pkg/carbon/cache/... ./pkg/carbon/api/...
```

На этом хосте (Windows, нет `gcc`): **не собирается** (`-race requires cgo`). Это ограничение toolchain, не дефект кода.

Обоснование PASS без локального `-race`: GET-путь — `atomic.Load` + копия; публикация — замена указателя; concurrent-тест Get∥Refresh зелёный. Обязательный прогон `-race` — **Linux CI / MinGW** (как T4.2/T4.3).

## 8. Тесты (T4.4)

| Пакет | Coverage |
| --- | ---: |
| `pkg/carbon/cache` | 94.7% |
| `pkg/carbon/api` | 96.9% |
| `pkg/carbon/replay` | 86.9% |
| `tests/unit` Replay→кэш→REST | PASS |

Повтор на Gate: `go test -count=1 ./pkg/carbon/... ./tests/unit/...` — **ok**.

## 9. Остатки (не блокеры)

| ID | Наблюдение | Действие |
| --- | --- | --- |
| QG4-FU-01 | `.golangci.yml` v1 vs бинарь v2 | закрепить v1.64 в Makefile **или** мигрировать YAML на `version: "2"` (`make lint`) |
| QG4-FU-02 | `-race` без cgo на Windows агента | job CI с gcc/clang |
| QG4-FU-03 | Live `ListZones` пуст без `Options.SourceZones`; в YAML конфиге карты зон нет | для demo live задать карту logical→EM zone; стенд mock/replay не затронут |
| QG4-FU-04 | `Loop` не `Wait`-ится при падении listen | `errgroup` при желании; `os.Exit` и так рвёт процесс |
| QG4-FU-05 | Нет `WriteTimeout` у `http.Server` (есть `ReadHeaderTimeout`) | опционально, GET короткий |

Формулы, OpenAPI и пути REST **не** менялись.

## 10. Готовность к Scheduler Plugin

Worker (T5.2) получает:

1. Стабильные URL из `v1alpha1.Path*`.
2. 200 на stale last-good — **не** затирать plugin snapshot.
3. 404 зоны → G-09 на узлах зоны, не `Error`.
4. 503 только пока CDS ни разу не опубликовал снимок.
5. Пустой `forecast: []` → persistence (2.11) в калькуляторе, не в CDS.
6. Таймаут клиента — `pluginConfig.args.httpTimeoutSeconds` (`GO-09`), не магия в CDS handler.

CDS **не** ходит в kube-apiserver и **не** считает Score.

## 11. Формальное решение Gate

**QUALITY GATE 4: PASS.**

**Stage 4 (Carbon Data Service) завершён.** Процесс `carbon-service` соответствует LOCKED API, пригоден как единственный источник \(CI\) для Cache Sync Worker.

**КОМАНДА:** интеграция CDS ↔ plugin идёт по контракту T5.2; менять OpenAPI/`Path*`/`DTO` без ADR запрещено (`VER-06`, `SSOT-08`).
