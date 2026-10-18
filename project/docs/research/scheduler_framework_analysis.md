# Анализ Kubernetes Scheduler Framework для плагина CarbonAware

- **Версия:** 1.1.0
- **Статус:** draft (исследовательская заметка, не SSOT; согласован с Gate 1)
- **Дата:** 2026-09-04
- **Автор:** Senior Kubernetes Core Researcher (уровень L2)
- **Задача:** T1.3 — «Исследовать Scheduler Framework» (исследовательская задача №3, `engineering-specification.md` §1.5)
- **Целевая версия API:** Kubernetes 1.37.0 (`kubescheduler.config.k8s.io/v1`)

Документ носит исследовательский характер. Он **не является** источником правды: канонические
формулировки после ревью переносит уровень L1 в [`docs/ssot/architecture.md`](../ssot/architecture.md)
и [`docs/ssot/interfaces.md`](../ssot/interfaces.md). Все утверждения о поведении Kubernetes
подтверждены чтением исходного кода ветки `release-1.37` (ссылки — в §12); утверждения о поведении
будущего плагина `CarbonAware` помечены как **проектное предложение** (DOD-DOC-09).

## Прочитанные документы (SSOT-05)

[`docs/ssot/governance.md`](../ssot/governance.md), [`docs/ssot/engineering-specification.md`](../ssot/engineering-specification.md)
(§3.3, §4, §24–26, §48–51, §Итоговая архитектура), [`docs/ssot/README.md`](../ssot/README.md),
[`docs/ssot/architecture.md`](../ssot/architecture.md), [`docs/decisions_adr/000_project_scope_and_governance.md`](../decisions_adr/000_project_scope_and_governance.md),
[`docs/audit/T0.2-governance-and-adr-000.md`](../audit/T0.2-governance-and-adr-000.md),
[`carbon_intensity_datasets.md`](carbon_intensity_datasets.md),
[`power_models_state_of_the_art.md`](power_models_state_of_the_art.md), `go.mod`, `README.md`.

Нормы Scope, определяющие содержание документа: `SCOPE-IN-01`, `SCOPE-IN-07`, `SCOPE-IN-08`,
`SCOPE-IN-09`, `SCOPE-OUT-07`, `SCOPE-OUT-08`, `SCOPE-OUT-16`, `SCOPE-OUT-17`, `SCOPE-EDGE-02`,
`SCOPE-EDGE-04`, `DOD-CODE-12`, `GO-06`, `GO-10`, `GO-13`, `GO-14`.

---

## 1. Целевая версия API и её последствия

### 1.1. Что зафиксировано в спецификации

`engineering-specification.md` §51 и §«Итоговая архитектура» фиксируют **Kubernetes 1.37.x** с
двумя профилями (`default-scheduler`, `carbon-aware-scheduler`) и оставляют выбор patch-версии на
этап реализации. Это существенно: между 1.29 и 1.37 контракт Scheduler Framework менялся
четыре раза, и подавляющее большинство доступных в сети примеров плагинов (включая
`kubernetes-sigs/scheduler-plugins` версий до 1.33) **не компилируются** против 1.37.

### 1.2. Где физически живут типы

С 1.34–1.35 типы Scheduler Framework вынесены в staging-репозиторий `k8s.io/kube-scheduler`,
чтобы out-of-tree плагины не тянули весь `k8s.io/kubernetes`. Итоговое расположение для 1.37:

| Что | Пакет | Импортируется как |
| --- | --- | --- |
| `Plugin`, `PreScorePlugin`, `ScorePlugin`, `ScoreExtensions`, `FilterPlugin`, `ReservePlugin`, `PermitPlugin`, `QueueSortPlugin`, `Handle`, `SharedLister`, `NodeInfoLister`, `NodeScoreList`, `NodeScore` | `k8s.io/kube-scheduler/framework` | `fwk` |
| `CycleState`, `StateData`, `StateKey`, `ErrNotFound`, `NodeInfo`, `Status`, `Code`, `MinScore`, `MaxScore`, `MaxTotalScore` | `k8s.io/kube-scheduler/framework` | `fwk` |
| `Registry`, `PluginFactory`, `DecodeInto` | `k8s.io/kubernetes/pkg/scheduler/framework/runtime` | `runtime` |
| Конкретные реализации `CycleState`, `NodeInfo`, `Framework` (внутренние) | `k8s.io/kubernetes/pkg/scheduler/framework` | — |
| `NewSchedulerCommand`, `WithPlugin`, `Option` | `k8s.io/kubernetes/cmd/kube-scheduler/app` | `app` |

Ловушка именования: **оба** пакета называются `framework`. Плагин, импортирующий и staging-типы,
и `runtime`, обязан алиасить как минимум один из них. Соглашение самого Kubernetes — `fwk` для
staging.

### 1.3. Матрица изменений сигнатур

| Релиз | `PreScore` (узлы) | `Score` (узел) | Тип состояния |
| --- | --- | --- | --- |
| 1.28–1.29 | `nodes []*v1.Node` | `nodeName string` | `*framework.CycleState` (структура) |
| 1.30–1.32 | `nodes []*framework.NodeInfo` | `nodeName string` | `*framework.CycleState` |
| 1.33 | `nodes []*framework.NodeInfo` | `nodeInfo *framework.NodeInfo` | `*framework.CycleState` |
| 1.34–1.37 | `nodes []fwk.NodeInfo` | `nodeInfo fwk.NodeInfo` | `fwk.CycleState` (**интерфейс**) |

Ключевое следствие для реализации: в 1.34 `NodeInfo` и `CycleState` **стали интерфейсами**. Прямое
чтение полей (`nodeInfo.Requested`, `nodeInfo.Pods`) больше невозможно — только геттеры
(`GetRequested()`, `GetPods()`).

### 1.4. Согласование версий Kubernetes и Go

Kubernetes 1.37 собран Go 1.26.5, а директива `go` в модулях `k8s.io/*` версии 1.37 — `1.26.0`.
Текущий [`go.mod`](../../go.mod) проекта объявляет `go 1.23`, и `governance.md` §3.1 пока нормирует
Go 1.23. Это расхождение конфигурации toolchain, а не ограничение Scheduler Framework.

Рабочий путь реализации однозначен: зафиксировать patch-версию Kubernetes 1.37, использовать
требуемую ею версию Go 1.26.x и синхронно обновить `go.mod` и норму governance по процедуре L1.
После такого согласования плагин штатно компилируется в бинарь `kube-scheduler`
(`SCOPE-IN-01`, `SCOPE-OUT-07`, `SCOPE-OUT-08`). Запрос на синхронизацию версий оформлен в отчёте
[`T1.3-scheduler-framework-analysis.md`](../audit/T1.3-scheduler-framework-analysis.md), раздел
`SSOT Change Request`; математический этап от этой инструментальной правки не зависит.

---

## 2. Scheduling Cycle и Binding Cycle

### 2.1. Разделение

Планирование одного Pod — это **одна попытка (scheduling attempt)**, состоящая из двух фаз с
принципиально разной моделью исполнения:

| Свойство | Scheduling Cycle | Binding Cycle |
| --- | --- | --- |
| Поток исполнения | главный поток планировщика, **строго последовательно**, один Pod за раз | отдельная goroutine на каждый Pod, **конкурентно** |
| Точки расширения | `PreFilter`, `Filter`, `PostFilter`, `PreScore`, `Score`, `NormalizeScore`, `Reserve`, `Permit` | `PreBindPreFlight`, `WaitOnPermit`, `PreBind`, `Bind`, `PostBind` |
| Снимок узлов (`SnapshotSharedLister`) | валиден и неизменен | **невалиден**, использовать запрещено |
| Влияние задержки | прямо снижает пропускную способность планировщика по всему кластеру | влияет только на этот Pod |
| Результат неудачи | Pod не размещён, уходит в backoff / unschedulable | `Unreserve` + возврат в backoffQ |

Границу видно в исходном коде — `scheduleOnePod` выполняет цикл планирования синхронно, а цикл
привязки запускает через `go`:

```go
state := framework.NewCycleState()
state.SetRecordPluginMetrics(rand.Intn(100) < pluginMetricsSamplePercent)
// ...
scheduleResult, assumedPodInfo, status := sched.schedulingCycle(schedulingCycleCtx, state, ...)
if !status.IsSuccess() { sched.FailureHandler(...); return }

// bind the pod to its host asynchronously (we can do this b/c of the assumption step above).
go sched.runBindingCycle(ctx, state, schedFramework, scheduleResult, assumedPodInfo, start, podsToActivate)
```

Между фазами стоит **assume**: планировщик оптимистично записывает Pod в свой кэш как размещённый
на выбранном узле, поэтому следующий Pod уже видит узел «занятым» и главный поток не ждёт ответа
apiserver.

### 2.2. Полный порядок точек расширения (1.37)

