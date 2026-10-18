# Quality Gate 5 — аудит Scheduler Plugin CarbonAware

- **Дата:** 2026-09-04
- **Роль:** Principal Kubernetes Core Auditor & Math Inspector
- **Статус:** **PASS**
- **Решение:** **Stage 5 — COMPLETE**; **Stage 6 (интеграция с Kubernetes) — UNLOCKED**
- **Проверяемый переход:** реализация плагина (T5.1–T5.5) → сборка в образ / kind / `schedulerName` (DOD-TEST-05/06)
- **Канон:** [`02_math_model_ssot.md`](../ssot/02_math_model_ssot.md) v1.0.0 **LOCKED** (2.1)–(2.23), G-01…G-21;
  [`01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) v1.1.0 (`ARCH-01`…`ARCH-16`);
  [`failure_and_fallback_policy.md`](../ssot/failure_and_fallback_policy.md) v1.1.0;
  [`03_api_contracts_ssot.md`](../ssot/03_api_contracts_ssot.md) v1.0.1 (`API-STATE-01`, args)
- **Вход:** [`T5.5-scheduler-unit-tests.md`](T5.5-scheduler-unit-tests.md),
  Gate 3 [`gate_3_architecture_review.md`](gate_3_architecture_review.md) §10 Stage 5,
  Gate 4 [`gate_4_carbon_service_review.md`](gate_4_carbon_service_review.md)

## 1. Итоговый вердикт

Плагин `CarbonAware` реализует заблокированную модель: \(P(u)\) с \(\operatorname{clip}\), IDLE-NONE \(\widehat{P}_{\mathrm{pod}}\), (2.10s) с \(\eta=1/3.6\cdot10^{6}\), hard SLA в `PreScore`, min-max \(S_C\) в `PreScore` (G-04), `Score` = \(O(1)\) lookup, `NormalizeScore` = (2.21) без повторного min-max. Горячий путь не выполняет HTTP, диск, `Wait`. Скрытых блокировок и утечек горутин/HTTP body на пути скоринга нет.

```text
Filter (standard) → PreScore O(NK): SLA, one Snapshot.Load, (2.19)/(2.23)
                  → CycleState {scorePerNode s_fp}
                  → Score O(1) map lookup, always Success
                  → NormalizeScore rnd(s_fp/100) clamp [0,100]
Cache Sync Worker (goroutine, ticker+timeout) ⟂ scheduling cycle
```

Контроль Gate: `Score` **94.10 ns/op** на синтетическом CycleState (бюджет **100 µs/node**). Запас \(\approx 10^{3}\).

Покрытие `pkg/scheduler` **96.3%**. `go test -race` — чисто. `golangci-lint` (эквивалент репозиторного набора) — **0 issues**.

Количество блокеров Stage 6: **0**.

**QUALITY GATE 5: PASS. Плагин соответствует SSOT, протестирован и готов к сборке в контейнер. Stage 6 (интеграция с Kubernetes) разблокирован.**

## 2. Проверенные материалы

| ID | Артефакт | Роль |
| --- | --- | --- |
| T5.1 | `cmd/scheduler/main.go`, `app.WithPlugin` | бинарь kube-scheduler 1.37 |
| T5.2 | `cache_sync.go`, `worker.go`, `cds_client.go` | Worker, `atomic.Pointer`, last-good |
| T5.3 | `prescore.go`, `score.go`, `normalize.go`, `cycle_state.go` | extension points |
| T5.4 | `pkg/energy`, `pkg/carbon` (корень), `pkg/runtime` | (2.2)–(2.4), (2.10s), (2.12)–(2.23) |
| T5.5 | `tests/unit/scheduler_score_test.go`, `*_test.go` | сценарии 1–5, Fallback, оракул 14.3 |
| — | `pkg/scheduler/score_bench_test.go` | бенчмарк Score / NormalizeScore / PreScore |
| SSOT | `02_math_model_ssot.md` §8–11, §14 | формулы, G-*, сложность |
| QG3 | `gate_3_architecture_review.md` | вход Stage 5 |
| QG4 | REST CDS | контракт Worker |

LOCKED SSOT **не изменялся**. Правки на Gate — гигиена линтера (см. §9), не формулы.

## 3. Критерии аудита

1. Нормализация Score = (2.21), не min-max \(S_C\) в `NormalizeScore` (G-04 считается в PreScore).
2. Нет деления на ноль и переполнения на G-02, G-04, G-14, G-18, интеграле.
3. Контракт CycleState: ключ `API-STATE-01`, immutable после `Write`, `Clone` identity, `Score` не пишет.
4. `Score` \(\le 100\,\mu\mathrm{s}\) на узел (синтетика, без сети).
5. Нет скрытых блокировок / HTTP / `Mutex` на горячем пути Score.
6. Нет утечек: Worker `Ticker.Stop` + `ctx.Done()`, HTTP `Body.Close`, bounded body 1 MiB.
7. Соответствие G-01…G-21 и оракулам §14.
8. Линтер + `go test -race` + покрытие \(>90\%\).
9. Бинарь `cmd/scheduler` собирается; плагин готов к образу Stage 6.

## 4. Аудит алгоритмов

### 4.1. Нормализация Score (2.21)

| Шаг | Канон | Код | Вердикт |
| --- | --- | --- | --- |
| Внутренний Score | \(100(w_C S_C+w_R S_R+w_P S_P)\in[0,100]\) | `WeightedScore` | PASS |
| Fixed-point | \(s^{\mathrm{fp}}=\operatorname{rnd}(100\cdot\operatorname{Score})\in\{0,\ldots,10000\}\) | `FixedPointScore`, NaN/Inf → 5000 | PASS |
| `Score()` | возвращает \(s^{\mathrm{fp}}\), не \([0,100]\) | `score.go` lookup | PASS; фреймворк зовёт `ScoreExtensions` |
| `NormalizeScore` | \(\operatorname{rnd}(s^{\mathrm{fp}}/100)\), clamp \([0,100]\) | `FrameworkScore` | PASS |
| Min-max \(S_C\) | только PreScore, формулы (2.15)/(2.23) | `CarbonScore` / `CurrentCIScore` | PASS |
| Ничья Max=Min | равные \(s^{\mathrm{fp}}\) остаются равными | тесты 3/5/20 узлов | PASS |
| G-19 | clamp | обе функции режут выход | PASS |

`NormalizeScore` **не** пересчитывает \(\widehat{CF}\) и **не** делает min-max по списку `NodeScoreList`. Это запрещённый informal путь; канон §9.2 / §15 соблюдён.

Оракул 14.3: fixed-point 8000/1600 → framework 80/16. Сценарий T5.5 North 80 vs West 400 (равные ресурсы): 84/24. Совпадает с ручным счётом (IDLE-NONE, \(\eta\), \(\lambda=1\)).

Стратегия B: `scoringMode=current-ci` → (2.23) `CurrentCIScore`, \(w=(1,0,0)\). Это **не** (2.19) с \(w_C=1\) на \(\widehat{CF}\).

### 4.2. Деление на ноль, overflow, NaN

| Предохранитель | Реализация | Вердикт |
| --- | --- | --- |
| G-01 clip \(u\) | `energy.Clip`; NaN → lo | PASS |
| G-02 \(R_{\mathrm{alloc}}=0\) | `CPUUtilization` / `MemoryUtilization` → 1, нет `/0` | PASS |
| G-03 / G-18 | `PluginArgs.Validate` в `New` fail-closed | PASS |
| G-04 / (2.23) | `span <= ε` → \(S_C=1/2\); `ε<=0` → default \(10^{-6}\) | PASS |
| G-14 \(0/0\) в \(S_P\) | `deadline==0 && hatT==0` → 1 до деления | PASS |
| G-15 \(\Delta t\le 0\) | кусок интеграла пропускается | PASS |
| G-17 \(\widehat{CF}<0\) | `cf < 0` → 0 | PASS |
| G-21 | `SafePartial`; Score не паникует | PASS |
| Override \(w_C\) | `sumRP==0` без молчаливой перенормировки: Error или \(w_C=1\) | PASS |
| Интеграл | стековый буфер `maxBreakpoints`; NaN power → error; `ValidateCarbonIntensity` | PASS |
| int64 Score | clamp \(s^{\mathrm{fp}}\) до записи; `FrameworkScore` clamp после `Round` | PASS |
| `hatT * time.Second` | горизонт из annotation/class, не wall-clock overflow на стендовых \(\hat T\) | PASS |

`panic(` в `pkg/scheduler`, `pkg/energy`, корневом `pkg/carbon`, `pkg/runtime`, `cmd/scheduler` **отсутствует**.

### 4.3. Контракт CycleState

| Норма | Реализация | Вердикт |
| --- | --- | --- |
| Ключ | `carbon-aware.io/CarbonAware` (`API-STATE-01`) | PASS |
| Тип | `cycleStateData` wraps `v1alpha1.CarbonCycleState`; `api` не импортирует framework (`ARCH-14`) | PASS |
| Запись | один `Write` в конце PreScore (или G-08/opt-out) | PASS |
| Иммутабельность | payload не мутируется после `Write`; SM-STALE флаги выставляются **до** `Write` | PASS |
| `Clone` | identity (`return d`); карта `scorePerNode` только читается в Score | PASS |
| Parallel Score | `TestScoreParallelLookups` `-race` | PASS |
| Нет ключа / nil state | Success + 5000 (`FF-INV-08`) | PASS |
| Не хранить CycleState в плагине | поля плагина: args, handle, cache, client, clock | PASS |
| Один snapshot на Pod | `p.Snapshot()` один раз в PreScore (`ARCH-06`) | PASS |

`Score` не вызывает `Snapshot()`, интеграл, HTTP, не аллоцирует map.

## 5. Соответствие математической модели

### 5.1. Формулы

| Формула | Код | Вердикт |
| --- | --- | --- |
| (2.2) \(P(u)\) | `LinearPowerModel.PredictNodePower` | PASS |
| (2.3)/(2.3a) | `CPUUtilization`, `MemoryUtilization` | PASS |
| (2.4) IDLE-NONE | `PredictPodMarginal` в `evaluateOne` | PASS |
| (2.10s) \(\eta\sum P\Delta t\,CI\) | `IntegrateExpectedFootprint` | PASS |
| (2.11) persistence | `ciAt` → `currentCI` вне forecast | PASS |
| (2.12) \(\hat T\) | `pkg/runtime.ResolveHatTSeconds` | PASS |
| (2.13) hard SLA | `HardSLAViolated` → PreScore Error | PASS |
| (2.15) \(S_C\) | `CarbonScore`; G-09 не входит в min/max | PASS |
| (2.16)–(2.17) \(S_R\) | `ResourceScore` | PASS |
| (2.18) \(S_P\) | `PerformanceScore`; G-13 → 1 | PASS |
| (2.19)–(2.20) | `WeightedScore`; веса из args, не перенормировка в Score | PASS |
| (2.21) | `FrameworkScore` | PASS |
| (2.23) | `CurrentCIScore` | PASS |

Scoring CF — IDLE-NONE (\(P_{\mathrm{pod}}\)). Узловой \(CF_n\) (idle входит) в `Score` не подставляется.

### 5.2. Каталог G-01…G-21 (выборочная трассировка в runtime)

| ID | Где закрыт | Тест / путь |
| --- | --- | --- |
| G-01…G-05 | energy / CarbonScore | `pkg/energy`, 14.4, 14.5 |
| G-06 | пустой forecast → persistence | интеграл + PreScore |
| G-07/G-08 | empty/dead/all_invalid → 50 всем | T5.5 сц. 4b, 5 |
| G-09 | нет зоны / нет ключа snapshot; \(S_C=1/2\) | T5.5 сц. 5 |
| G-10 / FF-FUT | `IsUsableForScoring`, `TimestampFromFuture` | `prescore_test` |
| G-11 | \(\hat T\le 0\) → CF=0 | интеграл |
| G-12 / G-20 | PreScore Error, не Score=0 | T5.5 сц. 3 |
| G-13 / G-14 | `PerformanceScore` | unit carbon |
| G-15…G-17 | интеграл | T5.4 |
| G-18 | `Validate` при `New` | `plugin_test` |
| G-19 | clamp | `NormalizeScore` |
| G-21 | `SafePartial` | `coverage_edges_test` |

SM-STALE \(\neq\) G-08 (FF-INV-07): age=120 s сохраняет 84/24.

## 6. Производительность

Стенд аудита: Windows amd64, AMD Ryzen 5 5600H, Go 1.26.5, CGO+race toolchain. Синтетика: 20 NodeInfo, FakeClock, seeded snapshot, без HTTP.

| Измерение | Результат | Бюджет | Вердикт |
| --- | --- | --- | --- |
| `BenchmarkScorePerNode` | **94.10 ns/op**, 64 B/op, 1 alloc | 100 µs = 100 000 ns | PASS (\(\times\sim 1060\)) |
| `TestScoreLatencyBudget` | **95 ns/node** (50 000 lookup) | 100 µs | PASS |
| `BenchmarkNormalizeScore20` | 106.1 ns/op на список из 20 | \(O(N)\), не Score | PASS |
| `BenchmarkPreScore20` | 17.7 µs/op, 3408 B, 14 alloc | \(O(NK)\) вне Score | PASS (не в бюджете Score) |
| `GetZoneState` (T5.2) | avg ≪ 50 µs | hot-path cache | PASS |

Единственная аллокация Score — `fwk.NewStatus(Success)` (64 B). Это не I/O и не меняет асимптотику \(O(1)\).

Сложность SSOT §11: PreScore \(O(NK)\), Score \(O(1)\), NormalizeScore \(O(N)\) — подтверждена кодом и бенчмарком.

## 7. Блокировки, горутины, утечки

| Риск | Наблюдение | Вердикт |
| --- | --- | --- |
| HTTP в Score/PreScore | клиент только в Worker `syncTick` | PASS |
| `Mutex` на Score | `staleWarnMu` только `warnStale` (PreScore, SM-STALE) | PASS |
| Cache | `atomic.Pointer`; Get без lock | PASS |
| Worker | `NewTicker` + `defer Stop`; выход `ctx.Done()`; ctx **не** в struct (`GO-08`) | PASS |
| HTTP body | `LimitReader(1 MiB+1)` + `Body.Close()` | PASS |
| Keep-alive | `http.Client` процесса планировщика — штатный пул, не утечка цикла | PASS |
| Data race | `go test -race ./pkg/scheduler ./pkg/energy ./pkg/carbon ./pkg/runtime ./tests/unit` | PASS |
| Filter/Permit | compile-time только PreScore+Score+ScoreExtensions | PASS |

## 8. Тесты T5.5, линтер, сборка

| Проверка | Результат |
| --- | --- |
| `go test -race ./pkg/scheduler` | PASS, coverage **96.3%** |
| `go test -race ./tests/unit` | PASS (сценарии 1–5, Normalize 3/5/20, CDS replay) |
| `go test -race ./pkg/energy ./pkg/carbon ./pkg/runtime` | PASS |
| `go vet` тех же пакетов | 0 (после QG5-F01) |
| `gofmt -l` | clean |
| golangci-lint v2.4.0 `--enable=errcheck,govet,ineffassign,staticcheck,unused,misspell` на `./cmd/scheduler ./pkg/scheduler ./pkg/energy ./pkg/carbon ./pkg/runtime` | **0 issues** (после QG5-F02) |
| `go build -o bin/kube-scheduler ./cmd/scheduler` | PASS |

Репозиторный `.golangci.yml` — схема v1; на машине аудита линтер v2, поэтому набор воспроизведён как на Gate 4 (`--no-config`).

## 9. Замечания, закрытые на Gate (не смена модели)

| ID | Находка | Резолюция |
| --- | --- | --- |
| QG5-F01 | `go vet`: self-assignment `w.sR, w.sP` в ветке G-21 | убрано; нейтрализуются только небезопасные \(S_{\bullet}\) |
| QG5-F02 | `errcheck`: `resp.Body.Close()` | `defer func() { _ = resp.Body.Close() }()` |

Поведение скоринга не изменилось.

## 10. Follow-up, не блокирующие Stage 6

### QG5-FU-01. Образ и манифесты

Контейнер, kind patch, YAML профилей B/C — зона Stage 6 (`gate_3` QG3-FU-02). Бинарь собирается; Dockerfile в репозитории ещё нет — это вход интеграции, не дефект плагина.

### QG5-FU-02. Аллокация Status в Score

1× `NewStatus` на вызов. Бюджет 100 µs не угрожается. Интернирование Success — косметика.

### QG5-FU-03. Init containers / Pod Overhead в \(R_p\)

`podResourceRequests` суммирует `spec.containers`. Модель говорит «CPU request пода». Уточнение init/overhead — карточка эксперимента / API, не формула (2.4).

## 11. Формальное решение Gate

**QUALITY GATE 5: PASS.**

**Stage 5 завершён.** Исходный код `cmd/scheduler`, `pkg/scheduler`, `pkg/energy` (+ чистые `pkg/carbon`, `pkg/runtime`) соответствует LOCKED модели, политике Fallback и контракту Scheduler Framework 1.37. Плагин протестирован (включая `-race`) и готов к сборке в контейнер.

**КОМАНДА НА ЗАПУСК:** Stage 6 — интеграция с Kubernetes: образ `carbon-aware-scheduler`, Deployment, профили B/C, стык с CDS (`DOD-TEST-05`), E2E `schedulerName` (`DOD-TEST-06`). Формулы (2.1)–(2.23) и автомат Fallback **не открываются**. Агенты L2 не изменяют LOCKED SSOT (`SSOT-08`).
