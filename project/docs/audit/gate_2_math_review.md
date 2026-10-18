# Quality Gate 2 — аудит математической модели

- **Дата:** 2026-09-04
- **Роль:** Chief Architect & Head of Research
- **Статус:** **PASS**
- **Решение:** **Stage 3 — UNLOCKED**
- **Проверяемый переход:** Stage 2 Mathematical Modeling → Stage 3 Architectural Design
- **Канон:** [`02_math_model_ssot.md`](../ssot/02_math_model_ssot.md) v1.0.0 **LOCKED**
- **ADR:** [`001_mathematical_model_approval.md`](../decisions_adr/001_mathematical_model_approval.md) — accepted

## 1. Итоговый вердикт

Математическая модель углеродо-ориентированного планирования полностью формализована, независимо
верифицирована и зафиксирована как единственный нормативный источник формул.

```text
P(u), clip(u)
    → маржинальная мощность пода (IDLE-NONE)
    → η · Σ P_pod · Δt · CI(t)  →  CF̂(p,n)  [gCO2e]
    → hard SLA в PreScore
    → S_C, S_R, S_P ∈ [0,1] с предохранителями G-01…G-21
    → Score ∈ [0,100]
    → Score lookup O(1)
```

Контрольный повтор валидации T2.2 (seed `42`): `verdict=PASS checks=48 failed=0`. Блокирующих
замечаний по краевым случаям нет. Формулы (2.1)–(2.23) утверждены ADR-001 и заблокированы:
изменение без нового ADR запрещено.

Количество блокеров Stage 3: **0**.

**QUALITY GATE 2: PASS. Stage 3 (Architectural Design) разблокирован.**

## 2. Проверенные материалы

| ID | Файл | Роль |
| --- | --- | --- |
| T2.1 | [02_math_model_ssot.md](../ssot/02_math_model_ssot.md) | нормативные формулы |
| T2.1 | [T2.1-math-model-ssot.md](T2.1-math-model-ssot.md) | отчёт формализации |
| T2.2 | [math_model_validation_report.md](../research/math_model_validation_report.md) | независимая валидация |
| T2.2 | [T2.2-math-model-validation.md](T2.2-math-model-validation.md) | отчёт L2 |
| T2.2 | `analysis/scripts/validate_math_model.py` | оракулы и G-* |
| — | [engineering-specification.md](../ssot/engineering-specification.md) §§7–24, §104, итоговая модель | входной канон (`CONF-05`) |
| — | [governance.md](../ssot/governance.md) | Scope, DoD, SSOT |
| — | [chapter_1_state_of_the_art.md](../diploma_text/chapter_1_state_of_the_art.md) (1.3)–(1.12) | постановка |
| QG1 | [gate_1_research_review.md](gate_1_research_review.md) §11 | вход Stage 2 |
| L1 | [chapter_2_mathematical_model.md](../diploma_text/chapter_2_mathematical_model.md) | черновик главы 2 |
| L1 | ADR-001 | утверждение и lock |

Дополнительно сверены `glossary.md`, `architecture.md` (заглушка T0.2 — не канон формул),
`results.json` (48 проверок).

## 3. Критерии аудита

1. Размерностная замкнутость \(P\), \(E\), \(CF\), \(S_{\bullet}\), \(\operatorname{Score}\).
2. Соответствие формул `SCOPE-IN-04`…`09` и входу Gate 1 §11.
3. Устранение всех замечаний по краевым случаям (G-01…G-21, \(0/0\), hard SLA \(\neq\) `Score=0`).
4. Различение cluster \(CF\) и \(\widehat{CF}(p,n)\) (IDLE-NONE).
5. Операциональное различие стратегий B и C.
6. \(O(NK)\) в `PreScore`, \(O(1)\) в `Score`.
7. Явная фиксация `CONF-05` относительно инженерной спецификации.
8. Полное соответствие главы 2 утверждённому SSOT.
9. Наличие ADR и режима LOCKED для последующих агентов разработки.

## 4. Соответствие формул спецификации и главе 1