```text
[вне цикла]        PreEnqueue        — допуск Pod в очередь (scheduling gates)
[вне цикла]        QueueSort         — порядок в очереди; вызывается конкурентно для пар Pod

── Scheduling Cycle (последовательно, главный поток) ─────────────────────
                   UpdateSnapshot    — обновление локального снимка узлов (не точка расширения)
  1.  PreFilter    — подготовка к фильтрации, может сузить множество узлов
  2.  Filter       — допустимость узла; параллельно по узлам
  3.  PostFilter   — вызывается, только если допустимых узлов нет (вытеснение)
  4.  PreScore     — подготовка к оценке; ОДИН вызов на Pod, все допустимые узлы разом
  5.  Score        — оценка узла; параллельно по узлам
  6.  NormalizeScore — приведение оценок плагина к [0,100]; один вызов на плагин
      (взвешивание и суммирование — код фреймворка, не точка расширения)
  7.  Reserve      — резервирование ресурсов в состоянии плагина
                   assume — Pod записывается в кэш планировщика
  8.  Permit       — разрешить / отклонить / отложить (Wait)

── Binding Cycle (goroutine, конкурентно) ────────────────────────────────
  9.  PreBindPreFlight — лёгкая проверка, нужен ли PreBind вообще
 10.  WaitOnPermit     — здесь фактически ожидается таймаут Wait из Permit
 11.  PreBind          — подготовка (например, привязка томов)
 12.  Bind             — вызов Binding API
 13.  PostBind         — уведомление об успехе

      Unreserve        — откат; вызывается из любого потока при отказе после Reserve
```

Три неочевидных момента, важных для проектирования:

1. **`Permit` не блокирует главный поток.** `RunPermitPlugins` в конце цикла планирования лишь
   регистрирует Pod как ожидающий и возвращает управление; собственно ожидание (`WaitOnPermit`)
   происходит уже в goroutine цикла привязки. Именно поэтому `Wait` тормозит один Pod, а не весь
   планировщик.
2. **Unreserve может вызываться из двух разных потоков** — из главного (если отказ произошёл в
   `Permit`) и из goroutine привязки (если отказ в `PreBind`/`Bind`). Реализация обязана быть
   идемпотентной и не может возвращать ошибку.
3. **Начиная с `PreBind` Pod уже не может стать «unschedulable»** — отказ на этом этапе трактуется
   как ошибка и отправляет Pod в backoffQ.

### 2.3. Модель конкурентности, которую обязан учитывать плагин

Экземпляр плагина создаётся **один раз на профиль** и переиспользуется для всех Pod. Отсюда три
уровня конкурентности:

| Уровень | Что происходит | Что из этого следует |
| --- | --- | --- |
| Внутри одной фазы | `Filter` и `Score` вызываются параллельно для разных узлов **одного и того же Pod**, с одним и тем же `CycleState` | чтения из `CycleState` и полей плагина в `Score` должны быть безопасны; запись в общие структуры — запрещена |
| Между фазами одного Pod | точки расширения одного цикла выполняются строго последовательно | `PreScore` гарантированно завершён до первого `Score`; happens-before обеспечен |
| Между разными Pod | цикл привязки Pod A выполняется одновременно с циклом планирования Pod B | поля самого плагина (кэш, счётчики) требуют `sync.RWMutex` / `atomic` (`GO-10`) |

Параллелизм по узлам обеспечивает `parallelize.Parallelizer`:

```go
const DefaultParallelism int = 16

// returns max(1, min(sqrt(n), n/Parallelism))
func chunkSizeFor(n, parallelism int) int { /* ... */ }
```

То есть до 16 goroutine по умолчанию (настраивается полем `parallelism` в
`KubeSchedulerConfiguration`). Внутри одной goroutine (одного узла) плагины вызываются
последовательно циклом `runRawScorePlugins`.

---

## 3. Разбор точек расширения

### 3.1. `Filter`

```go
type FilterPlugin interface {
	Plugin
	Filter(ctx context.Context, state CycleState, pod *v1.Pod, nodeInfo NodeInfo) *Status
}
```

Жёсткий отсев: узел либо допустим, либо нет. Возврат `Unschedulable` означает «попробуйте вытеснение»,
`UnschedulableAndUnresolvable` — «даже вытеснение не поможет», `Error` — внутренняя ошибка, Pod
уходит в backoff. Вызывается **параллельно по узлам**, до `numFeasibleNodesToFind` узлов (при
больших кластерах фреймворк сознательно не проверяет все узлы). Плагин обязан читать переданный
`nodeInfo`, а не искать узел в снимке: при моделировании вытеснения фреймворк передаёт копию
`NodeInfo` с удалёнными Pod-жертвами.

Для `CarbonAware` — **запрещено** (`SCOPE-OUT-16`). Обоснование в §4.

### 3.2. `PreScore`

```go
type PreScorePlugin interface {
	Plugin
	// When it returns Skip status, other fields in status are just ignored,
	// and coupled Score plugin will be skipped in this scheduling cycle.
	PreScore(ctx context.Context, state CycleState, pod *v1.Pod, nodes []NodeInfo) *Status
}
```

Единственная точка расширения фазы оценки, которая:

- вызывается **ровно один раз на попытку планирования**, а не по одному разу на узел;
- получает **сразу весь список допустимых узлов** `[]fwk.NodeInfo`;
- выполняется последовательно, вне параллельной развёртки.

Это делает `PreScore` естественным местом для любых вычислений сложности \(O(N_{nodes})\) и выше, для
единственного за цикл чтения внешнего состояния и для расчёта величин, требующих знания всех узлов
одновременно (минимум и максимум для нормализации).

Возврат статуса `Skip` — штатный механизм отключения плагина на один цикл: фреймворк заносит имя
плагина в `state.SetSkipScorePlugins(...)`, и `Score` для него уже не вызывается. Любой **не**-`Success`
и **не**-`Skip` статус приводит к отклонению Pod («All prescore plugins must return success or the
pod will be rejected»).

### 3.3. `Score` и `NormalizeScore`

```go
type ScorePlugin interface {
	Plugin
	// Score is called on each filtered node. It must return success and an integer
	// indicating the rank of the node. All scoring plugins must return success or
	// the pod will be rejected.
	Score(ctx context.Context, state CycleState, p *v1.Pod, nodeInfo NodeInfo) (int64, *Status)

	// ScoreExtensions returns a ScoreExtensions interface if it implements one, or nil if does not.
	ScoreExtensions() ScoreExtensions
}

type ScoreExtensions interface {
	// NormalizeScore is called for all node scores produced by the same plugin's "Score"
	// method. A successful run of NormalizeScore will update the scores list and return
	// a success status.
	NormalizeScore(ctx context.Context, state CycleState, p *v1.Pod, scores NodeScoreList) *Status
}
```

Асимметрия вызовов, определяющая всю архитектуру плагина:

| Метод | Число вызовов за цикл | Конкурентность | Что видит |
| --- | --- | --- | --- |
| `PreScore` | 1 | нет | все узлы |
| `Score` | \(N_{nodes}\) | до 16 goroutine, разные узлы | один узел |
| `NormalizeScore` | 1 | параллельно с `NormalizeScore` **других** плагинов | только `NodeScoreList` — пары «имя узла → int64» |

Важное ограничение `NormalizeScore`: в него передаётся исключительно список оценок. Ни `NodeInfo`,
ни исходные физические величины там недоступны — только то, что плагин сам положил в `CycleState`.
Это ограничивает выбор схемы нормализации (§7.2).

`ScoreExtensions()` возвращает `nil`, если нормализация не нужна; фреймворк тогда просто пропускает
шаг. Возврат `nil` из метода-заглушки — типовая ошибка, приводящая к «оценки не нормализуются, но
никто не жалуется»: диапазон проверяется **после** `NormalizeScore`, и если `Score` уже вернул
значения в \([0,100]\), отсутствие нормализации не будет замечено вплоть до момента, когда
распределение окажется вырожденным.

### 3.4. `Reserve` / `Unreserve`

```go
type ReservePlugin interface {
	Plugin
	Reserve(ctx context.Context, state CycleState, p *v1.Pod, nodeName string) *Status
	Unreserve(ctx context.Context, state CycleState, p *v1.Pod, nodeName string)
}
```

Точка для учёта ресурсов, о которых Kubernetes не знает (в нашем случае — «зарезервированный
углеродный бюджет» узла или зоны). Контракт жёсткий: `Unreserve` идемпотентен, не возвращает
ошибку, вызывается в порядке, обратном `Reserve`, и может быть вызван даже если соответствующий
`Reserve` не выполнялся.

Для `CarbonAware` — вне MVP: `governance.md` `SCOPE-OUT-16` допускает `Reserve` только отдельным
ADR. Содержательно он понадобился бы лишь при переходе к учёту накопленного эффекта нескольких
одновременно планируемых Pod (эффект «стада»: все Pod пачки уходят в самую чистую зону, потому что
каждый оценивается независимо). Это зафиксировано как открытый вопрос §11.4.

### 3.5. `Permit`

```go
type PermitPlugin interface {
	Plugin
	Permit(ctx context.Context, state CycleState, p *v1.Pod, nodeName string) (*Status, time.Duration)
}
```

Возврат `(Wait, d)` откладывает привязку Pod на срок до `d`. Формально это единственный
предусмотренный фреймворком способ реализовать «temporal shifting» — откладывание задачи до
снижения углеродной интенсивности.

Для `CarbonAware` — **запрещено** (`SCOPE-OUT-16`). Обоснование и последствия в §4.4.

### 3.6. `QueueSort`

```go
type QueueSortPlugin interface {
	Plugin
	Less(QueuedEntityInfo, QueuedEntityInfo) bool
}
```

Определяет порядок извлечения Pod из очереди. Плагин по умолчанию — `PrioritySort` (по
`.spec.priority`, при равенстве — по времени постановки в очередь).

Для `CarbonAware` — **запрещено** (`SCOPE-OUT-16`). Обоснование в §4.3.

---

## 4. Обоснование выбора точек расширения для CarbonAware

Итоговый набор: **`PreScore` + `Score` + `NormalizeScore`**, что совпадает с `SCOPE-IN-01` и
§3.3 инженерной спецификации. Ниже — почему именно так, с опорой на контракты из §3.

