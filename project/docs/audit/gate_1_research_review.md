# Quality Gate 1 — аудит исследовательского этапа

- **Дата:** 2026-09-04
- **Роль:** Lead Research Auditor / научный консультант
- **Статус:** **PASS**
- **Решение:** **Stage 2 — UNLOCKED**
- **Проверяемый переход:** Stage 1 Research → Stage 2 Mathematical Modeling

## 1. Итоговый вердикт

Исследования T1.1 (carbon data), T1.2 (power models) и T1.3 (Scheduler Framework) образуют
непрерывную и технически реализуемую цепочку:

```text
carbon data CI(t)
    → нормализация, forecast/replay и freshness
    → оценка мощности и маржинальной энергии workload
    → expected carbon footprint по горизонту runtime
    → resource Filter + SLA в PreScore
    → PreScore → Score → NormalizeScore
    → выбор узла или безопасный fallback к baseline
```

Критические расхождения между отчётами (единицы энергии, объект \(CF\) для scoring, семантика
стратегии B, трактовка SLA без собственного `Filter`) устранены. Оставшиеся замечания относятся к
синхронизации toolchain и заполнению малых SSOT-документов уровнем L1; они не меняют
математический объект Stage 2.

Количество блокеров Stage 2: **0**.

**QUALITY GATE 1: PASS. Stage 2 (Mathematical Modeling) разблокирован.**

## 2. Проверенные материалы

Обязательные входы:

| ID | Файл | Тема |
| --- | --- | --- |
| T1.1 | [carbon_intensity_datasets.md](../research/carbon_intensity_datasets.md) | источники, единицы, replay, freshness |
| T1.2 | [power_models_state_of_the_art.md](../research/power_models_state_of_the_art.md) | \(P(u)\), калибровка, атрибуция |
| T1.3 | [scheduler_framework_analysis.md](../research/scheduler_framework_analysis.md) | Scheduler Framework 1.37, extension points |
| — | [governance.md](../ssot/governance.md) | Scope, DoD, воспроизводимость |

Дополнительно сверены `engineering-specification.md` (§11–13, §88–89, §129–131), малые SSOT
(`architecture.md`, `interfaces.md`, `glossary.md`), `go.mod`, отчёты T1.1–T1.3 и сводный черновик
главы 1.

Нумерация T1.x следует заголовкам файлов и `docs/research/README.md`: T1.1 — carbon data, T1.2 —
power models, T1.3 — Scheduler Framework.

## 3. Критерии аудита

1. Непрерывность входов и выходов между тремя исследованиями.
2. Размерности W, s, kWh, `gCO2e/kWh` и gCO2e.
3. Различение полного `cluster CF` и ожидаемого вклада нового Pod.
4. Согласованность runtime, SLA и порядка extension points.
5. Семантика average / direct / lifecycle carbon intensity.
6. Гранулярность, forecast, replay, freshness и fallback.
7. Соответствие обязательному Scope и запретам `SCOPE-OUT-*`.
8. Наличие проверяемого пути реализации в Kubernetes Scheduler Framework — **без** утверждений о
   невозможности реализации.
9. Академический стиль главы 1, корректность ссылок на формулы, отсутствие расширения Scope
   (ML-нейросети, cross-cluster миграции).

## 4. Результаты сквозной проверки

### QG1-R01. Размерность углеродного следа — закрыто

Канон для реализации и главы 1:

$$
E_i=\frac{P_i\Delta t_i}{3.6\cdot10^6}\ [\mathrm{kWh}],
\qquad
CF=\sum_i E_iCI_i\ [\mathrm{gCO2e}],
$$

при \(P_i\) в W и \(\Delta t_i\) в s. Если время выражено в часах, применяется делитель \(1000\).
Обе записи эквивалентны. Формулы согласованы в T1.1, T1.2 §11, T1.3 §4.2 и в (1.1)–(1.2) главы 1.

Запись `SCOPE-IN-05` в governance по-прежнему сокращена (\(CF=\sum P_i\Delta t_i CI_i\)). Это
известное сжатие нормативного текста, а не противоречие канона Stage 2: глава 1 и research явно
вводят перевод в kWh. Перенос полного канона в SSOT — follow-up L1 (`QG1-FU-02`), не блокер
математического этапа.

### QG1-R02. Полная мощность узла и вклад нового Pod — закрыто

