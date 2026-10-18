# ADR-002: Системная архитектура двухпроцессного carbon-aware планировщика

- **Статус:** accepted
- **Дата:** 2026-09-04
- **Авторы:** Principal Distributed Systems Architect
- **Связанные документы:** [`docs/ssot/01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) v1.0.0, [`docs/ssot/02_math_model_ssot.md`](../ssot/02_math_model_ssot.md) v1.0.0 LOCKED, [`docs/ssot/governance.md`](../ssot/governance.md), [`docs/ssot/engineering-specification.md`](../ssot/engineering-specification.md) §§2–5, §48–50, [`docs/research/scheduler_framework_analysis.md`](../research/scheduler_framework_analysis.md), ADR-000, ADR-001
- **Supersedes:** —
- **Superseded by:** —
- **See also:** заморозка пакета Stage 3 — [`002_system_architecture_and_api_freeze.md`](002_system_architecture_and_api_freeze.md) (Quality Gate 3)

## Контекст

Quality Gate 2 заблокировал формулы (2.1)–(2.23). Stage 3 обязан отобразить их на процессы Kubernetes, не нарушив `SCOPE-OUT-17` (нет HTTP в `Score`) и требование пайплайна: связь Carbon Data Service ↔ Scheduler Plugin только через локальный in-memory кэш плагина.

Инженерная спецификация набрасывает четыре подсистемы и два профиля планировщика, но оставляет неоднозначности, которые реализация не должна разрешать ad hoc:

- ходить ли в CDS из `PreScore` «один раз на Pod» или вынести I/O полностью из scheduling cycle;
- заменять ли kind `kube-scheduler` одним бинарём с профилем `default-scheduler` или изолировать baseline отдельным процессом;
- класть ли провайдеры и replay в `pkg/carbon` рядом с чистыми формулами;
- как включить стратегию B без ловушки `multiPoint` + `disabled: '*'`.

Решение нельзя отложить: T3.2 (контракты) и T3.3 (метрики/манифесты) зависят от границ процессов и от того, кто является единственным HTTP-клиентом планировщика.

## Решение

Принимается документ [`docs/ssot/01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) версии **1.0.0**, статус **`accepted`**, как единственный нормативный источник компонентной модели. Формулы по-прежнему задаёт только ADR-001.

Фиксируется следующее.

1. **Два процесса runtime.** Carbon Data Service (`cmd/carbon-service`) готовит \(CI\). Custom kube-scheduler (`cmd/scheduler`) принимает решения. CDS не ранжирует узлы; планировщик не вызывает внешние Carbon API.

2. **Два in-memory кэша и асинхронный pull.** Кэш CDS и неизменяемый snapshot плагина разделены. Единственный HTTP-клиент в процессе планировщика — Cache Sync Worker (goroutine фабрики плагина). `PreScore` делает один `Load()` снимка на Pod; `Score` читает только `CycleState`. Синхронных сетевых вызовов в `PreScore`/`Score`/`NormalizeScore` нет.

3. **Точки расширения.** Только `PreScore`, `Score`, `NormalizeScore`. Углерод не является `Filter`. `Reserve` не вводится.

4. **Изоляция профилей.** Стратегия A обслуживается kind `kube-scheduler` (`schedulerName: default-scheduler`) **без** `CarbonAware`. Стратегии B и C — второй процесс custom-бинаря с профилями `carbon-only-scheduler` и `carbon-aware-scheduler`. Custom-бинарь не объявляет `default-scheduler`. Профиль B регистрирует плагин явно в `preScore`/`score` (не через `multiPoint` + `disabled: '*'`).

5. **Чистые пакеты отделены от I/O.** `pkg/energy`, `pkg/carbon`, `pkg/runtime` — без сети. Адаптеры — `pkg/provider`, replay — `pkg/replay`. Уточнение `CONF-05` относительно дерева §70 спецификации.

6. **Наблюдаемость pull-only.** Prometheus scrape; планировщик не запрашивает Prometheus в цикле планирования.

7. **Replay.** Модельные часы принадлежат Replay Engine в CDS. Perfect foresight в зачётном прогоне запрещён.

Файл [`architecture.md`](../ssot/architecture.md) становится указателем на `01_architecture_ssot.md`, чтобы старый путь `CONF-03` не оставался вторым каноном.

## Обоснование