### 4.1. Почему фаза Score, а не фаза Filter

Углеродная интенсивность — **критерий предпочтения, а не критерий допустимости**. Узел в «грязной»
зоне остаётся полностью работоспособным: Pod на нём запустится, отработает и уложится в SLA. Отсев
таких узлов на `Filter` означал бы, что при высокой интенсивности во всех зонах кластер перестаёт
планировать задачи вовсе — прямое нарушение `SCOPE-IN-09` (отказ источника или неблагоприятные
данные не должны ломать планирование).

Формально: `Filter` отвечает на вопрос «может ли Pod здесь работать», `Score` — «насколько это
хорошая идея». Углерод относится ко второму вопросу. Kubernetes закрепляет это разделение явно:
Filter определяет допустимые узлы, Score ранжирует оставшиеся.

Дополнительный аргумент — совместимость с baseline. Стратегия A (`default-scheduler`) и стратегия C
(предлагаемая) должны различаться **только алгоритмом ранжирования**, иначе сравнение в §50
спецификации теряет валидность: разное множество допустимых узлов означает разные условия
эксперимента, а не разные алгоритмы.

### 4.2. Почему PreScore обязателен, а не «сделаем всё в Score»

Три независимых довода, каждый из которых достаточен.

**Довод 1 — запрет I/O в горячем пути.** `SCOPE-OUT-17` и `DOD-CODE-12` запрещают сетевые вызовы
внутри `Score`. Данные об углеродной интенсивности всё же нужно откуда-то взять. `PreScore` —
единственная точка фазы оценки, вызываемая один раз за цикл, и именно там уместно **однократно
снять консистентный снимок** уже загруженного в память состояния.

**Довод 2 — арифметика вызовов.** `Score` вызывается \(N_{nodes}\) раз на каждый Pod. При кластере
стенда (`SCOPE-IN-11`: ≥ 4 worker) это немного, но эксперимент по масштабированию (`SCOPE-IN-14`)
предполагает рост числа узлов. Расчёт углеродного следа по формуле `SCOPE-IN-05`. Канон единиц и семантики \(CI(t)\) —
[`carbon_intensity_datasets.md`](carbon_intensity_datasets.md); канон \(P(u)\) и атрибуции —
[`power_models_state_of_the_art.md`](power_models_state_of_the_art.md).

$$
E_i=\frac{P_i\,\Delta t_i}{3.6\cdot10^6},\qquad
CF = \sum_i E_i\,CI_i
$$

где \(CF\) — углеродный след (gCO2e), \(E_i\) — энергия на интервале \(i\) (kWh),
\(P_i\) — мощность (W), \(\Delta t_i\) — длительность интервала (s), \(CI_i\) — углеродная
интенсивность (gCO2e/kWh). Делитель \(3.6\cdot10^6\) переводит W·s в kWh. Если \(\Delta t_i\)
задана в часах, эквивалентен делитель \(1000\). Для scoring **нового** Pod величина \(P_i\) есть
маржинальное приращение \(\Delta\widehat P_{p,n}=P(u_n^+)-P(u_n^-)\), а не полная мощность узла
с \(P_{\mathrm{idle}}\) (политика IDLE-NONE). Полная \(P_n(u)\) используется только в cluster KPI
после прогона. Горизонт суммы — expected runtime класса нагрузки (`SCOPE-IN-06`); интервалы
образуются пересечением \([t_{\mathrm{now}},\,t_{\mathrm{now}}+\widehat T)\) с piecewise-constant
сеткой \(CI(t)\). Вычисление представляет собой цикл по интервалам прогноза. Разбор прогноза,
поиск зоны в карте зон, проверка свежести данных и подготовка параметров энергомодели не зависят
от узла и не должны исполняться \(N_{nodes}\) раз.
Верхняя граница, на которую ориентируется сам Kubernetes: гистограмма
`plugin_execution_duration_seconds` имеет последнюю корзину «\(\gtrsim 22\) ms», а на попытку
планирования целиком стоит `trace.LogIfLong(100 * time.Millisecond)`.

**Довод 3 — нормализация требует знания всех узлов.** Min-max нормализация углеродного следа

$$
S_C(n) = 100 \cdot \frac{CF_{max} - CF(n)}{CF_{max} - CF_{min}}
$$

где \(S_C(n)\) — углеродная оценка узла \(n\) (безразмерная, \([0,100]\)), \(CF_{max}\) и \(CF_{min}\) —
максимальный и минимальный след по множеству допустимых узлов (gCO2e), — принципиально
не вычислима внутри `Score`, который видит ровно один узел. `PreScore` видит весь срез
`[]fwk.NodeInfo` и вычисляет \(CF_{min}\), \(CF_{max}\) до начала параллельной развёртки.

### 4.3. Почему не QueueSort

Четыре причины, в порядке убывания веса.

1. **Ломает базовые гарантии планировщика.** `QueueSort` управляет порядком обслуживания. Сортировка
   по углеродному критерию означает, что Pod «грязной» задачи может неограниченно долго
   пропускаться вперёд другими Pod — классический starvation. `PrioritySort` по умолчанию
   гарантирует FIFO внутри одного приоритета; своя сортировка эту гарантию снимает.
2. **Технически конфликтует с двухпрофильной схемой эксперимента.** Валидация конфигурации
   (`validateCommonQueueSort`) требует, чтобы плагин `queueSort` **и его аргументы были одинаковы во
   всех профилях**: у планировщика одна общая очередь Pod. Включив свой `QueueSort` в профиле
   `carbon-aware-scheduler`, мы обязаны включить его и в `default-scheduler` — и тем самым
   уничтожить baseline A. Разница между стратегиями перестала бы быть «только в алгоритме
   ранжирования».
3. **Не даёт содержательного эффекта в предмете работы.** Предмет — *выбор узла* (`governance.md`
   §1.1). Порядок обслуживания влияет на *когда* планируется Pod, а не *куда*. Эффект от
   переупорядочивания неотличим от эффекта откладывания, а откладывание — это `SCOPE-OUT-03`/§3.6.
4. **Формальный запрет.** `SCOPE-OUT-16`, подкреплённый предупреждением самого Kubernetes о том,
   что `queueSort`, `filter` и `permit` существенно влияют на работу планировщика.

Дополнительно: `QueueSort` — единственная точка расширения, вызываемая **вне контекста планирования**
и конкурентно для произвольных пар Pod. `CycleState` там недоступен, а значит недоступны и любые
подготовленные углеродные расчёты — пришлось бы читать разделяемый кэш под блокировкой на каждом
сравнении в куче, то есть \(O(\log n)\) раз на каждую операцию с очередью.

### 4.4. Почему не Permit (и что мы этим теряем)

`Permit` с `Wait` — единственный штатный способ отложить Pod до «зелёного окна». Отказ от него
обусловлен `SCOPE-OUT-16` и означает **осознанное сужение предмета до spatial shifting**
(«куда разместить») с исключением temporal shifting («когда запустить»). Это ограничение
работы, а не недосмотр, и в тексте диплома оно должно быть заявлено явно.

Технические риски `Permit`, подтверждающие разумность отказа: ожидающий Pod уже прошёл `assume`,
то есть **его ресурсы уже вычтены из кэша планировщика** на весь срок ожидания. Отложенный на
30 минут Pod все 30 минут держит ресурсы узла занятыми с точки зрения планировщика, ничего не
выполняя. Корректная реализация temporal shifting требует не `Permit`, а работы с
`schedulingGates` и отдельного контроллера — что уводит в `SCOPE-OUT-13`.

### 4.5. Сводная таблица решений

| Точка | Решение | Основание |
| --- | --- | --- |
| `QueueSort` | не реализуем | `SCOPE-OUT-16`; starvation; общая очередь на все профили; вне предмета |
| `PreFilter` | не реализуем | нечего сужать: углерод не влияет на допустимость |
| `Filter` | не реализуем | `SCOPE-OUT-16`; углерод — критерий предпочтения; сохранение валидности baseline |
| `PostFilter` | не реализуем | вытеснение вне предмета (`SCOPE-OUT-14`) |
| **`PreScore`** | **реализуем** | единственный однократный вызов в фазе оценки; снятие снимка данных; расчёт \(CF_{min}, CF_{max}\) |
| **`Score`** | **реализуем** | собственно ранжирование узлов; чистая функция от подготовленного состояния |
| **`NormalizeScore`** | **реализуем** | гарантия диапазона \([0,100]\); защита от вырожденных распределений |
| `Reserve` | вне MVP | `SCOPE-OUT-16` (только по ADR); нужен для учёта эффекта «стада» |
| `Permit` | не реализуем | `SCOPE-OUT-16`; удержание ресурсов на время ожидания; temporal shifting вне Scope |
| `PreBind`/`Bind`/`PostBind` | не реализуем | привязка не зависит от углерода |

---

## 5. `CycleState`: контракт, потокобезопасность, жизненный цикл

### 5.1. Тип

```go
// CycleState provides a mechanism for plugins to store and retrieve arbitrary data.
// StateData stored by one plugin can be read, altered, or deleted by another plugin.
// CycleState does not provide any data protection, as all plugins are assumed to be
// trusted.
type CycleState interface {
	Read(key StateKey) (StateData, error)
	Write(key StateKey, val StateData)
	Delete(key StateKey)
	Clone() CycleState
	// ... служебные методы фреймворка: ShouldRecordPluginMetrics, Get/SetSkip*Plugins и др.
}

type StateData interface {
	Clone() StateData
}

type StateKey string

var ErrNotFound = errors.New("not found")
```