Два KPI разведены:

- полный \(P_n(u)\), включая idle, — итоговый `cluster energy` / `cluster CF`;
- при сравнении кандидатов — приращение
  \(\Delta\widehat P_{p,n}=P_n(u_n^+)-P_n(u_n^-)\) (IDLE-NONE).

T1.3 §4.2 и §6.2 приведены к тому же канону, что T1.2 §8 и формулы (1.4)–(1.7) главы 1.

### QG1-R03. SLA и Scheduler Framework — закрыто

Ресурсную допустимость обеспечивают стандартные `Filter`-плагины (`NodeResourcesFit` и др.).
Workload-level SLA

$$
t_{\mathrm{now}}+\widehat T_{\mathrm{class}(p)}\le t_{\mathrm{deadline},p}
$$

проверяется в `CarbonAware.PreScore` **до** carbon scoring. Нарушение даёт `fwk.Error`
(`SLA precondition failed`): попытка завершается до `Score`, Pod остаётся `Pending`, действует
стандартный backoff. Это fail-closed проверка конфигурации workload, а не temporal shifting и не
собственный `Filter`. Порядок `resource feasibility → SLA eligibility → carbon ranking` реализуется
внутри разрешённого набора `PreScore` + `Score` + `NormalizeScore` (`SCOPE-OUT-16` соблюдён).

### QG1-R04. Семантика carbon intensity — согласовано

- внутренняя единица — `gCO2e/kWh`;
- основной эксперимент — average consumption-based lifecycle intensity;
- `direct` и `lifecycle` не смешиваются в одном сравнении;
- результат называется estimated carbon footprint, не изменение marginal emissions;
- наблюдение действует на полуинтервале \([t_k,t_k+\Delta t)\);
- нет прогноза → persistence \(\widehat{CI}(t+\Delta)=CI(t)\);
- perfect foresight в зачётных прогонах запрещён.

### QG1-R05. Observation time и freshness — разведено

`timestamp` — начало интервала \(CI\); `fetchedAt` — момент обновления кэша. Freshness считается
по `fetchedAt`. Буквальный `SCOPE-EDGE-04`: старше `maxDataAgeSeconds` (300 с) → одинаковый
нейтральный итоговый балл `CarbonAware` всем узлам, `Success`, `fallback_total++`. Постоянный
вклад не меняет `argmax`. Двухпороговая схема T1.1 §8.2 остаётся рекомендацией L2 и **не
применяется**, пока L1 не примет SSOT Change Request.

### QG1-R06. Стратегия B: min CI, не min \(\widehat{CF}\) — закрыто

`SCOPE-IN-13` и инженерная спецификация §88 требуют наивный baseline «всегда выбирай самый
зелёный узел сейчас». T1.3 §7.5 ранее смешивал это с \(w_C=1\) на полном \(\widehat{CF}\).
Исправлено:

- **B** — текущий (persistence) average \(CI\) зоны, формула (1.12), `scoringMode: current-ci`;
- **C** — ожидаемый след (1.7) на горизонте \(\widehat T\).

Иначе на однородных энергопрофилях и почти постоянном \(CI\) стратегии B и C совпали бы, и
эксперимент не проверял бы вклад прогноза и энергомодели.

### QG1-R07. Точность Score — оговорена

Внутренняя fixed-point шкала уменьшает преждевременное округление, но итоговый контракт
Scheduler Framework ограничен 101 целым значением \([0,100]\). Stage 2 задаёт \(\varepsilon\),
\(S_{\mathrm{neutral}}\) и правило округления; частота ничьих проверяется тестом.

### QG1-R08. Версии Kubernetes и Go — не блокер Stage 2

Модули Kubernetes 1.37 требуют Go 1.26.0; текущие `go.mod` и governance §3.1 объявляют Go 1.23.
Это расхождение **toolchain**, а не Scheduler Framework. Архитектурная реализуемость подтверждена;
сборка `cmd/scheduler` требует follow-up `QG1-FU-01`. Математическое моделирование от этой правки
не зависит.

### QG1-R09. Устаревшие SSOT-заглушки — локализовано

`architecture.md` упоминает «откладывание»; `glossary.md` пишет `gCO₂/kWh` без `e`;
`interfaces.md` и `metrics.md` содержат TBD. По `CONF-03` приоритет у
[governance.md](../ssot/governance.md). Глава 1 и настоящий отчёт следуют governance и не
наследуют temporal shifting, обязательную CRD или extender.