Асинхронный Worker, а не «один HTTP в `PreScore`», выбран потому, что `PreScore` всё ещё стоит на критическом пути сериализованного scheduling cycle: таймаут CDS задержал бы **все** последующие Pod. Вынос I/O в ticker сохраняет `ARCH-01` даже при деградации сети и даёт один и тот же снимок \(CI\) на все узлы Pod (`REPRO-17`).

Второй процесс для baseline, а не профиль `default-scheduler` в custom-бинаре, выбран чтобы не конкурировать с kind scheduler за имя и лидерство и чтобы стратегия A оставалась неотличимой от штатного планировщика кластера — условие защиты сравнения A vs C.

Третий профиль для стратегии B следует из `SCOPE-IN-13` и из разбора `multiPoint` в T1.3: иначе B либо разделяет очередь с C, либо молча теряет Score-плагин.

Разделение `pkg/carbon` и `pkg/provider` удерживает `GO-13` и покрытие 85 % на формулах без HTTP-фикстур.

Отказ от Extender, Redis и брокера прямо предписан `SCOPE-OUT-*`; ADR лишь фиксирует положительную замену: REST + два in-memory снимка.

## Последствия

### Положительные

- Горячий путь планирования детерминирован и свободен от сетевой задержки.
- Отказ CDS старит snapshot и включает G-08, не ломая размещение.
- Эксперименты A/B/C изолируются полем `schedulerName` на одном кластере.
- T3.2 получает закрытый список границ: какие поля в REST, какие — в `CycleState`, чего в API быть не должно.
- Чистые функции модели тестируются без кластера и без CDS.

### Отрицательные / компромиссы

- Данные \(CI\) в плагине запаздывают на величину `refreshInterval` (десятки секунд при каноне 30 s) — приемлемо относительно `maxDataAgeSeconds=300` и гранулярности сетки.
- Два процесса scheduler на control-plane усложняют манифесты относительно «одного бинаря на все профили».
- Вес фреймворка \(w_{\mathrm{CA}}^{\mathrm{fw}}\) остаётся отдельным экспериментальным рычагом; его легко спутать с \(w_C\).
- Холодный старт CDS/Worker даёт окно нейтрального scoring до первого успешного sync.

### Влияние на SSOT и код

- Каталоги / пакеты: норма раскладки `cmd/scheduler`, `cmd/carbon-service`, `pkg/scheduler`, `pkg/energy`, `pkg/carbon`, `pkg/runtime`, `pkg/provider`, `pkg/replay`, `pkg/metrics`.
- CRD / API: REST CDS как единственный межпроцессный контракт; CRD не вводится.
- Эксперименты / метрики: три `schedulerName`; scrape CDS и обоих scheduler; детали имён — T3.3.

## Альтернативы

| Вариант | Почему отклонён |
| --- | --- |
| Scheduler Extender (HTTP webhook) | `SCOPE-OUT-08`; синхронный I/O на каждую попытку |
| HTTP к CDS из `PreScore` «один раз» | всё ещё блокирует scheduling cycle; нарушает формулировку задачи Stage 3 |
| Прямой вызов Electricity Maps из плагина | `SCOPE-OUT-17`; невоспроизводимо; нет единой валидации |
| Redis / Kafka между CDS и плагином | `SCOPE-OUT-06`, `SCOPE-OUT-10` |
| Углеродный `Filter` | ломает `SCOPE-IN-09` и сравнимость с baseline |
| Один процесс, профиль `default-scheduler` с выключенным плагином | конфликт с kind scheduler; риск случайно включить `CarbonAware` в A |
| Стратегия B как \(w_C=1\) на формуле (2.10) в том же профиле, что C | противоречит ADR-001 / (2.23); смешивает очереди |
| Провайдеры внутри `pkg/carbon` | I/O в пакете заблокированных формул; слабее `GO-13` |
| `Reserve`/`PostBind` для логирования решения | `SCOPE-OUT-16` для Reserve; Bind не нужен — join Binding × метрики PreScore |

## Проверка

- Ревью диаграмм §4, §9–§11: нет ребра `Score → CDS` и `PreScore → REST`.
- Будущий compile-time тест: плагин не реализует `FilterPlugin` / `PermitPlugin` / `QueueSortPlugin`.
- Integration (`DOD-TEST-05`): при остановленном CDS планирование продолжается с fallback.
- E2E (`DOD-TEST-06`): Pod с `schedulerName: carbon-aware-scheduler` попадает на ожидаемый узел при зафиксированном датасете.
- `go test -race` на snapshot Worker (`GO-10`).
- Синтаксис PlantUML в `01_architecture_ssot.md` проверяется парсером PlantUML (отчёт T3.1).