Значимые детали контракта:

- `Read` возвращает `fwk.ErrNotFound`, если ключа нет. Ошибку **обязательно** обрабатывать: её
  наличие — штатная ситуация (например, `PreScore` вернул `Skip`), а не сбой.
- `StateData` — интерфейс с единственным методом `Clone()`. Любая структура плагина, кладущаяся в
  `CycleState`, обязана его реализовать.
- Ключи — глобальное пространство имён на весь `CycleState`; коллизия между плагинами возможна.
  Соглашение: ключ содержит имя плагина.
- Никакой изоляции между плагинами нет: «StateData stored by one plugin can be read, altered, or
  deleted by another plugin».

### 5.2. Потокобезопасность: что гарантировано, а что нет

Конкретная реализация в `pkg/scheduler/framework/cycle_state.go` хранит данные в `sync.Map`, о чём
сказано в комментарии к типу:

> Note: CycleState uses a sync.Map to back the storage, because it is thread safe. It's aimed to
> optimize for the "write once and read many times" scenarios. It is the recommended pattern used in
> all in-tree plugins — plugin-specific state is written once in PreFilter/PreScore and afterward read
> many times in Filter/Score.

Отсюда точная граница гарантий:

| Что | Безопасно? | Почему |
| --- | --- | --- |
| Одновременные `Read`/`Write`/`Delete` из разных goroutine | **да** | контейнер — `sync.Map` |
| Чтение полей значения `StateData`, полученного из `Read`, из нескольких `Score` одновременно | **да, если значение не мутируется** | `sync.Map` защищает отображение «ключ → значение», но не содержимое значения |
| Мутация среза/карты внутри `StateData` из `Score` | **нет** | `Score` исполняется до 16 goroutine одновременно, `sync.Map` от этого не защищает; гонка обнаруживается `go test -race` (`GO-10`) |
| Накопление результатов в общий буфер внутри `StateData` из `Score` | **нет** | тот же случай |

Важная методическая оговорка: в исходном коде Kubernetes **нет** комментария, прямо утверждающего,
что содержимое `StateData` не потокобезопасно. Единственная явная формулировка — процитированная выше
про `sync.Map` и паттерн «write once, read many», плюс «does not provide any data protection».
Требование неизменяемости выводится из модели исполнения `RunScorePlugins` (параллельная развёртка
по узлам с общим `state`), а не из документированного контракта. В тексте диплома это следует
формулировать именно так — как следствие модели исполнения.

**Проектное предложение.** Структура, публикуемая в `CycleState`, объявляется неизменяемой по
соглашению: заполняется целиком в `PreScore`, после `Write` не мутируется никем, все поля читаются
только на чтение, `Clone()` возвращает поверхностную копию (упомянутая в контракте `StateData`
оптимизация: «clone should make shallow copies for members that are not impacted by PreFilter's
optional AddPod/RemovePod methods» — наши данные `AddPod`/`RemovePod` не затрагивают).

### 5.3. Жизненный цикл

| Событие | Что происходит с `CycleState` |
| --- | --- |
| Начало попытки планирования Pod | `framework.NewCycleState()` — **новый пустой** объект |
| Переход `PreScore` → `Score` | тот же объект; happens-before гарантирован последовательностью фаз |
| Переход в цикл привязки | **тот же объект** передаётся в goroutine `runBindingCycle` |
| Отказ и повторная попытка того же Pod | **новый** `CycleState`; ничего не переносится |
| Моделирование вытеснения | используется `Clone()` — копия, полученная через `StateData.Clone()` каждого значения |

Формулировка KEP-624 категорична: данные, доступные через `CycleState`, невалидны после завершения
контекста планирования, и плагины не должны хранить на них ссылки.

Практические следствия для `CarbonAware`:

1. **Кэш углеродных данных нельзя держать в `CycleState`** — он живёт один цикл. Кэш живёт в полях
   плагина; в `CycleState` кладётся только производный срез под конкретный Pod.
2. **Нельзя переносить состояние между попытками.** Если Pod был отклонён и повторно планируется,
   `PreScore` отработает заново — это, наоборот, желаемое поведение: данные могли устареть.
3. **Нельзя удерживать ссылку на `CycleState` в поле плагина** (это ещё и нарушение `GO-08` по духу).
4. `CycleState` доступен и в цикле привязки, но пользоваться этим `CarbonAware` не будет: плагин не
   реализует ни одной точки расширения фазы привязки.

### 5.4. Передача данных PreScore → Score: проектное предложение

```go
const carbonStateKey fwk.StateKey = "carbon-aware.io/CarbonAware"

// carbonState is the immutable per-Pod result of the PreScore phase.
// It must never be mutated after being written to CycleState.
type carbonState struct {
	// перезаписи после Write запрещены; чтение из Score — конкурентное
	scorePerNode map[string]int64 // имя узла → предварительная оценка, fixed-point
	dataStale    bool             // сработал ли fallback по свежести данных
	zonesMissing int              // число узлов без label carbon-aware.io/zone
}

// Clone implements fwk.StateData.
func (s *carbonState) Clone() fwk.StateData { return s } // неизменяемо ⇒ копия не нужна
```

Обоснование каждого решения:

- **Карта «узел → уже посчитанное значение», а не «узел → исходные данные».** Тогда `Score`
  вырождается в один поиск по карте и не выполняет ни арифметики с плавающей точкой, ни аллокаций.
  Вся содержательная математика остаётся в `PreScore` — в чистой, детерминированной, табличными
  тестами покрываемой функции (`GO-13`, `DOD-TEST-02`, `DOD-TEST-03`).
- **`Clone()` возвращает `s` без копирования.** Это корректно **только** при соблюдении
  неизменяемости, и это соглашение должно быть закреплено комментарием и тестом. При нарушении
  неизменяемости появится гонка через клонированный `CycleState` в сценарии вытеснения.
- **Один ключ, а не несколько.** Один `Read` на вызов `Score` вместо трёх; меньше поверхность для
  коллизий имён.
- **Флаги деградации кладутся туда же.** `Score` не должен повторно проверять свежесть данных: это
  сделано один раз в `PreScore`, решение зафиксировано в состоянии.

Альтернатива «`Score` сам читает разделяемый кэш плагина под `RWMutex`» отвергается по двум причинам:
(1) до 16 goroutine борются за одну блокировку \(N_{nodes}\) раз на каждый Pod; (2) кэш может быть
обновлён фоновым обновлятором **между** вызовами `Score` для разных узлов, и разные узлы одного Pod
будут оценены по разным данным — неатомарность решения, разрушающая воспроизводимость эксперимента
(`REPRO-17`). Снимок в `PreScore` устраняет обе проблемы.

---

## 6. Взаимодействие с локальным кэшем узлов (`NodeInfo`)

### 6.1. Что такое снимок и когда он обновляется

Планировщик держит собственный кэш состояния кластера, наполняемый информерами. В начале **каждого**
цикла планирования из него делается снимок:

```go
if err := sched.Cache.UpdateSnapshot(klog.FromContext(ctx), sched.nodeInfoSnapshot); err != nil { /* ... */ }
```

Контракт доступа к снимку задан комментарием к `Handle.SnapshotSharedLister()`:

> SnapshotSharedLister returns listers from the latest NodeInfo Snapshot. The snapshot is taken at the
> beginning of a scheduling cycle and remains unchanged until a pod finishes "Permit" point.
>
> It should be used only during scheduling cycle:
> - There is no guarantee that the information remains unchanged in the binding phase of scheduling.
>   So, plugins shouldn't use it in the binding cycle (pre-bind/bind/post-bind/un-reserve plugin)
>   otherwise, a concurrent read/write error might occur.
> - There is no guarantee that the information is always up-to-date.
>   So, plugins shouldn't use it in QueueingHint and PreEnqueue otherwise, they might make a decision
>   based on stale information.

Для `CarbonAware` это удобно: обе наши точки расширения (`PreScore`, `Score`) находятся строго внутри
цикла планирования, где снимок валиден и неизменен.

### 6.2. Интерфейс `NodeInfo`

```go
type NodeInfo interface {
	Node() *v1.Node
	GetPods() []PodInfo
	GetUsedPorts() HostPortInfo
	// GetRequested returns total requested resources of all pods on this node. This includes assumed
	// pods, which scheduler has sent for binding, but may not be scheduled yet.
	GetRequested() Resource
	// GetNonZeroRequested return total requested resources of all pods on this node with a minimum
	// value applied to each container's CPU and memory requests.
	GetNonZeroRequested() Resource
	GetAllocatable() Resource
	GetImageStates() map[string]*ImageStateSummary
	GetPVCRefCounts() map[string]int
	GetGeneration() int64
	Snapshot() NodeInfo
	String() string

	AddPodInfo(podInfo PodInfo)
	RemovePod(logger klog.Logger, pod *v1.Pod) error
	SetNode(node *v1.Node)
}
```

Что из этого нужно плагину:

| Данные | Источник | Зачем в модели |
| --- | --- | --- |
| Энергозона узла | `nodeInfo.Node().Labels["carbon-aware.io/zone"]` | ключ поиска в углеродном кэше |
| Энергопрофиль узла | `nodeInfo.Node().Labels["carbon-aware.io/power-profile"]` | выбор \(P_{idle}\), \(P_{max}\) |
| Ёмкость узла | `nodeInfo.GetAllocatable()` | знаменатель утилизации |
| Текущая заявка | `nodeInfo.GetRequested()` | числитель утилизации **с учётом assumed Pod** |

Оценка утилизации после размещения Pod — вход энергомодели `SCOPE-IN-04`:

$$
u_n^-=\frac{R_{\mathrm{req}}(n)}{R_{\mathrm{alloc}}(n)},
\qquad
u_n^+=\frac{R_{\mathrm{req}}(n)+R_{\mathrm{pod}}}{R_{\mathrm{alloc}}(n)},
\qquad
P(u)=P_{\mathrm{idle}}+(P_{\max}-P_{\mathrm{idle}})\,u
$$

где \(u_n^-\) и \(u_n^+\) — прогнозируемая утилизация узла \(n\) до и после размещения
(доля, \([0,1]\)), \(R_{\mathrm{req}}(n)\) — уже запрошенные на узле ресурсы (millicores),
\(R_{\mathrm{pod}}\) — запрос планируемого Pod (millicores), \(R_{\mathrm{alloc}}(n)\) —
выделяемая ёмкость узла (millicores), \(P\) — мощность (W). Полная \(P(u_n^+)\) описывает
мощность узла для cluster KPI. В `PreScore` углеродная составляющая \(S_C\) строится по
приращению \(\Delta\widehat P_{p,n}=P(u_n^+)-P(u_n^-)\), чтобы не приписывать новому Pod
уже существующую idle- и соседскую мощность.

Существенно, что `GetRequested()` включает **assumed** Pod — те, что уже прошли `assume`, но ещё не
привязаны. Это частично гасит эффект «стада» при планировании пачки Pod: второй Pod пачки видит
узел уже нагруженным первым. Полностью эффект это не устраняет, поскольку углеродная интенсивность
зоны при этом не меняется (см. §11.4).

### 6.3. Правила работы со снимком

1. **`NodeInfo` — только на чтение.** Методы `AddPodInfo`, `RemovePod`, `SetNode` присутствуют в
   публичном интерфейсе, но предназначены фреймворку. Их вызов из плагина повреждает общий снимок
   для всех Pod, находящихся в обработке. Если нужна приватная копия — `Snapshot()`.
2. **В `Score` использовать переданный `nodeInfo`, а не искать узел в снимке.** Для `Filter` это
   зафиксировано в комментарии («instead of looking it up in the NodeInfoSnapshot because we don't
   guarantee that they will be the same»); тот же принцип применим и к `Score`, получающему
   `NodeInfo` с 1.33.
3. **Список узлов брать из аргумента `PreScore`, а не из `SnapshotSharedLister().NodeInfos().List()`.**
   В аргумент попадают только узлы, прошедшие `Filter`; `List()` вернёт все, включая недопустимые.
   Нормализация по всем узлам вместо допустимых исказит \(CF_{min}\), \(CF_{max}\).
4. **`handle.SnapshotSharedLister()` понадобится только фоновому обновлятору — и там он запрещён.**
   Для чтения меток узлов вне цикла планирования нужен информер:
   `handle.SharedInformerFactory().Core().V1().Nodes().Lister()`.

---

## 7. Нормализация оценок и агрегация весов

### 7.1. Константы и проверка диапазона

```go
const (
	// Deprecated: use MaxScore instead.
	MaxNodeScore int64 = MaxScore
	// Deprecated: use MinScore instead.
	MinNodeScore int64 = MinScore

	// MaxScore is the maximum score a Score or PlacementScore plugin is expected to return.
	MaxScore int64 = 100
	// MinScore is the minimum score a Score or PlacementScore plugin is expected to return.
	MinScore int64 = 0

	// MaxTotalScore is the maximum total score.
	MaxTotalScore int64 = math.MaxInt64
)
```

`MinNodeScore`/`MaxNodeScore` с 1.36 — устаревшие псевдонимы `MinScore`/`MaxScore`; числовые
значения (0 и 100) не менялись.

Проверка диапазона выполняется **после** `NormalizeScore`, при взвешивании:

```go
if score > fwk.MaxScore || score < fwk.MinScore {
	err := fmt.Errorf("plugin %q returns an invalid score %v, it should in the range of [%v, %v] after normalizing",
		pl.Name(), score, fwk.MinScore, fwk.MaxScore)
	errCh.SendWithCancel(err, cancel)
	return
}
```

Ошибка оборачивается в `fwk.AsStatus(...)` с кодом `Error`. Последствия: **весь цикл планирования
для этого Pod прерывается**, Pod уходит в backoffQ и будет перепланирован. Он не помечается как
`Unschedulable` — с точки зрения оператора это выглядит как внутренний сбой планировщика, а не как
проблема размещения. Диагностический вывод: ошибка нормализации в `CarbonAware` проявится в
экспериментах как рост задержки планирования и записи `Error` в
`framework_extension_point_duration_seconds{extension_point="Score",status="Error"}`, а вовсе не как
«Pod не размещён из-за углерода». Отсюда требование `DOD-TEST-04` о явном покрытии случая
\(CF_{max} = CF_{min}\) и `GO-14` о защите деления.

Из того, что проверка стоит **после** нормализации, следует полезная свобода: **`Score` вправе
возвращать значения вне \([0,100]\)**, если `NormalizeScore` приводит их в диапазон.

### 7.2. Выбор схемы нормализации: проектное предложение

Спецификация (`SCOPE-IN-07`) требует агрегата

$$
S = w_C S_C + w_R S_R + w_P S_P, \qquad \sum_i w_i = 1, \qquad S \in [0,100]
$$

где \(S\) — итоговая оценка узла (безразмерная), \(S_C, S_R, S_P\) — углеродная, ресурсная и
производительностная составляющие (каждая в \([0,100]\)), \(w_C, w_R, w_P\) — веса критериев
(безразмерные).

Ограничение, определяющее схему: **`NormalizeScore` получает только `NodeScoreList`** — пары
«имя узла → `int64`». Свести три критерия в один агрегат на этапе `NormalizeScore` невозможно: к
этому моменту составляющие уже потеряны. Значит, взвешенная сумма считается в `Score`, а
`NormalizeScore` работает с уже агрегированным скаляром.

Предлагаемое распределение обязанностей:

| Фаза | Что делает | Диапазон результата |
| --- | --- | --- |
| `PreScore` | \(CF(n)\) для всех узлов, \(CF_{min}\), \(CF_{max}\); \(S_C(n)\) min-max нормализацией; \(S_R(n)\), \(S_P(n)\); взвешенная сумма в **fixed-point** \(\times 100\) | \([0, 10000]\), `int64` |
| `Score` | поиск готового значения по имени узла в карте | \([0, 10000]\) |
| `NormalizeScore` | деление на 100 с округлением; клампинг в \([0,100]\) как страховка | \([0, 100]\) |

Fixed-point \([0,10000]\) предотвращает преждевременное округление отдельных составляющих при
расчёте взвешенной суммы и сохраняет два десятичных знака до границы контракта. Однако итоговый
результат Scheduler Framework всё равно содержит только 101 значение \([0,100]\), поэтому близкие
кандидаты после финального округления могут получить одинаковые оценки. Fixed-point уменьшает
арифметическую погрешность, но не устраняет квантование API; частоту ничьих и невырожденность
распределения необходимо проверить тестом на зафиксированном датасете (`DOD-TEST-03`).

Роль `NormalizeScore` при такой схеме — не «нормализация» в статистическом смысле (она уже
выполнена в `PreScore`, где известны \(CF_{min}\) и \(CF_{max}\)), а **гарантия контракта**: ни при
каких входных данных плагин не отдаст фреймворку значение вне \([0,100]\). Это дешёвая страховка
против прерывания цикла планирования по §7.1.

Защита от вырождения (`GO-14`, `DOD-TEST-04`):

$$
S_C(n) = \begin{cases}
100 \cdot \dfrac{CF_{max} - CF(n)}{CF_{max} - CF_{min}}, & CF_{max} - CF_{min} > \varepsilon \\[2ex]
S_{neutral}, & \text{иначе}
\end{cases}
$$

где \(\varepsilon\) — порог значимости разброса (gCO2e), \(S_{neutral}\) — нейтральная оценка,
одинаковая для всех узлов. Тот же \(S_{neutral}\) применяется к узлу без label
`carbon-aware.io/zone` (`SCOPE-EDGE-02`).

### 7.3. Агрегация весов плагинов фреймворком

После `NormalizeScore` фреймворк выполняет взвешивание и суммирование:

```go
weightedScore := score * int64(weights[i])
nodePluginScores[i] = fwk.PluginScore{Name: pl.Name(), Score: weightedScore}
totalScore += weightedScore
```

$$
\text{TotalScore}(n) = \sum_{p \in P_{enabled}} w_p \cdot S_p(n)
$$

где \(\text{TotalScore}(n)\) — итоговая оценка узла \(n\), \(P_{enabled}\) — множество включённых
в профиле Score-плагинов, \(w_p\) — вес плагина \(p\) из `KubeSchedulerConfiguration`
(целое, безразмерное), \(S_p(n) \in [0,100]\) — нормализованная оценка плагина \(p\).

Побеждает узел с максимальным `TotalScore`; при равенстве максимума выбор рандомизируется
(поле `Randomizer` в `NodePluginScores`).

Правила обращения с весами, зафиксированные в `getValidScoreWeights`:

- **Вес 0 недопустим и молча превращается в 1** («a weight of zero is not permitted, plugins can be
  disabled explicitly when configured»). Отключить плагин можно только через `disabled`.
- **Явный вес в `plugins.score.enabled[]` перекрывает вес в `multiPoint`** для того же плагина.
- Максимума веса нет; на старте проверяется переполнение
  \(\sum_p w_p \cdot 100 \le \texttt{math.MaxInt64}\), при нарушении планировщик **не стартует** с
  ошибкой `total score of Score plugins could overflow`.