| Норма / формула | SSOT | Вердикт |
| --- | --- | --- |
| `SCOPE-IN-04` \(P(u)\) | (2.2) | PASS; добавлен \(\operatorname{clip}\) |
| `SCOPE-IN-05` дискретная сумма \(CF\) | (2.9), (2.10) с \(\eta\) | PASS; сжатая запись §12 без \(\eta\) **не** переносится в код (ADR-001) |
| `SCOPE-IN-06` runtime-таблица | (2.12) | PASS |
| `SCOPE-IN-07` \(S=w_CS_C+w_RS_R+w_PS_P\), \([0,100]\) | (2.19)–(2.21) | PASS |
| `SCOPE-IN-08` hard constraints до carbon | (2.13), G-12 | PASS; псевдокод §26 `return 0` отклонён |
| `SCOPE-IN-09` fallback | G-06…G-10 | PASS |
| `SCOPE-IN-13` стратегия B | (2.23) | PASS; не \(w_C=1\) на (2.10) |
| `SCOPE-EDGE-02/03/04/07` | G-09, (2.11), G-08, \(w_K=0\) | PASS |
| `SCOPE-OUT-04/16/17/18` | persistence; SLA в `PreScore`; `Score` \(O(1)\); нет сети/storage/embodied | PASS |
| Глава 1 (1.3) | (2.13a) | тождество |
| Глава 1 (1.4)–(1.7) | (2.2), (2.3), (2.4), (2.10s) | PASS; \(\operatorname{clip}\) и \(\eta\) явные |
| Глава 1 (1.8)–(1.12) | (2.11), (2.15), (2.19), (2.22), (2.23) | PASS; \(S_C^{(1.9)}=100\cdot S_C^{(2.15)}\) |
| Спецификация §21 экспонента \(S_P\) | не в runtime | PASS; линейный slack (2.18) |
| Спецификация §22 \(S_K\) | не в (2.19) | PASS |
| Спецификация §23 веса | default \((0{,}6,0{,}2,0{,}2)\) | PASS; prose \(w_C=0{,}5\) не канон |

QG1-FU-03 закрыт: expected utilization (2.3), \(\hat T\) (2.12), численные примеры SLA, \(S_R\) и
\(S_P\), \(S_{\mathrm{neutral}}\) / \(\varepsilon\) / округление, \(P_{\mathrm{idle}},P_{\max}\) и
sensitivity, пересечение runtime с сеткой \(CI\), различение \(w_C\) и \(w_{\mathrm{CA}}^{\mathrm{fw}}\).

## 5. Краевые случаи

Независимый прогон и повтор L1:

```text
python analysis/scripts/validate_math_model.py --output-dir docs/research/figures/math_model_validation --seed 42
# seed=42  verdict=PASS checks=48 failed=0
```

| Требование | Результат |
| --- | --- |
| Тождество \(\eta\) (часы ↔ секунды), оракул \(3{,}6\,\mathrm{gCO_2e}\) | PASS |
| \(P,E,CF\ge 0\) на домене (2.2a) | PASS |
| \(S_C,S_R,S_P\in[0,1]\), \(\operatorname{Score}\in[0,100]\) | PASS |
| Нет \(0/0\) (G-02, G-04, G-14) | PASS; \(S_C^{\mathrm{neutral}}=1/2\), не \(1\) |
| Hard SLA \(\neq\) `Score=0` | PASS, `SLAPreconditionFailed` |
| G-08 гасит итоговый Score обоим узлам | PASS, оба \(=50\) |
| B \(\neq\) C на волатильном \(CI\) | PASS, 4 горизонта |
| `Score` lookup \(O(1)\) по \(K\) | PASS |
| Оракулы 14.1–14.7 | PASS |
| \(\hat T=T_{\mathrm{deadline}}\) eligible, \(S_P=0\); \(+\varepsilon\) — hard-cut | PASS |

Блокирующих SSOT Change Request нет. Неблокирующие замечания T2.2 (§11 отчёта валидации)
подтверждаются и **не** открывают права на правку формул: относительный \(\varepsilon_{CF}\) —
будущий ADR; скачок \(S_C\) — комментарий в `pkg/scheduler`; sweep весов через
\(w_C=0{,}7\,w_R\); freeze Python 3.11 — toolchain анализа (прогон выполнен на 3.13.3; на формулы
не влияет).

## 6. Соответствие главы 2 утверждённому SSOT

Артефакт: [chapter_2_mathematical_model.md](../diploma_text/chapter_2_mathematical_model.md).

Все нумерованные формулы SSOT присутствуют в главе 2 с теми же тегами и тем же LaTeX-телом:

| Тег | Глава 2 | SSOT |
| --- | --- | --- |
| (2.1), (2.1a), (2.1b) | §2.1.3 | §2.4 |
| (2.2), (2.2a), (2.3), (2.3a), (2.4), (2.4a), (2.4b) | §2.2 | §§3–4 |
| (2.5)–(2.11) | §2.3 | §§5–6 |
| (2.12), (2.13), (2.13a) | §2.4 | §7 |
| (2.14)–(2.22) | §2.5 | §§8–9 |
| (2.23) | §2.6 | §9.4 |
| G-01…G-21 | §2.7.1 | §10 |
| параметры по умолчанию | §2.8 | §13 |
| эталоны 14.1–14.7 | §2.8.1–2.8.7 | §14 |

Расхождений в правых частях уравнений, доменах, единицах и предохранителях не обнаружено.
Редакционные отличия главы (академическая композиция, ссылки на (1.x), отсутствие Go-таблицы
исключений T2.2 §10) не меняют математический объект.

| Требование к главе | Результат |
| --- | --- |
| Структура 2.1–2.9, объект → формулы → предохранители → эталоны → выводы | ✅ |
| Формулы (2.1)–(2.23) пронумерованы, ссылки согласованы | ✅ |
| Переменные и единицы определены | ✅ |
| Различены оценка / измерение, average / marginal, cluster CF / вклад Pod | ✅ |
| Стратегия B = текущая \(CI\), стратегия C = \(\widehat{CF}\) | ✅ |
| Нет расширения Scope (ML, federation, HPA, extender) | ✅ |
| Эталоны помечены как расчёт модели, не как `run_id` эксперимента | ✅ |
| Академический стиль, без «впервые в мире» | ✅ |

## 7. Проверка Scope и DoD

### Scope

- ✅ линейная калибруемая \(P(u)\), дискретная сумма \(CF\) с \(\eta\);
- ✅ таблица класса → runtime; hard SLA до carbon scoring;
- ✅ \(S=w_CS_C+w_RS_R+w_PS_P\), \(\sum w_i=1\), нормализация в \([0,100]\);
- ✅ fallback fresh → persistence → нейтральный Score;
- ✅ B = min текущей \(CI\); C = ожидаемый след;
- ✅ только `PreScore` / `Score` / `NormalizeScore`;
- ✅ нет ML-прогноза, embodied/network/storage carbon, \(S_K\), temporal shifting;
- ✅ глава 2 не расширяет Scope.

### Применимый DoD

| ID | Статус |
| --- | --- |
| `DOD-DOC-01` | ✅ SSOT утверждён, TBD в каноне формул нет |
| `DOD-DOC-02` | ✅ ADR-001 по шаблону, все секции заполнены |
| `DOD-DOC-03` | ✅ индекс ADR обновлён |
| `DOD-DOC-04` | ✅ термины по `glossary.md`; lock-ссылка на SSOT |
| `DOD-DOC-05` | ✅ W, s/h, kWh, `gCO2e/kWh`, gCO2e |
| `DOD-DOC-06` | ✅ LaTeX, переменные расшифрованы |
| `DOD-DOC-07` | ✅ относительные ссылки |
| `DOD-DOC-08` | ✅ русский язык, идентификаторы без перевода |
| `DOD-DOC-09` | ✅ эффект A/B/C и contention \(S_R\) — гипотезы |
| `DOD-DOC-10` | ✅ н/п: в главе 2 нет экспериментальных `run_id`; эталоны — расчёт модели |
| `DOD-DOC-11` | ✅ настоящий отчёт |
| `SSOT-01` | ✅ статус `accepted`; L2/L3 не вправе править формулы |
| `GO-14` | ✅ каталог G-01…G-21 закрыт T2.2 |
| `SCOPE-OUT-*` | ✅ нарушений не обнаружено |

Код, runtime-тесты и эксперименты стенда не требовались (DOD-CODE / DOD-TEST runtime / DOD-EXP —
н/п до Stage 3+). Оракулы §14 остаются обязательным входом будущих unit-тестов (`DOD-TEST-03/04`).

## 8. Что утверждено и что отклонено