## 5. Подтверждение технической реализуемости

Выбранный стек реализуем в Kubernetes Scheduler Framework в принятом Scope.

1. `CarbonAware` компилируется в `kube-scheduler` и регистрируется через `app.WithPlugin`.
2. Профиль `carbon-aware-scheduler` включается через `KubeSchedulerConfiguration`; baseline
   `default-scheduler` сохраняется в том же процессе.
3. `PreScore` вызывается один раз по списку feasible nodes, снимает immutable snapshot, проверяет
   SLA, рассчитывает \(\widehat{CF}\) или текущий \(CI\) (режим B), min/max и составляющие score.
4. Неизменяемое состояние записывается в `CycleState`.
5. Параллельный `Score` выполняет только lookup по имени узла; HTTP, диск и блокировки отсутствуют.
6. `NormalizeScore` приводит результат к \([0,100]\), включая защиту \(CF_{\max}=CF_{\min}\).
7. Carbon Data Service унифицирует Mock / Historical / Electricity Maps; live API — только демо.
8. При stale/missing/invalid данных всем узлам присваивается одинаковый итоговый нейтральный балл;
   рабочая нагрузка продолжает планироваться.
9. Kind-стенд с логическими energy zones достаточен для сравнительного исследования, если
   модельные ватты не выдаются за физическое измерение kind-node.
10. Линейная \(P(u)\) и persistence-прогноз вычислимы как чистые функции; ML и cross-cluster не
    требуются.

Утверждений о невозможности реализации в рамках Scheduler Framework нет и не допускается.

## 6. Проверка Scope

- ✅ один Kubernetes-кластер и первоначальный выбор узла;
- ✅ `PreScore` + `Score` + `NormalizeScore`;
- ✅ линейная калибруемая модель \(P(u)\);
- ✅ replay, persistence forecast и live API только для демонстрации;
- ✅ ConfigMap и labels/annotations;
- ✅ Prometheus/Grafana и Python только для анализа;
- ✅ отсутствуют обучаемые нейросетевые модели;
- ✅ отсутствуют межкластерное управление и миграция запущенных Pod;
- ✅ отсутствуют carbon-aware HPA, внешняя СУБД, брокер сообщений и scheduler extender;
- ✅ отсутствуют обещания конкретного процента снижения до эксперимента;
- ✅ глава 1 не расширяет Scope.

## 7. Проверка качества главы 1

Артефакт: [chapter_1_state_of_the_art.md](../diploma_text/chapter_1_state_of_the_art.md).

| Требование | Результат |
| --- | --- |
| Структура 1.1–1.4 | ✅ |
| Проблема → требования к планировщику без логического скачка | ✅ |
| Scheduler Framework, carbon data и power model связаны одной цепочкой | ✅ |
| Формулы (1.1)–(1.12) пронумерованы, ссылки согласованы | ✅ |
| Переменные и единицы определены | ✅ |
| Различены оценка / измерение, average / marginal, cluster CF / вклад Pod | ✅ |
| Стратегия B = min текущей \(CI\), стратегия C = \(\widehat{CF}\) | ✅ |
| Существующие решения сопоставлены по управляющему механизму | ✅ |
| Цель, объект, предмет, задачи, гипотеза сформулированы | ✅ |
| Техническая реализуемость объяснена без расширения Scope | ✅ |
| Внешние численные сведения снабжены источником | ✅ |
| Нет экспериментальных чисел проекта, `run_id` не применим | ✅ |
| Академический стиль, без «впервые в мире» | ✅ |

## 8. Применимый DoD

| ID | Статус |
| --- | --- |
| `DOD-DOC-05` | ✅ единицы W, s, kWh, `gCO2e/kWh`, gCO2e указаны |
| `DOD-DOC-06` | ✅ формулы в LaTeX, переменные расшифрованы |
| `DOD-DOC-07` | ✅ внутренние ссылки относительные, внешние источники имеют URL |
| `DOD-DOC-08` | ✅ русский язык, технические идентификаторы сохранены |
| `DOD-DOC-09` | ✅ будущие результаты обозначены как гипотеза |
| `DOD-DOC-10` | ✅ не применим: собственных прогонов в главе нет |
| `DOD-DOC-11` | ✅ настоящий отчёт |
| `SCOPE-OUT-*` | ✅ нарушений не обнаружено |
| `DOD-DOC-04` | ⚠ термины определены локально; канон в `glossary.md` — follow-up L1 |