- Отдельная проверка в `NewFramework` требует, чтобы у каждого Score-плагина вес был определён;
  типовая причина ошибки — расхождение между именем в реестре и значением, возвращаемым `Name()`.

### 7.4. Два уровня весов — источник методической ошибки

В системе присутствуют **две независимые системы весов**, которые легко перепутать:

| Уровень | Веса | Где задаются | Сумма |
| --- | --- | --- | --- |
| Внутренний (модель) | \(w_C, w_R, w_P\) | ConfigMap / `pluginConfig.args` плагина | \(\sum w_i = 1\) (`SCOPE-IN-07`) |
| Внешний (фреймворк) | \(w_p\) для каждого Score-плагина | `plugins.multiPoint.enabled[].weight` | произвольная, целые числа |

Вклад `CarbonAware` в итоговое решение определяется **вторым** уровнем, и по умолчанию он мал.
Профиль `carbon-aware-scheduler`, объявленный как в §48 спецификации, наследует все стандартные
плагины. Их веса в 1.37:

| Score-плагин по умолчанию | Вес |
| --- | --- |
| `TaintToleration` | 3 |
| `NodeAffinity` | 2 |
| `PodTopologySpread` | 2 |
| `InterPodAffinity` | 2 |
| `NodeResourcesFit` | 1 |
| `NodeResourcesBalancedAllocation` | 1 |
| `ImageLocality` | 1 |
| `VolumeBinding` | 1 (вес не задан явно ⇒ 1) |
| **Сумма** | **13** |

Добавление `CarbonAware` без явного веса даёт \(w_{CarbonAware} = 1\) и теоретический максимум
вклада \(100\) из \(1400\), то есть **около 7 %**. Практический вклад определяется не максимумом, а
**разбросом** взвешенных оценок между узлами: на однородном kind-стенде без taint'ов и affinity
`TaintToleration` и `NodeAffinity` вернут одинаковые значения всем узлам и на выбор не повлияют,
тогда как `NodeResourcesBalancedAllocation` и `NodeResourcesFit` дадут реальный разброс, прямо
конкурирующий с углеродным критерием.

Отсюда два вывода для этапа реализации:

1. **Вес `CarbonAware` должен задаваться явно и попадать в manifest окружения прогона** (`REPRO-06`,
   `REPRO-09`). Это параметр эксперимента, а не деталь конфигурации.
2. **Разброс взвешенных оценок по плагинам нужно логировать** для хотя бы одного прогона, иначе
   невозможно отличить «алгоритм не даёт эффекта» от «эффект есть, но его перебивают стандартные
   плагины». Это кандидат в метрику (`DOD-CODE-13`).

### 7.5. Стратегия B (carbon-only) и её конфигурация

`SCOPE-IN-13` требует стратегию B — carbon-only (**минимум текущей** \(CI\)), то есть наивный
выбор «самого зелёного узла сейчас», а не минимум ожидаемого следа \(\widehat{CF}\) по горизонту
runtime. Это сознательное различие с стратегией C: иначе на однородных энергопрофилях и
постоянном \(CI\) стратегии B и C совпали бы, и эксперимент не проверял бы вклад прогноза и
энергомодели. Реализуется тем же бинарём: отдельный профиль, стандартные Score-плагины в фазе
оценки отключены, внутренние веса \(w_C = 1,\, w_R = w_P = 0\), а \(S_C\) считается по текущему
(или persistence) average \(CI\) зоны, без интегрирования по \(\widehat T\) и без множителя
\(\Delta\widehat P\). Режим задаётся аргументом плагина (например `scoringMode: current-ci`),
а не отдельным типом.

Здесь есть ловушка, которую легко не заметить. Интуитивная конфигурация «включить `CarbonAware`
через `multiPoint` и отключить остальные через `score.disabled: ['*']`» **не работает**: в
`expandMultiPointPlugins` проверка `disabled: '*'` стоит **до** развёртки и обрывает её целиком для
всей точки расширения:

```go
if disabledSet.Has("*") {
	logger.V(4).Info("Skipped MultiPoint expansion because all plugins are disabled for extension point", "extension", pluginType)
	continue
}
```

То есть `CarbonAware`, попавший в `Score` только через `multiPoint`, будет отключён вместе со
стандартными плагинами, и профиль останется вообще без оценки. Планировщик при этом стартует без
ошибки — дефект проявится лишь как «стратегия B неотличима от случайного выбора».

Работоспособный вариант — регистрировать плагин **явно в конкретных точках расширения**, поскольку
`updatePluginList` выполняется до развёртки `multiPoint` и на неё не влияет:

```yaml
apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
profiles:
  - schedulerName: carbon-only-scheduler
    plugins:
      preScore:
        enabled:
          - name: CarbonAware
        disabled:
          - name: '*'
      score:
        enabled:
          - name: CarbonAware
            weight: 1
        disabled:
          - name: '*'
    pluginConfig:
      - name: CarbonAware
        args:
          scoringMode: current-ci
          carbonWeight: 1.0
          resourceWeight: 0.0
          performanceWeight: 0.0
```

`disabled: '*'` указан только для `preScore` и `score`, поэтому `Filter`-часть стандартных плагинов
(`NodeResourcesFit`, `NodeUnschedulable`, `TaintToleration`, `NodePorts`, …) продолжает
регистрироваться обычной развёрткой `multiPoint` по умолчанию: ресурсная выполнимость остаётся
жёстким ограничением, как требует `SCOPE-IN-08`.

Это частный случай общего правила приоритетов (`1. Specific extension points, 2. Explicitly
configured MultiPoint plugins, 3. The set of default plugins, as MultiPoint plugins`): явная
регистрация в конкретной точке сильнее `multiPoint`, а `disabled: '*'` действует только на то, что
пришло бы из `multiPoint`.

**Проверка обязательна экспериментом, а не чтением конфигурации.** Приёмочный критерий для профиля
стратегии B: в логе планировщика при `-v=4` присутствует запись о регистрации `CarbonAware` в
`Score`, а метрика `scheduler_plugin_evaluation_total{plugin="CarbonAware",extension_point="Score"}`
растёт при подаче нагрузки.

---

## 8. Регистрация плагина и конфигурация профилей

### 8.1. Точка входа

Плагин компилируется в бинарь планировщика (`SCOPE-OUT-07`, `SCOPE-OUT-08`), «out-of-tree» здесь
означает «код живёт в нашем репозитории», а не «подключается динамически». Go не поддерживает
подключение плагинов планировщика в рантайме.

```go
// cmd/scheduler/main.go
func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(carbonaware.Name, carbonaware.New),
	)
	code := cli.Run(command)
	os.Exit(code)
}
```

Сигнатура фабрики (`k8s.io/kubernetes/pkg/scheduler/framework/runtime`):

```go
type PluginFactory = func(ctx context.Context, configuration runtime.Object, f fwk.Handle) (fwk.Plugin, error)
```

Три следствия, важных для реализации:

1. **`configuration runtime.Object`** — это содержимое `pluginConfig.args` из
   `KubeSchedulerConfiguration`, разбираемое через `runtime.DecodeInto(configuration, args)`. Через
   него передаются \(w_C, w_R, w_P\), \(P_{idle}\), \(P_{max}\), `maxDataAgeSeconds`, адрес Carbon
   Data Service — то есть выполняется `GO-12` (никаких магических чисел) и `REPRO-06`.
2. **`ctx`, переданный в фабрику, живёт столько же, сколько планировщик.** У плагинов нет хука
   `Start()`, поэтому фоновая goroutine обновления кэша запускается именно здесь и завершается по
   отмене этого контекста. Это единственное корректное место для её жизненного цикла.
3. **`fwk.Handle`** даёт доступ к `SharedInformerFactory()`, `ClientSet()`, `Parallelizer()`,
   `EventRecorder()` и `SnapshotSharedLister()`. Фоновый обновлятор пользуется информерами;
   `SnapshotSharedLister()` вне цикла планирования использовать нельзя (§6.1).

### 8.2. Профиль

```yaml
apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration
parallelism: 16
profiles:
  - schedulerName: default-scheduler        # baseline A, не трогаем

  - schedulerName: carbon-aware-scheduler   # стратегия C
    plugins:
      multiPoint:
        enabled:
          - name: CarbonAware
            weight: 13                      # параметр эксперимента, см. §7.4
    pluginConfig:
      - name: CarbonAware
        args:
          carbonWeight: 0.6
          resourceWeight: 0.2
          performanceWeight: 0.2
          maxDataAgeSeconds: 300
```

Семантика `multiPoint`: плагин автоматически регистрируется на **все** точки расширения, которые он
реализует. Поскольку `CarbonAware` реализует только `PreScorePlugin` и `ScorePlugin` (а
`NormalizeScore` — не отдельная точка, а метод `ScoreExtensions`), одной записи достаточно; ничего
лишнего не включится. Это дополнительный аргумент в пользу минимального набора интерфейсов: **набор
точек расширения плагина при `multiPoint` определяется тем, какие интерфейсы реализует его тип**.
Случайно добавленный метод `Filter` с правильной сигнатурой автоматически включит плагин в фазу
фильтрации и нарушит `SCOPE-OUT-16`, ничего при этом не сломав видимым образом.

**Проектное предложение**: закрепить это compile-time проверками и тестом, запрещающим лишние
интерфейсы:

```go
var (
	_ fwk.Plugin         = &CarbonAware{}
	_ fwk.PreScorePlugin = &CarbonAware{}
	_ fwk.ScorePlugin    = &CarbonAware{}
	_ fwk.ScoreExtensions = &CarbonAware{}
)

// Тест утверждает обратное: плагин НЕ реализует запрещённые Scope точки расширения.
func TestPluginImplementsOnlyAllowedExtensionPoints(t *testing.T) {
	var p any = &CarbonAware{}
	if _, ok := p.(fwk.FilterPlugin); ok    { t.Error("SCOPE-OUT-16: Filter запрещён") }
	if _, ok := p.(fwk.PermitPlugin); ok    { t.Error("SCOPE-OUT-16: Permit запрещён") }
	if _, ok := p.(fwk.QueueSortPlugin); ok { t.Error("SCOPE-OUT-16: QueueSort запрещён") }
	if _, ok := p.(fwk.ReservePlugin); ok   { t.Error("SCOPE-OUT-16: Reserve — только по ADR") }
}
```

---

## 9. Безопасная интеграция без HTTP-запросов в цикле Score

Требование `SCOPE-OUT-17` и `DOD-CODE-12` — никаких сетевых вызовов, обращений к диску и блокирующих
ожиданий в `PreScore`/`Score`. Ниже — схема, удовлетворяющая ему.

### 9.1. Разделение на два контура

```text
┌── Контур обновления (goroutine, вне цикла планирования) ─────────────┐
│                                                                      │
│  time.Ticker (refreshInterval, по умолчанию 60 s)                    │
│        ↓                                                             │
│  HTTP GET к Carbon Data Service, с таймаутом (GO-09)                 │
│        ↓                                                             │
│  валидация: CI ≥ 0, не NaN, timestamp не в будущем (DOD-TEST-04)     │
│        ↓                                                             │
│  сборка НОВОГО неизменяемого снимка *carbonSnapshot                  │
│        ↓                                                             │
│  atomic.Pointer[carbonSnapshot].Store(newSnapshot)   ← единственная  │
│                                                        точка записи  │
└──────────────────────────────────────────────────────────────────────┘
                                 │
                    atomic.Pointer.Load() — O(1), без блокировок
                                 ↓
┌── Цикл планирования (главный поток → 16 goroutine) ──────────────────┐
│                                                                      │
│  PreScore(pod, nodes):                                               │
│      snap := p.snapshot.Load()          ← ОДИН Load на весь Pod      │
│      if snap == nil || snap.age > maxDataAge:                        │
│          metrics.fallback_total.Inc()                                │
│          записать одинаковый S_neutral для всех nodes в CycleState  │
│          return nil                     ← штатный Success             │
│      вычислить CF(n) для всех n, найти CF_min, CF_max                │
│      state.Write(carbonStateKey, &carbonState{...})                  │
│                                                                      │
│  Score(pod, nodeInfo):        ← до 16 goroutine, N_nodes вызовов     │
│      s, _ := state.Read(carbonStateKey)                              │
│      return s.(*carbonState).scorePerNode[nodeInfo.Node().Name], nil │
│      ── ни I/O, ни блокировок, ни аллокаций                          │
│                                                                      │
│  NormalizeScore(pod, scores):                                        │
│      деление на 100 + клампинг в [0,100]                             │
└──────────────────────────────────────────────────────────────────────┘
```

Обоснование выбора `atomic.Pointer` вместо `sync.RWMutex` (`GO-10` допускает оба): снимок
заменяется целиком и никогда не мутируется частично, поэтому достаточно атомарной замены указателя.
Это избавляет от блокировки на горячем пути и делает невозможным чтение полусобранного состояния.
`RWMutex` понадобился бы при инкрементальном обновлении отдельных зон — такого сценария нет.

Единственный `Load()` в `PreScore` (а не в каждом `Score`) даёт ещё одно свойство: **все узлы одного
Pod оцениваются по одному и тому же снимку данных**, даже если фоновый обновлятор сработал в
середине цикла. Без этого результат зависел бы от гонки таймера с планировщиком и был бы
невоспроизводим (`REPRO-17`).

### 9.2. Цепочка деградации

`SCOPE-IN-09` требует цепочку `fresh cache → stale cache → отключение carbon scoring → baseline
behaviour`. Отображение на механику фреймворка:

| Состояние данных | Действие плагина | Механизм | Наблюдаемый эффект |
| --- | --- | --- | --- |
| Свежие | обычный расчёт | — | углеродное ранжирование работает |
| Устаревшие, но в пределах допуска | расчёт + пометка в состоянии | флаг в `carbonState` | работает, помечено в метрике качества данных (`SCOPE-EDGE-03`) |
| Старше `maxDataAgeSeconds` | одинаковая нейтральная оценка всем узлам | `PreScore` записывает \(S_{neutral}\), возвращает `Success` | вклад `CarbonAware` не меняет argmax; решают стандартные плагины (`SCOPE-EDGE-04`) |
| Кэш пуст (сервис не отвечал ни разу) | то же | нейтральные оценки + `Success` | планирование не ломается (`DOD-TEST-07`) |
| Узел без label зоны | нейтральная оценка **этому узлу** | \(S_{neutral}\) в `PreScore` | узел не исключается (`SCOPE-EDGE-02`) |

Нейтральный балл, предписанный `SCOPE-EDGE-04`, должен быть одинаковым **итоговым** вкладом
`CarbonAware` для всех узлов. Недостаточно нейтрализовать только \(S_C\), оставив различающиеся
\(S_R\) и \(S_P\): тогда профиль не вернётся к baseline behaviour. `PreScore` записывает готовую
константу для каждого узла, `Score` штатно её возвращает, а `NormalizeScore` сохраняет равенство.
Срабатывание наблюдается через собственный `fallback_total{reason}`; стандартный
`scheduler_plugin_evaluation_total` при этом продолжает расти и подтверждает, что плагин не
прервал scheduling cycle.

Критически важно, чего делать **нельзя**: возвращать `Error` при проблемах с данными. Любой
не-`Success`/не-`Skip` статус из `PreScore` или `Score` **отклоняет Pod**. Проблема с внешним
источником углеродных данных не должна приводить к тому, что рабочая нагрузка не запускается
(`DOD-TEST-07`: ожидание — деградация, а не отказ планирования).

### 9.3. Наблюдаемость накладных расходов

Эксперимент «overhead планировщика» (`SCOPE-IN-14`) должен опираться на:

| Метрика | Замечание |
| --- | --- |
| `scheduler_framework_extension_point_duration_seconds{extension_point="PreScore"\|"Score"}` | не семплируется, пишется каждый цикл — **основной источник** |
| `scheduler_plugin_execution_duration_seconds{plugin="CarbonAware"}` | **семплируется: `pluginMetricsSamplePercent = 10`**, только 10 % циклов |
| `scheduler_scheduling_attempt_duration_seconds` | полная попытка, включая привязку |
| `scheduler_pod_scheduling_sli_duration_seconds` | сквозная задержка с точки зрения Pod |

Семплирование `plugin_execution_duration_seconds` — известная ловушка: при малом числе Pod в
сценарии выборка окажется непредставительной, а `DOD-EXP-06` требует mean, median, p95 и разброс.
Основные измерения накладных расходов следует строить на
`framework_extension_point_duration_seconds`, а `plugin_execution_duration_seconds` использовать как
уточняющую метрику.

---

## 10. Проверка корректности описания типов

Все сигнатуры ниже сверены с ветвью `release-1.37` (файлы и строки — в §12).

| Тип | Проверенное утверждение | Статус |
| --- | --- | --- |
| `framework.Plugin` | `k8s.io/kube-scheduler/framework`; единственный метод `Name() string`; встраивается во все интерфейсы точек расширения | ✅ |
| `framework.CycleState` | **интерфейс** (с 1.34), не структура; `Read`/`Write`/`Delete`/`Clone` + служебные методы; реализация на `sync.Map`; `Read` возвращает `ErrNotFound` | ✅ |
| `framework.NodeInfo` | **интерфейс** (с 1.34); доступ через геттеры `Node()`, `GetRequested()`, `GetAllocatable()`, `GetPods()`; мутирующие методы предназначены фреймворку | ✅ |
| `framework.Status` | структура с неэкспортируемыми полями `code`, `reasons`, `err`, `plugin`; `nil` эквивалентен `Success`; коды `Success`, `Error`, `Unschedulable`, `UnschedulableAndUnresolvable`, `Wait`, `Skip`, `Pending` | ✅ |
| `ScorePlugin.Score` | принимает `nodeInfo fwk.NodeInfo` (не `nodeName string` — изменено в 1.33), возвращает `(int64, *Status)` | ✅ |
| `PreScorePlugin.PreScore` | принимает `nodes []fwk.NodeInfo` (не `[]*v1.Node` — изменено в 1.30); статус `Skip` пропускает `Score` | ✅ |
| `ScoreExtensions.NormalizeScore` | получает `scores NodeScoreList`, модифицирует список на месте; `ScoreExtensions()` может вернуть `nil` | ✅ |
| `MinScore` / `MaxScore` | 0 и 100; `MinNodeScore`/`MaxNodeScore` — устаревшие псевдонимы с 1.36 | ✅ |
| `MaxTotalScore` | `math.MaxInt64`; проверяется на старте планировщика | ✅ |
| Проверка диапазона | выполняется **после** `NormalizeScore`, ошибка имеет код `Error` и прерывает цикл | ✅ |

Отдельно отмечено как **не подтверждённое**: в исходном коде Kubernetes отсутствует комментарий,
прямо запрещающий мутацию `StateData` или объявляющий её содержимое непотокобезопасным (§5.2).
Требование выводится из модели исполнения и должно формулироваться именно так.

