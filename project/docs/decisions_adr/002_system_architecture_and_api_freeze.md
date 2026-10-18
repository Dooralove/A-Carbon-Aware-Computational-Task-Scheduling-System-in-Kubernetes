# ADR-002: Заморозка системной архитектуры и API-контрактов (SSOT Freeze)

- **Статус:** accepted
- **Дата:** 2026-09-04
- **Авторы:** Lead System Architect & Scientific Project Director
- **Связанные документы:** [`docs/ssot/01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) v1.1.0 LOCKED, [`docs/ssot/03_api_contracts_ssot.md`](../ssot/03_api_contracts_ssot.md) v1.0.1 LOCKED, [`docs/ssot/failure_and_fallback_policy.md`](../ssot/failure_and_fallback_policy.md) v1.1.0 LOCKED, [`docs/ssot/02_math_model_ssot.md`](../ssot/02_math_model_ssot.md) v1.0.0 LOCKED, [`docs/ssot/metrics.md`](../ssot/metrics.md) v1.0.0 LOCKED, [`docs/ssot/governance.md`](../ssot/governance.md) v1.4.0 (`SSOT-08`), [`api/v1alpha1/openapi.yaml`](../../api/v1alpha1/openapi.yaml), [`docs/research/scheduler_framework_analysis.md`](../research/scheduler_framework_analysis.md), [`docs/diploma_text/chapter_3_system_design.md`](../diploma_text/chapter_3_system_design.md), ADR-000, ADR-001, [`002_system_architecture.md`](002_system_architecture.md), Quality Gate 3
- **Supersedes:** —
- **Superseded by:** —
- **Расширяет:** [`002_system_architecture.md`](002_system_architecture.md) (компонентная модель T3.1). Настоящий ADR **не заменяет** семь решений T3.1; он **замораживает** их вместе с контрактами T3.2, автоматом T3.4 и именами метрик. Файл `002_*` группирует эпоху Stage 3 (как ADR-001 заморозил Stage 2).

## Контекст

Quality Gate 2 заблокировал формулы (2.1)–(2.23). Stage 3 (T3.1, T3.2, T3.4) отобразил их на два процесса, REST CDS, плагин Scheduler Framework и политику Fallback. Без формальной заморозки пакет остаётся `accepted`, но не `LOCKED`: следующий агент мог бы «уточнить» горячий путь, добавить углеродный `Filter`, синхронный HTTP в `PreScore` или endpoint «посчитать Score».

Инженерная спецификация и T1.3 оставляют ловушки, которые реализация не должна разрешать ad hoc:

- Scheduler Extender и HTTP из `Score` / `PreScore` (`SCOPE-OUT-08`, `SCOPE-OUT-17`);
- `multiPoint` + `disabled: '*'` для стратегии B (плагин исчезает из `Score`);
- смешение `w_C` и \(w_{\mathrm{CA}}^{\mathrm{fw}}\);
- G-08 как `Unschedulable` / `Skip` / нейтраль только \(S_C\);
- opt-in, появившийся в T3.2, не был явно вписан в автомат T3.4.

Решение нельзя отложить: Stage 4 (Carbon Data Service) и Stage 5 (Scheduler Plugin) стартуют параллельно и обязаны ссылаться на один замкнутый набор границ.

## Решение

1. **Утверждается пакет Stage 3 как LOCKED** (Quality Gate 3, `SSOT-08`):

   | Документ | Версия | Что заморожено |
   | --- | --- | --- |
   | `01_architecture_ssot.md` | 1.1.0 | `ARCH-01`…`ARCH-16`, два процесса, два кэша, точки расширения, профили A/B/C |
   | `03_api_contracts_ssot.md` | 1.0.1 | REST, DTO, `carbon-aware.io/*`, `pluginConfig.args`, `CycleState` |
   | `failure_and_fallback_policy.md` | 1.1.0 | матрица FF-*, автомат SM-*, `fwk.Status`, Anti-Thrashing AT-*, opt-out |
   | `metrics.md` | 1.0.0 | имена/типы/лейблы `carbon_aware_*` |
   | `02_math_model_ssot.md` | 1.0.0 | без изменений; формулы уже LOCKED ADR-001 |

2. **Компонентная модель T3.1 остаётся в силе** ([`002_system_architecture.md`](002_system_architecture.md)): CDS не ранжирует узлы; единственный HTTP-клиент планировщика — Cache Sync Worker; `PreScore`/`Score`/`NormalizeScore` без сети; углерод не `Filter`; baseline A — kind `kube-scheduler`.

3. **Запрет на правку без нового ADR.** Агенты L2/L3 не вправе изменять `ARCH-*`, `API-*`, `FF-*`, имена метрик и формулы. Расхождение кода с пакетом разрешается в пользу SSOT (`SSOT-04`).

4. **Операционализации Freeze (не новые формулы, `CONF-05` относительно порядка PreScore):**

   | Тема | До Freeze | Канон Freeze |
   | --- | --- | --- |
   | Opt-in | T3.2 `API-META-02`; T3.4 начинал с SLA | `ARCH-16` / `FF-INV-13`: сначала профиль, затем opt-out (Score=50, не G-08), затем SLA, затем `Load()` |
   | Health | ARCH `/health`; API ещё `/healthz` | оба пути, один обработчик |
   | Args YAML | без `staleWarnSeconds` / `clockSkewSeconds` | канон стенда 90 s / 60 s |
   | `CarbonProvider` vs «чистые интерфейсы» | API §5 смешивал ctx | I/O-интерфейс имеет `context`; `EnergyModel`/`CarbonCalculator` — без I/O |
   | Имена метрик | `metrics.md` TBD | таблица `carbon_aware_*`; G-08 ≠ stale ≠ opt-out |

5. **Параллельный старт Stage 4 и Stage 5 разрешён.** Граница: CDS реализует OpenAPI и кэш; плагин реализует Worker + горячий путь. Контракт стыка — REST + `CarbonSnapshot`. Манифесты `deployments/*` пишутся вместе с кодом, без Helm (`SCOPE-OUT-21`). Карточки экспериментов (`experiment-protocol.md`) — Stage 6, не блокер кода.

6. Указатели `architecture.md` и `interfaces.md` остаются стабильными путями `CONF-03` и **не** являются вторым каноном.

## Обоснование

Асинхронный Worker, а не HTTP в `PreScore`, выбран потому, что `PreScore` стоит на сериализованном scheduling cycle: таймаут CDS задержал бы все последующие Pod (T1.3, `SCOPE-OUT-17`). Два кэша дают один снимок \(CI\) на все узлы Pod (`ARCH-06`, `REPRO-17`) и позволяют CDS падать без `Unschedulable` (`ARCH-09`).

Заморозка API вместе с архитектурой необходима: иначе Stage 4 мог бы добавить Unix-время на проводе, а Stage 5 — RFC 3339 (`VER-06`). Opt-in вынесен из G-08, чтобы эксперимент отказа источника не смешивался с нагрузками, которые сознательно не участвуют в углеродном скоринге.

Имена метрик заморожены до первой реализации, иначе `DOD-CODE-13` заставил бы каждый сервис придумывать префикс и сломал бы сравнение Fallback rate.

Третий профиль для стратегии B и отказ от Extender — решения T3.1; Freeze только фиксирует, что они не подлежат «упрощению» при кодировании.

## Последствия

### Положительные

- Горячий путь планирования детерминирован и свободен от сетевой задержки.
- Stage 4 и Stage 5 могут идти параллельно по одному контракту.
- Отказ CDS старит snapshot и включает G-08, не ломая размещение.
- Opt-out наблюдаем отдельно от Fallback.
- Глава 3 диплома трассируется к заблокированному пакету без параллельной «авторской» архитектуры.

### Отрицательные / компромиссы

- Данные \(CI\) в плагине запаздывают на `refreshInterval` (канон 30 s) относительно CDS.
- Два процесса scheduler на control-plane усложняют манифесты.
- Холодный старт даёт окно нейтрального scoring до первого sync.
- Итоговый контракт Score — 101 целое в \([0,100]\); ничьи возможны.
- `experiment-protocol.md` ещё не детализирован: зачётные карточки — Stage 6.
- Toolchain Go 1.23 vs Kubernetes 1.37 (нужен Go 1.26) остаётся инфраструктурным ADR до `make build` бинаря планировщика (QG2-FU-03); на контракты не влияет.

### Влияние на SSOT и код

- **Каталоги / пакеты:** `cmd/carbon-service`, `cmd/scheduler`, `pkg/{scheduler,energy,carbon,runtime,provider,replay,metrics}` — норма `ARCH-14`.
- **CRD / API:** REST CDS; CRD не вводится; группа `carbon-aware.io/v1alpha1`.
- **Эксперименты / метрики:** три `schedulerName`; scrape CDS и обоих scheduler; имена — `metrics.md`.
- **Документы:** пакет LOCKED; `governance.md` v1.4.0; глава 3; Quality Gate 3.

## Альтернативы

| Вариант | Почему отклонён |
| --- | --- |
| Оставить архитектуру/API в статусе `accepted` без LOCKED | Код начал бы определять контракт; нарушает цель Gate 3 |
| Scheduler Extender | `SCOPE-OUT-08`; синхронный I/O на каждую попытку |
| HTTP к CDS из `PreScore` «один раз» | всё ещё блокирует scheduling cycle |
| Углеродный `Filter` | ломает `SCOPE-IN-09` и сравнимость A vs C |
| Слить CDS в процесс планировщика | сбой провайдера стал бы сбоем scheduler; replay смешается с циклом |
| Redis / Kafka между CDS и плагином | `SCOPE-OUT-06`, `SCOPE-OUT-10` |
| Стратегия B через `multiPoint` + `disabled: '*'` | T1.3 §7.5: плагин не попадает в `Score` |
| G-08 = `Skip` или \(S_C=0\) | не канон ADR-001; ломает baseline C |
| Opt-out считать G-08 | смешивает эксперимент отказа источника с выключенным скорингом |
| Отложить имена метрик до первого PR | нарушение `DOD-CODE-13`; расхождение префиксов у двух команд |
| Unix-секунды на REST | отвергнуто T3.2; смена — только ADR (`VER-06`) |

## Проверка

1. **Статус пакета.** Шапки `01` / `03` / Fallback / `metrics.md`: `accepted (LOCKED)`; баннер Quality Gate 3.
2. **Нет синхронного I/O.** Диаграммы `01_architecture_ssot.md` §4, §10, §11: нет ребра `PreScore → REST` и `Score → CDS`. Compile-time: плагин не реализует `FilterPlugin` / `PermitPlugin` / `QueueSortPlugin`.
3. **OpenAPI.** Нет path «score» / «bind» / «schedule». GET `/forecast` с пустым массивом — 200.
4. **Status.** `Score` на углеродном отказе — `Success`; SLA — только `PreScore` Error; opt-out — Success + 50, не `fallback_total`.
5. **Согласованность весов.** `carbonWeight` суммируется к 1; `plugins.score.weight` — целое \(w_{\mathrm{CA}}^{\mathrm{fw}}\), не путать.
6. **Процесс.** Diff в LOCKED-файлах после этой даты без нового ADR — нарушение, подлежит откату (`SSOT-01`, `SSOT-08`).
7. **Глава 3.** Компонентная модель и потоки совпадают с SSOT; формулы не переопределяются.
8. **Интеграция (будущее).** `DOD-TEST-05/07`: CDS down → планирование живо; `go test -race` на snapshot Worker (`GO-10`).