## 9. Что исправлено на Gate 1

| Было | Стало |
| --- | --- |
| T1.3 scoring по полной \(P(u)\) с idle | маржинальное \(\Delta\widehat P\) для \(S_C\); полная \(P\) — cluster KPI |
| T1.3 стратегия B как \(w_C=1\) на \(\widehat{CF}\) | B = текущий \(CI\), `scoringMode: current-ci` |
| T1.3 без ссылок на T1.1/T1.2 | взаимные ссылки в §4.2 и шапке |
| T1.2 делитель только `/1000` (часы) | явно эквивалентен \(3.6\cdot10^6\) при \(\Delta t\) в секундах |
| T1.1 не фиксировал роль CI в стратегиях A/B/C | §10: B — текущий \(CI\), C — ряд на горизонте \(\widehat T\) |
| URL IEA с опечаткой регистра | `energy-demand-from-ai` |

Независимый сквозной просмотр initially фиксировал условный FAIL по пунктам K1–K6. После
унификации формул и семантики B/C эти пункты закрыты. Оставшиеся K-пункты (glossary, interfaces,
Go 1.26) вынесены в §10 как неблокирующие.

## 10. Follow-up, не блокирующие Stage 2

### QG1-FU-01. Синхронизировать toolchain до реализации

- **Затронуто:** `docs/ssot/governance.md` §3.1 и `go.mod`.
- **Факт:** Kubernetes 1.37 modules требуют Go 1.26.0.
- **Действие L1:** зафиксировать patch-версию Kubernetes, Go 1.26.x и digest kind node image.
- **Срок:** до начала реализации `cmd/scheduler`.

### QG1-FU-02. Синхронизировать малые SSOT-документы

- **Затронуто:** `architecture.md`, `interfaces.md`, `metrics.md`, `experiment-protocol.md`,
  `glossary.md`; сжатая запись `SCOPE-IN-05`.
- **Действие L1:** перенести контракты Gate 1, единица `gCO2e/kWh`, модель `CarbonRecord`,
  удалить «откладывание» из architecture, не вводя новых механизмов.

### QG1-FU-03. Зафиксировать параметры на Stage 2

Предмет следующего этапа, не пробел Stage 1:

- точная формула expected utilization нового Pod;
- таблица `workload class → expected runtime`;
- численные примеры fail-closed SLA;
- определения \(S_R\) и \(S_P\);
- \(S_{\mathrm{neutral}}\), \(\varepsilon\), правило округления;
- базовые \(P_{\mathrm{idle}},P_{\max}\) и сетка sensitivity;
- пересечение runtime с сеткой \(CI(t)\);
- явный framework weight `CarbonAware`.

### QG1-FU-04. Freshness: один или два порога

До решения L1 действует буквальный `SCOPE-EDGE-04` (300 с). Рекомендация T1.1 о окне stale
300–1800 с не применяется.

## 11. Передача в Stage 2

Обязательный вход математического этапа:

1. \(P_n(u)=P_{\mathrm{idle},n}+(P_{\max,n}-P_{\mathrm{idle},n})u\);
2. \(\Delta\widehat P_{p,n}=P_n(u_n^+)-P_n(u_n^-)\) для candidate scoring;
3. полный \(P_n(u)\) для итогового `cluster CF`;
4. размерностно корректная дискретная сумма \(CF\) (1.1)–(1.2);
5. average consumption-based lifecycle \(CI(t)\), piecewise-constant;
6. persistence fallback при отсутствии forecast;
7. workload-level SLA (1.3) до carbon scoring;
8. min-max carbon score с нейтральной ветвью (1.9);
9. \(S=w_CS_C+w_RS_R+w_PS_P\), \(\sum w_i=1\);
10. стратегия B — формула (1.12), не (1.7);
11. fail-open поведение при недоступных или просроченных данных.

## 12. Формальное решение Gate

**QUALITY GATE 1: PASS.**

**Stage 1 завершён. Stage 2 (Mathematical Modeling) разблокирован.**

Следующий агент вправе формализовать математическую модель по входу §11, не расширяя Scope.