---

## 11. Критические ограничения для фазы реализации

### 11.1. Версии toolchain должны быть согласованы (см. §1.4)

До сборки плагина `go.mod` и governance необходимо синхронизировать с минимальной версией Go,
которую требует выбранная patch-версия Kubernetes 1.37. Известный рабочий вариант — Go 1.26.x.
Это контроль конфигурации реализации; математическое моделирование и выбор extension points уже
могут продолжаться.

### 11.2. Реализация жёсткой проверки SLA до carbon scoring

`SCOPE-IN-08` требует применять ресурсную выполнимость и SLA до углеродного скоринга. Эти проверки
реализуются двумя штатными механизмами Scheduler Framework:

1. стандартный `NodeResourcesFit` в фазе `Filter` формирует множество ресурсно допустимых узлов;
2. `CarbonAware.PreScore` проверяет workload-level предикат
   \(t_{\mathrm{now}}+\widehat T_{\mathrm{class}(p)}\le t_{\mathrm{deadline},p}\) до расчёта
   углеродных оценок.

Модель `SCOPE-IN-06` задаёт \(\widehat T\) таблицей «класс workload → expected runtime», поэтому в
текущем Scope условие SLA относится к Pod в целом, а не различается по узлам. Если условие не
выполнено, `PreScore` возвращает `fwk.NewStatus(fwk.Error, "SLA precondition failed")`. Runtime
Scheduler Framework оборачивает non-success из `PreScore` в статус `Error`, завершает попытку до
`Score` и оставляет Pod в `Pending` для стандартного scheduler backoff/retry. Это намеренная
fail-closed валидация конфигурации workload: исправление deadline или класса runtime делает
следующую попытку допустимой; ожидание более чистого carbon window не выполняется. Причина
отдельно учитывается метрикой `sla_rejection_total`, чтобы не смешивать её с отказом carbon data.
Контракт фиксируется integration-тестом для целевой patch-версии Kubernetes 1.37. Если SLA
соблюдается, `PreScore` рассчитывает углеродные оценки только по узлам, которые уже прошли
стандартную ресурсную фильтрацию.

Нулевая оценка в `Score` для жёсткой проверки не используется: значение 0 ранжирует узел, но не
исключает попытку планирования. Производительностная составляющая \(S_P\) остаётся мягким критерием
различения ресурсно допустимых узлов, тогда как workload-level SLA проверяется отдельно и раньше.
Такой контракт сохраняет разрешённый набор `PreScore` + `Score` + `NormalizeScore` и не требует
добавления собственного `Filter`.

### 11.3. Разрешающая способность целочисленной шкалы

101 различимое значение оценки на 4–8 узлов однородного стенда с близкими значениями \(CI\) — риск
получить одинаковые оценки и решение, определяемое рандомизацией фреймворка, а не алгоритмом. Меры:
внутренняя fixed-point шкала (§7.2) и обязательная проверка в эксперименте, что распределение
итоговых `TotalScore` по узлам невырождено. Гипотеза, требующая проверки на реальном датасете.

### 11.4. Эффект «стада» при пачечном планировании

Углеродная интенсивность зоны не зависит от того, сколько Pod мы туда уже отправили в текущей пачке.
`GetRequested()` учитывает assumed Pod и частично компенсирует это через ресурсную составляющую
\(S_R\), но при \(w_C = 0.6\) компенсации может не хватить: вся пачка уйдёт в самую чистую зону,
перегрузит её и увеличит фактическое время выполнения. Штатное средство — `Reserve` с учётом
«углеродного бюджета» зоны, что требует ADR (`SCOPE-OUT-16`). Кандидат в отдельный экспериментальный
сценарий: сравнение поштучной и пачечной подачи нагрузки.

### 11.5. Разбавление углеродного сигнала стандартными плагинами

Суммарный вес стандартных Score-плагинов — 13 (§7.4). Вес `CarbonAware` обязателен к явному заданию
и должен фиксироваться в manifest окружения прогона как параметр эксперимента.

### 11.6. Хрупкость привязки к версии

Контракт `Score` менялся в 1.33, типы стали интерфейсами в 1.34, интерфейсы плагинов переехали в
staging в 1.35, константы переименованы в 1.36. Практические следствия:

- patch-версию Kubernetes зафиксировать в `go.mod` и в manifest окружения (`REPRO-04`, `REPRO-08`);
- любые примеры плагинов из интернета (включая `kubernetes-sigs/scheduler-plugins` до 1.33) считать
  устаревшими и не копировать сигнатуры;
- обновление minor-версии Kubernetes в ходе работы над дипломом трактовать как ломающее изменение.

### 11.7. `multiPoint` молчаливо включает и молчаливо выключает

Две ошибки конфигурации, ни одна из которых не приводит к падению планировщика:

1. **Молчаливое включение.** Набор точек расширения при `multiPoint` определяется реализованными
   интерфейсами, а не конфигурацией. Добавление метода с сигнатурой `Filter` или `Permit` включит
   соответствующую фазу и нарушит `SCOPE-OUT-16`, ничего видимо не сломав. Защита — compile-time
   утверждения и негативный тест (§8.2).
2. **Молчаливое выключение.** `disabled: '*'` в конкретной точке расширения обрывает развёртку
   `multiPoint` для всей этой точки, включая наш собственный плагин (§7.5). Профиль стратегии B,
   собранный «очевидным» способом, останется вообще без оценки узлов. Защита — явная регистрация в
   конкретных точках расширения и приёмочная проверка по метрике `plugin_evaluation_total`.

Общий вывод: **корректность конфигурации профиля должна проверяться наблюдаемым поведением
планировщика, а не чтением YAML.** Для каждого из трёх профилей эксперимента (A, B, C) нужен
приёмочный тест, подтверждающий фактический состав активных плагинов.

---

## 12. Источники

Исходный код Kubernetes, ветка `release-1.37` (проверено 2026-09-04):

- [`staging/src/k8s.io/kube-scheduler/framework/interface.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/staging/src/k8s.io/kube-scheduler/framework/interface.go) — `Plugin`, `PreScorePlugin`, `ScorePlugin`, `ScoreExtensions`, `FilterPlugin`, `ReservePlugin`, `PermitPlugin`, `Status`, `Code`, `Handle`, константы `MinScore`/`MaxScore`/`MaxTotalScore`
- [`staging/src/k8s.io/kube-scheduler/framework/cycle_state.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/staging/src/k8s.io/kube-scheduler/framework/cycle_state.go) — интерфейс `CycleState`, `StateData`, `StateKey`, `ErrNotFound`
- [`staging/src/k8s.io/kube-scheduler/framework/types.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/staging/src/k8s.io/kube-scheduler/framework/types.go) — интерфейс `NodeInfo`
- [`staging/src/k8s.io/kube-scheduler/framework/listers.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/staging/src/k8s.io/kube-scheduler/framework/listers.go) — `SharedLister`, `NodeInfoLister`
- [`pkg/scheduler/framework/cycle_state.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/pkg/scheduler/framework/cycle_state.go) — реализация на `sync.Map`, комментарий «write once and read many times»
- [`pkg/scheduler/framework/runtime/framework.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/pkg/scheduler/framework/runtime/framework.go) — `RunPreScorePlugins`, `RunScorePlugins`, `normalizeScores`, `getValidScoreWeights`, `expandMultiPointPlugins`
- [`pkg/scheduler/schedule_one.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/pkg/scheduler/schedule_one.go) — `scheduleOnePod`, `schedulingCycle`, `bindingCycle`, `UpdateSnapshot`, `pluginMetricsSamplePercent`
- [`pkg/scheduler/framework/parallelize/parallelism.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/pkg/scheduler/framework/parallelize/parallelism.go) — `DefaultParallelism = 16`, `chunkSizeFor`
- [`pkg/scheduler/apis/config/v1/default_plugins.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/pkg/scheduler/apis/config/v1/default_plugins.go) — веса стандартных плагинов
- [`pkg/scheduler/apis/config/validation/validation.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/pkg/scheduler/apis/config/validation/validation.go) — `validateCommonQueueSort`
- [`staging/src/k8s.io/kube-scheduler/config/v1/types.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/staging/src/k8s.io/kube-scheduler/config/v1/types.go) — `Plugin.Weight`, `PluginConfig`, семантика `MultiPoint`
- [`cmd/kube-scheduler/app/server.go`](https://github.com/kubernetes/kubernetes/blob/release-1.37/cmd/kube-scheduler/app/server.go) — `NewSchedulerCommand`, `WithPlugin`, `Option`

Документация и проектные документы:

- [Scheduling Framework](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/) — kubernetes.io
- [Scheduler Configuration](https://kubernetes.io/docs/reference/scheduling/config/) — kubernetes.io, профили и `multiPoint`
- [KEP-624: Scheduling Framework](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/624-scheduling-framework/README.md) — раздел Concurrency, жизненный цикл `CycleState`
- [CHANGELOG-1.30](https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.30.md), [CHANGELOG-1.33](https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.33.md), [CHANGELOG-1.34](https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.34.md), [CHANGELOG-1.35](https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.35.md), [CHANGELOG-1.37](https://github.com/kubernetes/kubernetes/blob/master/CHANGELOG/CHANGELOG-1.37.md) — изменения сигнатур, перенос типов в staging, сборка Go 1.26.5
- [`docs/ssot/governance.md`](../ssot/governance.md), [`docs/ssot/engineering-specification.md`](../ssot/engineering-specification.md) — нормы Scope и DoD