| Решение | Статус |
| --- | --- |
| Явный \(\eta\); runtime (2.10s) | утверждено |
| IDLE-NONE для \(\widehat{CF}(p,n)\) | утверждено |
| \(S_C,S_R,S_P\) на \([0,1]\), затем \(\times 100\) | утверждено |
| \(S_C^{\mathrm{neutral}}=1/2\) при вырождении | утверждено |
| Линейный slack \(S_P\); hard-cut в `PreScore` | утверждено |
| Default-веса \((0{,}6,0{,}2,0{,}2)\) | утверждено |
| Стратегия B = (2.23) | утверждено |
| Сжатая сумма без \(\eta\) | отклонено для кода |
| Экспонента §21 | отклонено для runtime |
| `Score=0` как SLA-cut | отклонено |
| \(S_K\) в каноне | отклонено |
| \(S_C=1\) на вырождении | отклонено |
| B = \(w_C=1\) на \(\widehat{CF}\) | отклонено |
| \(\gamma\neq 1\) или node-dependent \(\hat T\) в runtime | отклонено (нужен ADR) |

## 9. Follow-up, не блокирующие Stage 3

### QG2-FU-01. Реализация чистых функций по оракулам

- **Затронуто:** `pkg/energy`, `pkg/carbon`, scoring в `pkg/scheduler`.
- **Действие:** табличные тесты 14.1–14.7 и G-04, G-08, 14.6, тождество (2.10h)/(2.10s), B≠C при
  \(\hat T=5400\,\mathrm{s}\). Комментарий о скачке \(S_C\) на \(\varepsilon_{CF}\).
- **Срок:** вместе с первой реализацией модели.

### QG2-FU-02. Малые SSOT-заглушки архитектуры (перенос QG1-FU-02)

- **Затронуто:** `architecture.md`, `interfaces.md`, `metrics.md`, `experiment-protocol.md`.
- **Факт:** TBD и устаревшее «откладывание» в `architecture.md` не являются каноном формул
  (`CONF-03`: math SSOT выше architecture).
- **Действие:** заполнить на Stage 3, не вводя механизмов вне Scope.
- **Срок:** Stage 3.

### QG2-FU-03. Toolchain Go / Python (перенос QG1-FU-01 и замечания T2.2)

- **Затронуто:** `go.mod` (Go 1.23 vs Kubernetes 1.37 → Go 1.26); `analysis/requirements.txt`
  (прогон на Python 3.13.3 при норме PY-01 = 3.11).
- **Действие:** отдельный инфраструктурный ADR до сборки `cmd/scheduler` и freeze анализа.
- **На формулы не влияет.**

### QG2-FU-04. Относительный порог \(\varepsilon_{CF}\)

Только отдельным ADR, если понадобится «практическая неразличимость» зон. Канон runtime не
меняется.

### QG2-FU-05. Sweep весов в экспериментальном протоколе

Карточка сценария обязана включать точку ниже линии \(w_C=0{,}7\,w_R\) на конфликтном кластере,
иначе \(S_R\) эмпирически невидим. Это требование протокола, не модели.

## 10. Передача в Stage 3

Архитектор и реализация **обязаны** использовать только заблокированный SSOT. Вход:

1. \(P_n(u)=(2.2)\), \(u_n^{\pm}=(2.3)\), \(\widehat{P}_{\mathrm{pod}}=(2.4)\);
2. \(\widehat{CF}(p,n)=(2.10s)\) в runtime, \(\eta\) обязателен;
3. persistence (2.11); hard SLA (2.13) в `PreScore` Error, не `Score=0`;
4. \(S_C,S_R,S_P\) — (2.15), (2.17), (2.18); \(\operatorname{Score}\) — (2.19);
5. стратегия B — (2.23); G-08 гасит **итоговый** Score;
6. `Score` = \(O(1)\) lookup; суммирование по \(K\) только в `PreScore`;
7. эталоны §14 — оракул unit-тестов;
8. изменение любой формулы — новый ADR, затем SSOT, затем код.

Компонентная схема, sequence diagram, контракты REST Carbon Data Service и раскладка пакетов —
предмет Stage 3. Они не вправе переопределять (2.1)–(2.23).

## 11. Формальное решение Gate

**QUALITY GATE 2: PASS.**

**Stage 2 завершён. Математическая модель заблокирована (SSOT Lock). Stage 3 (Architectural Design) разблокирован.**

Следующий агент вправе проектировать архитектуру компонентов и контракты по входу §10, не изменяя
утверждённые формулы и не расширяя Scope.
