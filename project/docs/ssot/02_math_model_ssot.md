# Математическая модель Carbon-Aware Kubernetes Scheduler

- **Версия:** 1.0.0
- **Статус:** accepted (LOCKED)
- **Дата утверждения:** 2026-09-04
- **Владелец:** Chief Architect & Head of Research
- **Уровень изменяемости:** L1; формулы **заблокированы** ADR-001
- **Основание:** [governance.md](governance.md) (`SCOPE-IN-04`…`09`, `SCOPE-OUT-04`, `SCOPE-OUT-16`…`18`, `SCOPE-EDGE-02`…`04`, `SCOPE-EDGE-07`, `GO-11`…`14`); [engineering-specification.md](engineering-specification.md) §§7–24, §41–44 (уточнения — ADR-001, `CONF-05`); [power_models_state_of_the_art.md](../research/power_models_state_of_the_art.md); [carbon_intensity_datasets.md](../research/carbon_intensity_datasets.md); [scheduler_framework_analysis.md](../research/scheduler_framework_analysis.md); глава 1, формулы (1.3)–(1.12); Quality Gate 1 §11; T2.1 v0.1.0 draft; [math_model_validation_report.md](../research/math_model_validation_report.md); ADR [`001_mathematical_model_approval.md`](../decisions_adr/001_mathematical_model_approval.md); Quality Gate 2
- **Следующий потребитель:** Stage 4 / Stage 5 (реализация чистых функций и плагина); пакет архитектуры заморожен Quality Gate 3

> **LOCKED / SSOT Freeze.** Настоящий файл — единственный нормативный источник формул (2.1)–(2.23) и предохранителей G-01…G-21. Содержание формул тождественно draft v0.1.0; изменены только статус, шапка и §18. Агенты разработки (L2/L3) **не вправе** изменять формулы, предохранители, эталоны §14 и нормативные параметры §13. Любое изменение канона требует **нового ADR** со статусом `accepted`, затем правки этого файла уровнем L1 (`SSOT-01`, `SSOT-04`, `VER-18`, ADR-001). Расхождение кода с настоящим файлом разрешается в пользу канона.

Документ является **единственным нормативным источником формул** для `pkg/energy`, `pkg/carbon` и scoring в `pkg/scheduler`.

Утверждения о численном эффекте стратегий A/B/C на стенде помечены как гипотеза (`DOD-DOC-09`).

---

## 0. Ключевая система уравнений

На одном scheduling cycle для пода \(p\) и кандидата \(n\) канон сводится к системе

$$
\begin{aligned}
u_n^{-}
&=
\operatorname{clip}
\Bigl(\tfrac{R_{\mathrm{req},n}}{R_{\mathrm{alloc},n}},\,0,\,1\Bigr),
&
u_n^{+}
&=
\operatorname{clip}
\Bigl(\tfrac{R_{\mathrm{req},n}+R_p}{R_{\mathrm{alloc},n}},\,0,\,1\Bigr),
\\[0.6em]
P_n(u)
&=
P_{\mathrm{idle},n}+(P_{\max,n}-P_{\mathrm{idle},n})\,u,
&
\widehat{P}_{\mathrm{pod}}(p,n)
&=
P_n(u_n^{+})-P_n(u_n^{-}),
\\[0.6em]
\widehat{CF}(p,n)
&=
\eta
\sum_{k=1}^{K}
\widehat{P}_{\mathrm{pod}}(p,n,t_k)\,
\Delta t_k\,
\widehat{CI}\bigl(z(n),t_k\bigr),
\\[0.6em]
S_C(n)
&=
1-
\frac{\widehat{CF}(p,n)-CF_{\min}(p)}{CF_{\max}(p)-CF_{\min}(p)},
&
S_R(n)
&=
1-U_{\mathrm{node}}(p,n),
\\[0.6em]
S_P(n)
&\text{ — slack при }\hat T(p,n)\le T_{\mathrm{deadline}}(p),\quad
\text{иначе hard-отсечение},
\\[0.6em]
\operatorname{Score}(n)
&=
100\cdot\bigl(w_C S_C(n)+w_R S_R(n)+w_P S_P(n)\bigr),
&
w_C+w_R+w_P&=1.
\end{aligned}
$$

Коэффициент \(\eta\) **обязателен**: без него произведение \(P\cdot\Delta t\cdot CI\) не имеет размерности \(\mathrm{gCO_2e}\). Частные скоринги лежат в \([0,1]\); \(\operatorname{Score}(n)\in[0,100]\). Фаза `Score` планировщика выполняет только чтение готового значения: \(O(1)\) на узел.

---

## 1. Назначение, объект и границы

### 1.1. Что задаёт модель

Модель отвечает на вопрос: **какой из ресурсно допустимых узлов \(n\) минимизирует ожидаемый углеродный след выполнения пода \(p\) на горизонте \(\hat T\), при соблюдении SLA и ресурсного предпочтения.**

Она задаёт:

1. оценочную мощность логического узла и маржинальную мощность пода;
2. энергию и углеродный след в непрерывной и дискретной формах;
3. частные скоринги \(S_C,S_R,S_P\) и интегральную полезность \(\operatorname{Score}\);
4. предохранители всех краевых случаев, возникающих в арифметике и в данных.

### 1.2. Два различных объекта \(CF\) (не смешивать)

| Объект | Обозначение | Idle | Когда считается | Роль |
| --- | --- | --- | --- | --- |
| Маржинальный ожидаемый след пода | \(\widehat{CF}(p,n)\) | **не входит** (политика IDLE-NONE) | ex-ante, в `PreScore` | аргумент \(S_C\) |
| След узла / кластера | \(CF_n\), \(CF_{\mathrm{cluster}}\) | **входит** через \(P_n(u)\) | ex-post, после прогона | главный экспериментальный KPI |

Запрещённая интерпретация: «\(\widehat{CF}(p,n)\) есть измеренные выбросы пода». Допустимая: «\(\widehat{CF}(p,n)\) есть оценочный маржинальный вклад размещения \(p\) на уже включённый узел \(n\)».

### 1.3. Что модель сознательно не содержит

Не вводятся нелинейные дифференциальные уравнения состояния, DVFS-динамика, очередь задач внутри узла, тепловая модель, сеть, storage, GPU, embodied carbon, обучаемый прогноз \(CI\) (`SCOPE-OUT-04`, `SCOPE-OUT-18`). Функции \(P(u)\) и \(CI(t)\) — кусочно-постоянные либо аффинные; интеграл всегда сводится к конечной сумме. Cost-слагаемое \(S_K\) не входит в канон (\(w_K=0\), `SCOPE-EDGE-07`).

---

## 2. Нотация, множества и единицы

### 2.1. Соглашения

- Шапка \(\widehat{\,\cdot\,}\) — оценка / прогноз (ex-ante). Без шапки — факт после прогона (ex-post) либо калиброванный параметр.
- Время модельное, не wall-clock replay (`REPRO-03`).
- Все физические величины несут размерность (`DOD-DOC-05`, `GO-11`). Безразмерные величины имеют размерность \(1\).
- \(\operatorname{clip}(x,a,b):=\min\{b,\max\{a,x\}\}\).

### 2.2. Множества и индексы

| Символ | Смысл |
| --- | --- |
| \(\mathcal{N}\) | множество worker-узлов кластера |
| \(\mathcal{N}_{\mathrm{feas}}(p)\) | узлы, прошедшие стандартный Kubernetes `Filter` |
| \(\mathcal{N}_{\mathrm{SLA}}(p)\) | узлы \(\mathcal{N}_{\mathrm{feas}}\), удовлетворяющие hard SLA |
| \(\mathcal{N}^{\star}(p)\) | узлы, по которым считается \(\operatorname{Score}\); канон: \(\mathcal{N}^{\star}=\mathcal{N}_{\mathrm{SLA}}\) |
| \(p\) | планируемый Pod |
| \(n\) | узел-кандидат |
| \(z(n)\) | энергетическая зона узла (label `carbon-aware.io/zone`) |
| \(k=1,\ldots,K\) | индекс куска горизонта выполнения, пересечённого с сеткой \(CI\) |

Если \(\mathcal{N}_{\mathrm{feas}}(p)=\varnothing\), настоящая модель не вызывается: планировщик возвращает `Unschedulable` стандартными средствами.

### 2.3. Размерностный словарь

| Символ | Смысл | Размерность | Каноническая единица |
| --- | --- | --- | --- |
| \(t\), \(t_{\mathrm{now}}\), \(t_{\mathrm{deadline}}\) | момент времени | \(\mathrm{T}\) | \(\mathrm{s}\) (Unix UTC) |
| \(\hat T(p,n)\), \(T_{\mathrm{deadline}}(p)\), \(\Delta t_k\) | длительность | \(\mathrm{T}\) | \(\mathrm{s}\) в runtime; \(\mathrm{h}\) — допустимая аналитическая форма |
| \(R_{\mathrm{req},n}\), \(R_p\), \(R_{\mathrm{alloc},n}\) | CPU request / allocatable | \(1\) | millicores |
| \(M_{\mathrm{req},n}\), \(M_p\), \(M_{\mathrm{alloc},n}\) | RAM request / allocatable | \(\mathrm{M}\) | byte (или MiB; **одна** единица в формуле) |
| \(u\), \(u_n^{\pm}\) | CPU-утилизация | \(1\) | доля в \([0,1]\) |
| \(U_{\mathrm{node}}\) | сводная утилизация узла | \(1\) | доля в \([0,1]\) |
| \(P_n(u)\), \(\widehat{P}_{\mathrm{pod}}\) | мощность | \(\mathrm{M}\mathrm{L}^{2}\mathrm{T}^{-3}\) | \(\mathrm{W}\) |
| \(P_{\mathrm{idle},n}\), \(P_{\max,n}\) | калибруемые параметры мощности | \(\mathrm{W}\) | \(\mathrm{W}\) |
| \(E\), \(\widehat{E}\) | энергия | \(\mathrm{M}\mathrm{L}^{2}\mathrm{T}^{-2}\) | \(\mathrm{kWh}\) |
| \(CI\), \(\widehat{CI}\) | average consumption-based lifecycle intensity | \(\mathrm{gCO_2e}\,\mathrm{kWh}^{-1}\) | `gCO2e/kWh` |
| \(CF\), \(\widehat{CF}\) | углеродный след | \(\mathrm{gCO_2e}\) | `gCO2e` |
| \(\eta\) | перевод \(P\cdot\Delta t\to E\) | \(\mathrm{kWh}\,\mathrm{W}^{-1}\,\mathrm{T}^{-1}\) | см. (2.1) |
| \(S_C,S_R,S_P,w_C,w_R,w_P,\lambda\) | скоринги и веса | \(1\) | безразмерные |
| \(\operatorname{Score}\) | полезность плагина | \(1\) | \([0,100]\) |
| \(\varepsilon_{CF}\) | порог вырождения разброса \(CF\) | \(\mathrm{gCO_2e}\) | `gCO2e` |

RAM входит **только** в \(U_{\mathrm{node}}\) (предпочтение), не в \(P(u)\).

### 2.4. Коэффициент перевода энергии

$$
\eta
:=
\begin{cases}
10^{-3}\,\mathrm{kWh}\,\mathrm{W}^{-1}\,\mathrm{h}^{-1},
& \text{если }\Delta t\text{ задан в }\mathrm{h},\\[0.4em]
\dfrac{1}{3.6\cdot 10^{6}}\,\mathrm{kWh}\,\mathrm{W}^{-1}\,\mathrm{s}^{-1},
& \text{если }\Delta t\text{ задан в }\mathrm{s}.
\end{cases}
\tag{2.1}
$$

Обе ветви тождественны: \(1\,\mathrm{h}=3600\,\mathrm{s}\) и \(1\,\mathrm{kWh}=3.6\cdot 10^{6}\,\mathrm{J}\). **Runtime-канон Scheduler Framework использует секунды.** Аналитические примеры ниже допускают часы, если \(\eta=10^{-3}\).

Проверка приёмки (часовая форма):

$$
[\mathrm{W}]\cdot[\mathrm{h}]\cdot[\mathrm{gCO_2e}/\mathrm{kWh}]\cdot 10^{-3}
=
[\mathrm{kWh}]\cdot[\mathrm{gCO_2e}/\mathrm{kWh}]
=
[\mathrm{gCO_2e}].
\tag{2.1a}
$$

Проверка (секундная форма):

$$
[\mathrm{W}]\cdot[\mathrm{s}]\cdot[\mathrm{gCO_2e}/\mathrm{kWh}]\cdot(3.6\cdot 10^{6})^{-1}
=
[\mathrm{gCO_2e}].
\tag{2.1b}
$$

Формула без \(\eta\) (как сжатая запись `SCOPE-IN-05` и инженерной спецификации §12) **размерностно не замкнута** и в реализации запрещена.

---

## 3. Модель мощности узла

### 3.1. Аффинный оценщик

Для узла \(n\) с CPU-утилизацией \(u\in\mathbb{R}\)

$$
P_n(u)
=
P_{\mathrm{idle},n}
+
\bigl(P_{\max,n}-P_{\mathrm{idle},n}\bigr)\,
\operatorname{clip}(u,0,1),
\qquad
[P_n]=\mathrm{W}.
\tag{2.2}
$$

Параметры калибруются вне цикла планирования (CAL-A/B/C, research T1.2) и читаются из конфигурации (`GO-12`). Домен:

$$
0\le P_{\mathrm{idle},n}\le P_{\max,n}
<+\infty.
\tag{2.2a}
$$

Нарушение (2.2a) — ошибка конфигурации: плагин **не стартует** (fail-closed по конфигу, не по данным).

Следствия при (2.2a):

- \(P_n(0)=P_{\mathrm{idle},n}\), \(P_n(1)=P_{\max,n}\);
- \(P_n(u)\in[P_{\mathrm{idle},n},P_{\max,n}]\) для любого \(u\in\mathbb{R}\) благодаря \(\operatorname{clip}\);
- тождество Beloglazov: \(P_n(u)=k_n P_{\max,n}+(1-k_n)P_{\max,n}\operatorname{clip}(u,0,1)\) при \(k_n=P_{\mathrm{idle},n}/P_{\max,n}\) и \(P_{\max,n}>0\).

Величина \(P_n(u)\) есть **оценка** логического сервера энергозоны, а не показание датчика kind-node (`SCOPE-OUT-15`, LIM-01).

### 3.2. Прогнозируемая утилизация до и после размещения

Пусть \(R_{\mathrm{req},n}\) — суммарный CPU request на \(n\) по снимку планировщика (включая assumed Pod), millicores; \(R_p\) — CPU request пода \(p\); \(R_{\mathrm{alloc},n}\) — allocatable CPU.

$$
u_n^{-}
=
\operatorname{clip}
\Bigl(
\frac{R_{\mathrm{req},n}}{R_{\mathrm{alloc},n}},\,0,\,1
\Bigr),
\qquad
u_n^{+}
=
\operatorname{clip}
\Bigl(
\frac{R_{\mathrm{req},n}+R_p}{R_{\mathrm{alloc},n}},\,0,\,1
\Bigr),
\tag{2.3}
$$

при условии \(R_{\mathrm{alloc},n}>0\). Размерность: числитель и знаменатель — millicores, отношение безразмерно.

Стандартный `NodeResourcesFit` гарантирует \(R_{\mathrm{req},n}+R_p\le R_{\mathrm{alloc},n}\) для \(n\in\mathcal{N}_{\mathrm{feas}}\). \(\operatorname{clip}(\cdot,0,1)\) остаётся математическим предохранителем на случай \(u>1\) (гонка, обход Filter, ошибка снимка).

Аналогично для RAM (только для \(U_{\mathrm{node}}\), не для \(P\)):

$$
v_n^{+}
=
\operatorname{clip}
\Bigl(
\frac{M_{\mathrm{req},n}+M_p}{M_{\mathrm{alloc},n}},\,0,\,1
\Bigr)
\quad (M_{\mathrm{alloc},n}>0).
\tag{2.3a}
$$

### 3.3. Показатель степени (вне runtime)

Чувствительность может подставлять \(u^{\gamma}\) с \(\gamma\in\{0{,}75;\,1;\,1{,}4\}\) **только в анализе**. Runtime-канон: \(\gamma=1\) (`SCOPE-IN-04`). Введение \(\gamma\neq 1\) в `pkg/energy` требует ADR.

---

## 4. Модель мощности пода

### 4.1. Политика IDLE-NONE

Статическая мощность включённого узла не порождается подом \(p\). Приписывать новому поду долю \(P_{\mathrm{idle},n}\) означало бы двойной счёт: idle уже входит в cluster KPI независимо от решения. Канон scoring — **IDLE-NONE**.

### 4.2. Маржинальная мощность

$$
\widehat{P}_{\mathrm{pod}}(p,n)
:=
P_n(u_n^{+})-P_n(u_n^{-})
=
\bigl(P_{\max,n}-P_{\mathrm{idle},n}\bigr)
\bigl(u_n^{+}-u_n^{-}\bigr),
\qquad
[\widehat{P}_{\mathrm{pod}}]=\mathrm{W}.
\tag{2.4}
$$

При отсутствии активного \(\operatorname{clip}\) (внутренность \((0,1)\))

$$
u_n^{+}-u_n^{-}=\frac{R_p}{R_{\mathrm{alloc},n}},
\qquad
\widehat{P}_{\mathrm{pod}}(p,n)
=
(P_{\max,n}-P_{\mathrm{idle},n})\,\frac{R_p}{R_{\mathrm{alloc},n}}.
\tag{2.4a}
$$

Это та же CPU-share атрибуция, что у Kepler/Scaphandre для *active* мощности, с заменой датчика на \(P(u)\).

Свойства:

- \(\widehat{P}_{\mathrm{pod}}(p,n)\ge 0\) при (2.2a) и (2.3);
- idle в (2.4) не входит;
- сумма маржинальных мощностей уже работающих подов восстанавливает динамическую часть \(P_n-P_{\mathrm{idle},n}\) (research T1.2 §8.3) — это тождество ex-post атрибуции, не вход `Score`.

### 4.3. Время в \(\widehat{P}_{\mathrm{pod}}(p,n,t)\)

Общая форма допускает кусочно-постоянный профиль мощности на горизонте. Канон scoring:

$$
\widehat{P}_{\mathrm{pod}}(p,n,t)
\equiv
\widehat{P}_{\mathrm{pod}}(p,n)
\quad
\text{для всех }t\in[t_{\mathrm{now}},\,t_{\mathrm{now}}+\hat T(p,n)).
\tag{2.4b}
$$

Нет ОДУ для \(u(t)\): ожидаемая утилизация фиксируется requests. Если когда-либо понадобится табличный профиль класса нагрузки, он задаётся конечным набором кусков, а не дифференциальным уравнением.

---

## 5. Энергия

Непрерывная форма (аналитический канон):

$$
E_n(t_0,T)
=
\eta\int_{t_0}^{t_0+T} P_n\bigl(u_n(t)\bigr)\,dt,
\qquad
[E]=\mathrm{kWh}.
\tag{2.5}
$$

Дискретная форма (реализационный канон). Пусть \(\{I_k\}_{k=1}^{K}\) — конечное разбиение \([t_0,t_0+T)\) с длинами \(\Delta t_k>0\) и представителями \(u_{n,k}\):

$$
E_n
=
\eta\sum_{k=1}^{K} P_n(u_{n,k})\,\Delta t_k,
\qquad
[E]=\mathrm{kWh}.
\tag{2.6}
$$

Для маржинальной энергии пода (scoring, IDLE-NONE):

$$
\widehat{E}(p,n)
=
\eta\sum_{k=1}^{K}
\widehat{P}_{\mathrm{pod}}(p,n,t_k)\,\Delta t_k.
\tag{2.6a}
$$

При (2.4b) сумма вырождается в \(\eta\,\widehat{P}_{\mathrm{pod}}(p,n)\,\hat T(p,n)\).

Функция \(P_n\circ u_n\) предполагается интегрируемой по Риману; для кусочно-постоянного \(u\) интеграл (2.5) **точно** равен (2.6). Отдельной схемы Рунге–Кутты нет и не будет.

---

## 6. Углеродный след

### 6.1. Непрерывная форма

Average consumption-based lifecycle intensity зоны \(z\) — кусочно-постоянная функция

$$
CI(z,t)=CI_{z,k}
\quad\text{при}\quad
t\in[t_k,\,t_k+\Delta t_k^{\mathrm{grid}}),
\qquad
[CI]=\mathrm{gCO_2e}/\mathrm{kWh}.
\tag{2.7}
$$

Линейная интерполяция по умолчанию **не выполняется**. Тогда

$$
CF_n
=
\eta\int_{t_0}^{t_0+T}
P_n\bigl(u_n(t)\bigr)\,CI\bigl(z(n),t\bigr)\,dt,
\qquad
[CF]=\mathrm{gCO_2e}.
\tag{2.8}
$$

### 6.2. Дискретная форма (узел, ex-post)

$$
CF_n
=
\eta\sum_{k=1}^{K}
P_n(u_{n,k})\,\Delta t_k\,CI\bigl(z(n),t_k\bigr).
\tag{2.9}
$$

Cluster KPI:

$$
CF_{\mathrm{cluster}}
=
\sum_{n\in\mathcal{N}} CF_n.
\tag{2.9a}
$$

### 6.3. Ожидаемый след размещения (ex-ante, scoring)

Горизонт \([t_{\mathrm{now}},\,t_{\mathrm{now}}+\hat T(p,n))\) пересекается с сеткой (2.7). Получается конечное разбиение с длинами \(\Delta t_k>0\), \(\sum_k\Delta t_k=\hat T(p,n)\), \(K<\infty\).

$$
\widehat{CF}(p,n)
=
\eta
\sum_{k=1}^{K}
\widehat{P}_{\mathrm{pod}}(p,n,t_k)\,
\Delta t_k\,
\widehat{CI}\bigl(z(n),t_k\bigr),
\qquad
[\widehat{CF}]=\mathrm{gCO_2e}.
\tag{2.10}
$$

Это размерностно замкнутая запись пользовательской суммы. При \(\eta=10^{-3}\) и \(\Delta t_k\) в часах:

$$
\widehat{CF}(p,n)
=
\sum_{k=1}^{K}
\widehat{P}_{\mathrm{pod}}(p,n,t_k)\,
\Delta t_k\,
\widehat{CI}\bigl(z(n),t_k\bigr)
\cdot 10^{-3}.
\tag{2.10h}
$$

При каноне runtime (\(\Delta t_k\) в секундах):

$$
\widehat{CF}(p,n)
=
\sum_{k=1}^{K}
\frac{
\widehat{P}_{\mathrm{pod}}(p,n,t_k)\,
\Delta t_k\,
\widehat{CI}\bigl(z(n),t_k\bigr)
}{3.6\cdot 10^{6}}.
\tag{2.10s}
$$

Формулы (2.10h) и (2.10s) численно тождественны при согласованных \(\Delta t_k\).

При постоянной мощности (2.4b):

$$
\widehat{CF}(p,n)
=
\eta\,\widehat{P}_{\mathrm{pod}}(p,n)
\sum_{k=1}^{K}
\Delta t_k\,\widehat{CI}\bigl(z(n),t_k\bigr).
\tag{2.10c}
$$

### 6.4. Построение разбиения

Пусть \(\mathcal{G}(z)=\{t_k^{\mathrm{grid}}\}\) — упорядоченные начала интервалов \(CI\) зоны \(z\), и \(\tau_0=t_{\mathrm{now}}\), \(\tau_{\mathrm{end}}=t_{\mathrm{now}}+\hat T(p,n)\). Узлы разбиения — отсортированное множество

$$
\{\tau_0,\tau_{\mathrm{end}}\}
\cup
\{t\in\mathcal{G}(z):\tau_0<t<\tau_{\mathrm{end}}\}.
$$

На каждом куске \([\tau_{k-1},\tau_k)\) длиной \(\Delta t_k=\tau_k-\tau_{k-1}\) значение \(\widehat{CI}(z,\tau_{k-1})\) берётся из единственного полуинтервала сетки, содержащего \(\tau_{k-1}\). Сложность построения \(O(K)\), \(K\le 1+\lceil\hat T/\Delta t_{\mathrm{grid}}\rceil\).

### 6.5. Прогноз \(CI\) и persistence

Если внешний forecast на горизонте доступен и валиден, \(\widehat{CI}=CI^{\mathrm{forecast}}\). Иначе (`SCOPE-EDGE-03`, `SCOPE-OUT-04`):

$$
\widehat{CI}(z,t_{\mathrm{now}}+\Delta)
=
CI(z,t_{\mathrm{now}}),
\qquad \Delta\ge 0.
\tag{2.11}
$$

Тогда (2.10c) вырождается в \(\eta\,\widehat{P}_{\mathrm{pod}}\,\hat T\cdot CI_{\mathrm{now}}(z)\). Perfect foresight (подстановка будущих actual из того же файла) в зачётном прогоне запрещён.

Семантика \(CI\): average, consumption-based, lifecycle, единица `gCO2e/kWh`. Direct и lifecycle в одном сравнении не смешиваются. Утверждение о marginal emissions запрещено.

---

## 7. Время выполнения и hard SLA

### 7.1. Expected runtime

Канон `SCOPE-IN-06`:

$$
\hat T(p,n)
=
T_{\mathrm{class}(p)},
\qquad
[\hat T]=\mathrm{s}.
\tag{2.12}
$$

Таблица «класс → expected runtime» измеряется на стенде и не зависит от \(n\). Зависимость от типа узла в runtime **не вводится** (иначе потребуется ADR и новая калибровка). Формулы ниже всё же пишутся как \(\hat T(p,n)\), чтобы остаться корректными при будущем расширении.

Если класс неизвестен и значение по умолчанию в конфигурации отсутствует, попытка планирования **не eligible** (fail-closed конфигурации workload): \(\widehat{CF}\) без горизонта не определён.

### 7.2. Hard-отсечение по deadline

Пусть \(T_{\mathrm{deadline}}(p):=t_{\mathrm{deadline}}(p)-t_{\mathrm{now}}\) при наличии аннотации дедлайна, и \(T_{\mathrm{deadline}}(p)=+\infty\) при её отсутствии. Размерность секунды.

$$
n\in\mathcal{N}_{\mathrm{SLA}}(p)
\iff
n\in\mathcal{N}_{\mathrm{feas}}(p)
\;\wedge\;
\hat T(p,n)\le T_{\mathrm{deadline}}(p).
\tag{2.13}
$$

Эквивалентная запись в абсолютном времени (глава 1, (1.3)):

$$
t_{\mathrm{now}}+\hat T(p,n)\le t_{\mathrm{deadline}}(p).
\tag{2.13a}
$$

**Семантика отсечения.** Узел с \(\hat T>T_{\mathrm{deadline}}\) не входит в \(\mathcal{N}^{\star}\) и **не участвует** в \(CF_{\min},CF_{\max}\). При каноне (2.12) условие одинаково для всех узлов: либо \(\mathcal{N}_{\mathrm{SLA}}=\mathcal{N}_{\mathrm{feas}}\), либо \(\mathcal{N}_{\mathrm{SLA}}=\varnothing\).

Если \(\mathcal{N}_{\mathrm{SLA}}(p)=\varnothing\), `PreScore` возвращает ошибку `SLA precondition failed`. Попытка завершается до `Score`; Pod остаётся `Pending`; действует стандартный backoff. Это fail-closed проверка конфигурации workload, а не temporal shifting и не собственный `Filter` (`SCOPE-OUT-16`). Экология не обходит SLA (`SCOPE-IN-08`).

Нуль в `Score` **не** является hard-отсечением: значение `0` ранжирует узел, но не исключает его из выбора фреймворка.

---

## 8. Частные скоринги

Область определения всех \(S_{\bullet}\) ниже — \(n\in\mathcal{N}^{\star}(p)\). Значения в \([0,1]\); больше — лучше.

### 8.1. Углеродный скоринг

$$
\begin{aligned}
CF_{\min}(p)
&:=
\min_{n\in\mathcal{N}^{\star}(p)}\widehat{CF}(p,n),
\\
CF_{\max}(p)
&:=
\max_{n\in\mathcal{N}^{\star}(p)}\widehat{CF}(p,n).
\end{aligned}
\tag{2.14}
$$

Размерность \(CF_{\min},CF_{\max}\) — \(\mathrm{gCO_2e}\).

$$
S_C(n)
=
\begin{cases}
1-
\dfrac{\widehat{CF}(p,n)-CF_{\min}(p)}{CF_{\max}(p)-CF_{\min}(p)},
&
CF_{\max}(p)-CF_{\min}(p)>\varepsilon_{CF},
\\[1.1em]
S_C^{\mathrm{neutral}},
&
\text{иначе.}
\end{cases}
\tag{2.15}
$$

Тождество при невырожденном разбросе:

$$
S_C(n)
=
\frac{CF_{\max}(p)-\widehat{CF}(p,n)}{CF_{\max}(p)-CF_{\min}(p)}.
\tag{2.15a}
$$

Канон: \(S_C^{\mathrm{neutral}}=1/2\), \(\varepsilon_{CF}=10^{-6}\,\mathrm{gCO_2e}\) (конфигурируемо). Вторая ветвь закрывает \(CF_{\max}=CF_{\min}\) и численно неотличимый разброс: деление на ноль запрещено (`GO-14`).

Следствия: \(S_C\in[0,1]\); лучший (минимальный) след даёт \(S_C=1\); худший — \(S_C=0\).

Узел без label зоны (`SCOPE-EDGE-02`): \(S_C(n)=S_C^{\mathrm{neutral}}\); \(\widehat{CF}(p,n)\) этого узла **не входит** в (2.14), чтобы отсутствующая зона не сдвигала шкалу остальных.

Эквивалент главы 1 (1.9): \(S_C^{(1.9)}(n)=100\cdot S_C(n)\) при той же нейтральной ветви. Канон Stage 2 — форма (2.15) на \([0,1]\) с последующим множителем \(100\) в (2.19).

### 8.2. Ресурсный скоринг

Сводная прогнозируемая утилизация **после** размещения \(p\):

$$
U_{\mathrm{node}}(p,n)
=
\operatorname{clip}
\bigl(
\lambda u_n^{+}
+
(1-\lambda)v_n^{+},\,
0,\,1
\bigr),
\qquad
\lambda\in[0,1].
\tag{2.16}
$$

$$
S_R(n)
=
1-U_{\mathrm{node}}(p,n),
\qquad
[S_R]=1.
\tag{2.17}
$$

Канон runtime: \(\lambda=1\) (CPU-only), согласованный с \(P(u)\). RAM-член — опциональный preference (`GO-12`); \(\lambda\neq 1\) задаётся конфигурацией сценария. \(S_R=1\) на пустом узле, \(S_R=0\) на полностью занятом. Это предпочтение spreading (LeastAllocated-тип) и **не** заменяет `NodeResourcesFit`.

При всегда включённых узлах \(S_R\) не меняет idle-часть \(CF_{\mathrm{cluster}}\); он штрафует уплотнение, которое увеличивает риск contention. Эффект на фактический runtime — гипотеза эксперимента.

### 8.3. SLA / performance скоринг

Hard-слой — (2.13). Soft-слой на \(\mathcal{N}_{\mathrm{SLA}}\):

$$
S_P(n)
=
\begin{cases}
1,
&
T_{\mathrm{deadline}}(p)=+\infty,
\\[0.4em]
1,
&
T_{\mathrm{deadline}}(p)=0
\;\wedge\;
\hat T(p,n)=0,
\\[0.4em]
\operatorname{clip}
\!\left(
\dfrac{T_{\mathrm{deadline}}(p)-\hat T(p,n)}{T_{\mathrm{deadline}}(p)},\,
0,\,1
\right),
&
0<T_{\mathrm{deadline}}(p)<+\infty
\;\wedge\;
\hat T(p,n)\le T_{\mathrm{deadline}}(p).
\end{cases}
\tag{2.18}
$$

Размерность числителя и знаменателя — секунды; отношение безразмерно. Экспонента спецификации §21 **не** входит в runtime-канон: она добавляет свободный \(k\) без калибровки. Введение \(e^{-k(\cdot)}\) — только ADR.

При (2.12) величина \(S_P(n)\) одинакова для всех \(n\in\mathcal{N}_{\mathrm{SLA}}\) и **не меняет** \(\operatorname{argmax}_n\operatorname{Score}(n)\). Это намеренно: различение узлов по производительности без node-dependent runtime было бы фиктивным. Hard-отсечение при этом полностью сохраняет `SCOPE-IN-08`.

---

## 9. Интегральная функция полезности

### 9.1. Внутренний Score плагина

$$
\operatorname{Score}(n)
=
100\cdot
\bigl(
w_C S_C(n)+w_R S_R(n)+w_P S_P(n)
\bigr),
\tag{2.19}
$$

$$
w_C,w_R,w_P\ge 0,
\qquad
w_C+w_R+w_P=1.
\tag{2.20}
$$

Тогда \(\operatorname{Score}(n)\in[0,100]\). Веса — конфигурация сценария, не константы кода (`REPRO-06`). Нарушение (2.20) при старте — ошибка конфигурации (плагин не стартует). Молчаливая перенормировка в горячем пути запрещена.

Режимы спецификации (примеры, не единственные допустимые точки):

| Режим | \(w_C\) | \(w_R\) | \(w_P\) |
| --- | --- | --- | --- |
| Carbon-first | \(0{,}7\) | \(0{,}2\) | \(0{,}1\) |
| Balanced (default) | \(0{,}6\) | \(0{,}2\) | \(0{,}2\) |
| Performance-first | \(0{,}2\) | \(0{,}2\) | \(0{,}6\) |

Default канона: \((w_C,w_R,w_P)=(0{,}6,\,0{,}2,\,0{,}2)\).

### 9.2. Отображение в контракт Scheduler Framework

Математический \(\operatorname{Score}(n)\in\mathbb{R}\). Контракт API — целое в \(\{0,1,\ldots,100\}\):

$$
s_{\mathrm{fwk}}(n)
=
\operatorname{clamp}
\bigl(
\operatorname{rnd}
(\operatorname{Score}(n)),\,
0,\,100
\bigr),
\tag{2.21}
$$

где \(\operatorname{rnd}\) — округление к ближайшему целому, ties away from zero; \(\operatorname{clamp}(x,a,b)=\operatorname{clip}(x,a,b)\).

Рекомендуемая реализация (не часть математического объекта, а защита от раннего квантования): в `PreScore` хранить fixed-point \(s^{\mathrm{fp}}=\operatorname{rnd}(100\cdot\operatorname{Score})\in\{0,\ldots,10000\}\); `Score` возвращает \(s^{\mathrm{fp}}\); `NormalizeScore` выполняет \(\operatorname{rnd}(s^{\mathrm{fp}}/100)\) и clamp в \([0,100]\). Итоговый контракт всё равно имеет 101 значение; частота ничьих проверяется тестом.

### 9.3. Два уровня весов

После `NormalizeScore` фреймворк считает

$$
\operatorname{TotalScore}(n)
=
w_{\mathrm{CA}}^{\mathrm{fw}}\,s_{\mathrm{fwk}}(n)
+
\sum_{q\in Q} w_q^{\mathrm{fw}} S_q(n),
\tag{2.22}
$$

где \(w^{\mathrm{fw}}\) — целые веса `KubeSchedulerConfiguration`, **не** обязанные суммироваться к 1. \(w_{\mathrm{CA}}^{\mathrm{fw}}\) фиксируется в карточке эксперимента. Путать \(w_C\) с \(w_{\mathrm{CA}}^{\mathrm{fw}}\) запрещено.

### 9.4. Стратегия B (не формула (2.19) с \(w_C=1\))

Наивный carbon-only baseline (`SCOPE-IN-13`) ранжирует по **текущей** \(CI\) зоны, без \(\widehat{P}\) и без горизонта \(\hat T\):

$$
S_B(n)
=
\begin{cases}
\dfrac{CI_{\max}-CI_n}{CI_{\max}-CI_{\min}},
&
CI_{\max}-CI_{\min}>\varepsilon_{CI},
\\[0.9em]
1/2,
&
\text{иначе,}
\end{cases}
\tag{2.23}
$$

\(CI_n\) — текущий (или persistence) average \(CI\) зоны, `gCO2e/kWh`; \(\varepsilon_{CI}=10^{-6}\,\mathrm{gCO_2e}/\mathrm{kWh}\). Плагин в этом режиме: `scoringMode: current-ci`, \(w_C=1\), \(w_R=w_P=0\), \(\operatorname{Score}_B=100\cdot S_B\).

---

## 10. Краевые случаи (математические предохранители)

Каждый предохранитель обязателен в реализации и в табличных тестах (`DOD-TEST-04`).

| ID | Условие | Предохранитель | Результат |
| --- | --- | --- | --- |
| G-01 | \(u<0\) или \(u>1\) | \(\operatorname{clip}(u,0,1)\) в (2.2)–(2.3) | \(P\in[P_{\mathrm{idle}},P_{\max}]\), \(S_R\in[0,1]\) |
| G-02 | \(R_{\mathrm{alloc},n}=0\) или \(M_{\mathrm{alloc},n}=0\) | узел \(\notin\mathcal{N}_{\mathrm{feas}}\); если всё же дошёл до формул — \(u_n^{\pm}:=1\) (соответственно \(v_n^{+}:=1\)) | нет деления на 0 |
| G-03 | \(P_{\mathrm{idle}}<0\) или \(P_{\max}<P_{\mathrm{idle}}\) или \(\mathrm{NaN}\) | домен (2.2a) нарушен | отказ старта плагина |
| G-04 | \(CF_{\max}=CF_{\min}\) или разброс \(\le\varepsilon_{CF}\) | вторая ветвь (2.15) | \(S_C=1/2\), нет деления на 0 |
| G-05 | \(\lvert\mathcal{N}^{\star}\rvert\le 1\) | (2.14) вырождено | как G-04 |
| G-06 | нет forecast | (2.11) | persistence на весь горизонт |
| G-07 | нет текущего \(CI\) зоны, кэш пуст | данные недоступны | G-10 |
| G-08 | данные старше `maxDataAgeSeconds` (default \(300\,\mathrm{s}\)) | `SCOPE-EDGE-04` | \(\operatorname{Score}(n)=\operatorname{Score}_{\mathrm{neutral}}=50\) **всем** узлам; `Success`; carbon ranking отключён целиком (не только \(S_C\)) |
| G-09 | узел без `carbon-aware.io/zone` | `SCOPE-EDGE-02` | \(S_C=1/2\); \(S_R,S_P\) считаются; узел не в (2.14) |
| G-10 | \(CI<0\), \(\mathrm{NaN}\), \(\pm\infty\), \(CI>2000\,\mathrm{gCO_2e}/\mathrm{kWh}\) | запись invalid | зона как G-09; если invalid все зоны — G-08 |
| G-11 | \(\hat T=0\) | \(K=0\) или все \(\Delta t_k=0\Rightarrow\widehat{CF}=0\) | вырождение \(S_C\), если все нули; SLA: \(0\le T_{\mathrm{deadline}}\) |
| G-12 | \(\hat T>T_{\mathrm{deadline}}\) | (2.13) | hard-отсечение; при (2.12) — `PreScore` Error |
| G-13 | дедлайн отсутствует | \(T_{\mathrm{deadline}}=+\infty\) | hard-слой всегда истинен; \(S_P=1\) |
| G-14 | \(T_{\mathrm{deadline}}=0\) и \(\hat T=0\) | отдельная ветвь (2.18) | \(S_P=1\), нет \(0/0\) |
| G-15 | \(\Delta t_k\le 0\) | кусок отбрасывается | не портит сумму |
| G-16 | пустое пересечение горизонта с сеткой при \(\hat T>0\) | построить один кусок длины \(\hat T\) с persistence | \(K\ge 1\) |
| G-17 | \(\widehat{CF}<0\) (составной сбой входов) | \(\widehat{CF}\leftarrow\max\{\widehat{CF},0\}\) | домен \(CF\ge 0\) |
| G-18 | \(w_i<0\) или \(\lvert w_C+w_R+w_P-1\rvert>\varepsilon_w\) (\(\varepsilon_w=10^{-9}\)) | (2.20) | отказ старта |
| G-19 | \(\operatorname{Score}\notin[0,100]\) из-за численной ошибки | (2.21) clamp | контракт фреймворка |
| G-20 | неизвестный workload class, нет default | \(\hat T\) не определён | fail-closed, как G-12 |
| G-21 | \(\mathrm{NaN}\) в любом промежуточном \(S_{\bullet}\) | считать узел как G-09 / G-08 по источнику | планирование не паникует (`GO-06`) |

Цепочка деградации данных (`SCOPE-IN-09`): fresh cache → persistence / stale-in-window → отключение carbon scoring (G-08) → baseline behaviour остальных плагинов. Ошибка источника **не** преобразуется в отказ работоспособного Pod.

---

## 11. Вычислительная сложность

Пусть \(N=\lvert\mathcal{N}^{\star}(p)\rvert\), \(K\) — число кусков (2.10). \(K\) ограничен горизонтом и гранулярностью (\(K\le 1+\lceil\hat T/\Delta t_{\mathrm{grid}}\rceil\); для \(\hat T\le 7200\,\mathrm{s}\) и шага \(300\,\mathrm{s}\) имеем \(K\le 25\)).

| Фаза | Работа | Сложность |
| --- | --- | --- |
| `PreScore` | для каждого \(n\): (2.3), (2.4), разбиение, (2.10), затем (2.14)–(2.19) | \(O(NK)\) арифметических операций |
| `Score` | lookup \(\operatorname{Score}(n)\) по имени узла | **\(O(1)\)** |
| `NormalizeScore` | (2.21) по списку | \(O(N)\) |

Нет итеративных солверов, нет интегрирования ОДУ, нет I/O (`SCOPE-OUT-17`). Горячий путь `Score` не зависит от \(K\).

---

## 12. Инварианты сравнительного эксперимента

Узлы всегда включены, привязка \(n\mapsto z(n)\) фиксирована. Тогда (T1.2 §6.4):

1. **Idle не зависит от стратегии размещения.** \(CF_{\mathrm{idle}}=\sum_n\eta P_{\mathrm{idle},n}\sum_k\Delta t_k\,CI_{z(n),k}\) одинаков для A/B/C. Разность \(CF_{\mathrm{cluster}}\) определяется только динамической частью.
2. **Масштаб \(\Delta P\).** При однородных \((P_{\mathrm{idle}},P_{\max})\) знак сравнения стратегий по \(\Delta CF\) не зависит от \(\Delta P>0\).
3. **Процент зависит от \(k=P_{\mathrm{idle}}/P_{\max}\).** Относительное «снижение на X %» без sensitivity по \(k\) публиковать нельзя (`DOD-EXP-11`, `LIM-07`).
4. **Разнородные профили.** При разных \((P_{\mathrm{idle}},P_{\max})\) ranking по \(\widehat{CF}\) не сводится к ranking по \(CI\); энергопрофиль идентифицируем.

Следствие для scoring: (2.4) согласовано с инвариантом 1 — idle не ранжирует кандидатов.

---

## 13. Нормативные параметры по умолчанию

| Параметр | Значение | Единица | Комментарий |
| --- | --- | --- | --- |
| \(P_{\mathrm{idle}}\) | \(40\) | W | CAL-A; не nameplate |
| \(P_{\max}\) | \(100\) | W | CAL-A |
| \(\lambda\) | \(1\) | \(1\) | CPU-only \(U_{\mathrm{node}}\) |
| \((w_C,w_R,w_P)\) | \((0{,}6,\,0{,}2,\,0{,}2)\) | \(1\) | simplex |
| \(S_C^{\mathrm{neutral}}\) | \(1/2\) | \(1\) | G-04, G-09 |
| \(\operatorname{Score}_{\mathrm{neutral}}\) | \(50\) | \(1\) | G-08 |
| \(\varepsilon_{CF}\) | \(10^{-6}\) | gCO2e | G-04 |
| \(\varepsilon_{CI}\) | \(10^{-6}\) | gCO2e/kWh | (2.23) |
| \(\varepsilon_w\) | \(10^{-9}\) | \(1\) | G-18 |
| `maxDataAgeSeconds` | \(300\) | s | `SCOPE-EDGE-04` |
| допустимый \(CI\) | \([0,2000]\) | gCO2e/kWh | G-10 |
| политика idle (scoring) | IDLE-NONE | — | §4.1 |
| \(\gamma\) | \(1\) | \(1\) | runtime |

Sensitivity (`DOD-EXP-11`): \(P_{\mathrm{idle}}\in\{20,40,60\}\,\mathrm{W}\), \(P_{\max}\in\{70,100,130\}\,\mathrm{W}\), \(k\in\{0{,}30;\,0{,}40;\,0{,}50;\,0{,}70\}\).

---

## 14. Эталонные численные примеры

Примеры обязательны для `DOD-TEST-03`. Арифметика ниже — ручная; реализация должна совпасть с указанными числами.

### 14.1. Мощность узла и пода (T1.2 §8.6)

Дано: \(P_{\mathrm{idle}}=40\,\mathrm{W}\), \(P_{\max}=100\,\mathrm{W}\), \(R_{\mathrm{alloc}}=1000\,\mathrm{millicores}\), \(R_{\mathrm{req}}=500\), два уже размещённых пода с фактическими \(c_1=300\), \(c_2=200\) millicores (ex-post атрибуция), \(u=0{,}5\).

$$
P_n=40+(100-40)\cdot 0{,}5=70\,\mathrm{W}.
$$

Маржинальная мощность нового пода с \(R_p=300\) при \(R_{\mathrm{req}}=500\):

$$
u^{-}=0{,}5,\quad u^{+}=0{,}8,
\quad
\widehat{P}_{\mathrm{pod}}=(100-40)\cdot(0{,}8-0{,}5)=18\,\mathrm{W}.
$$

Ex-post динамика уже работающих: \(\widehat{P}_1=60\cdot 0{,}30=18\,\mathrm{W}\), \(\widehat{P}_2=12\,\mathrm{W}\), сумма \(30\,\mathrm{W}=70-40\). Idle \(40\,\mathrm{W}\) в pod-KPI не входит.

### 14.2. Размерностная проверка \(\widehat{CF}\) (часы)

\(\widehat{P}_{\mathrm{pod}}=18\,\mathrm{W}\), \(\hat T=2\,\mathrm{h}\), \(CI=100\,\mathrm{gCO_2e}/\mathrm{kWh}\) (const), \(\eta=10^{-3}\):

$$
\widehat{E}=\frac{18\cdot 2}{1000}=0{,}036\,\mathrm{kWh},
\qquad
\widehat{CF}=0{,}036\cdot 100=3{,}6\,\mathrm{gCO_2e}.
$$

Прямая форма (2.10h): \(18\cdot 2\cdot 100\cdot 10^{-3}=3{,}6\,\mathrm{gCO_2e}\).

Секундная форма (2.10s): \(\hat T=7200\,\mathrm{s}\),

$$
\widehat{CF}=\frac{18\cdot 7200\cdot 100}{3.6\cdot 10^{6}}=3{,}6\,\mathrm{gCO_2e}.
$$

Полный узел на том же интервале: \(E_n=70\cdot 2/1000=0{,}14\,\mathrm{kWh}\), \(CF_n=14\,\mathrm{gCO_2e}\). Idle-след \(8\,\mathrm{gCO_2e}\) в \(\widehat{CF}\) не входит.

### 14.3. Scoring двух узлов

Pod \(p\): \(R_p=300\,\mathrm{millicores}\), \(\hat T=2\,\mathrm{h}\), \(T_{\mathrm{deadline}}=4\,\mathrm{h}\). Веса default \((0{,}6,0{,}2,0{,}2)\), \(\lambda=1\), \(\eta=10^{-3}\), \(P_{\mathrm{idle}}=40\,\mathrm{W}\), \(P_{\max}=100\,\mathrm{W}\). Forecast отсутствует → persistence.

| | \(n_1\) (north) | \(n_2\) (west) |
| --- | --- | --- |
| \(R_{\mathrm{alloc}}\) | \(1000\) | \(1000\) |
| \(R_{\mathrm{req}}\) | \(200\) | \(400\) |
| \(u^{+}\) | \(0{,}5\) | \(0{,}7\) |
| \(\widehat{P}_{\mathrm{pod}}\) | \(60\cdot 0{,}3=18\,\mathrm{W}\) | \(60\cdot 0{,}3=18\,\mathrm{W}\) |
| \(CI_{\mathrm{now}}\) | \(100\,\mathrm{gCO_2e}/\mathrm{kWh}\) | \(400\,\mathrm{gCO_2e}/\mathrm{kWh}\) |
| \(\widehat{CF}\) | \(3{,}6\,\mathrm{gCO_2e}\) | \(14{,}4\,\mathrm{gCO_2e}\) |
| \(U_{\mathrm{node}}=u^{+}\) | \(0{,}5\) | \(0{,}7\) |

$$
CF_{\min}=3{,}6,\quad CF_{\max}=14{,}4,
\quad
S_C(n_1)=1,\quad S_C(n_2)=0.
$$

$$
S_R(n_1)=0{,}5,\quad S_R(n_2)=0{,}3,
\quad
S_P(n_1)=S_P(n_2)=\frac{4-2}{4}=0{,}5.
$$

$$
\operatorname{Score}(n_1)=100\cdot(0{,}6\cdot 1+0{,}2\cdot 0{,}5+0{,}2\cdot 0{,}5)=80,
$$

$$
\operatorname{Score}(n_2)=100\cdot(0{,}6\cdot 0+0{,}2\cdot 0{,}3+0{,}2\cdot 0{,}5)=16.
$$

Выбор: \(n_1\). \(s_{\mathrm{fwk}}\in\{80,16\}\).

### 14.4. Вырождение \(CF_{\max}=CF_{\min}\)

Те же узлы, но \(CI_1=CI_2=100\). Тогда \(\widehat{CF}(p,n_1)=\widehat{CF}(p,n_2)=3{,}6\), \(S_C=1/2\).

$$
\operatorname{Score}(n_1)=100\cdot(0{,}6\cdot 0{,}5+0{,}2\cdot 0{,}5+0{,}2\cdot 0{,}5)=50,
$$

$$
\operatorname{Score}(n_2)=100\cdot(0{,}6\cdot 0{,}5+0{,}2\cdot 0{,}3+0{,}2\cdot 0{,}5)=46.
$$

Ранжирование сохраняется за счёт \(S_R\); деления на ноль нет.

### 14.5. \(u>1\)

\(R_{\mathrm{alloc}}=1000\), \(R_{\mathrm{req}}=900\), \(R_p=300\) (узел **не** должен быть в \(\mathcal{N}_{\mathrm{feas}}\); тест предохранителя). Без clip: \(u^{+}=1{,}2\). С clip: \(u^{+}=1\), \(u^{-}=0{,}9\), \(\widehat{P}_{\mathrm{pod}}=60\cdot 0{,}1=6\,\mathrm{W}\), \(U_{\mathrm{node}}=1\), \(S_R=0\).

### 14.6. Hard SLA

\(t_{\mathrm{now}}=0\), \(\hat T=1800\,\mathrm{s}\), \(t_{\mathrm{deadline}}=1200\,\mathrm{s}\). Условие (2.13a): \(1800\le 1200\) ложно. \(\mathcal{N}_{\mathrm{SLA}}=\varnothing\). `PreScore` Error. `Score` не вызывается.

Контраст: \(t_{\mathrm{deadline}}=3600\,\mathrm{s}\) → eligible, \(S_P=(3600-1800)/3600=0{,}5\).

### 14.7. Persistence vs два интервала forecast

\(\widehat{P}_{\mathrm{pod}}=18\,\mathrm{W}\), \(\hat T=2\,\mathrm{h}\), \(\eta=10^{-3}\). Forecast: \(1\,\mathrm{h}\) при \(100\,\mathrm{gCO_2e}/\mathrm{kWh}\), затем \(1\,\mathrm{h}\) при \(400\).

$$
\widehat{CF}=18\cdot 1\cdot 100\cdot 10^{-3}+18\cdot 1\cdot 400\cdot 10^{-3}=1{,}8+7{,}2=9{,}0\,\mathrm{gCO_2e}.
$$

Persistence от \(CI_{\mathrm{now}}=100\): \(3{,}6\,\mathrm{gCO_2e}\) (пример 14.2). Различие стратегий B и C на этом профиле **идентифицируемо**.

---

## 15. Соответствие Scheduler Framework

Порядок: стандартный `Filter` → `CarbonAware.PreScore` (SLA, (2.10), min/max, (2.19)) → параллельный `Score` \(O(1)\) → `NormalizeScore` (2.21).

Состояние одного Pod неизменно на все узлы (один snapshot \(CI\)). HTTP в `Score` запрещён. Формулы (2.15) и G-04 обязаны быть посчитаны в `PreScore`, где известен весь \(\mathcal{N}^{\star}\): `NormalizeScore` видит только итоговый скаляр.

---

## 16. Допущения (перенос LIM без смены смысла)

Модель наследует LIM-01…LIM-09 research T1.2: оценочный характер \(P\); CPU-only динамика; аффинность; логические, а не физические узлы kind; учётная атрибуция; раздельность cluster/pod KPI; запрет абсолютных процентов без sensitivity; запрет экстраполяции \(\mathrm{gCO_2e}\) на production.

Дополнительно: \(\hat T\) не зависит от \(n\); \(CI\) — average lifecycle; узлы не выключаются; сеть/storage/embodied carbon отсутствуют.

---

## 17. Трассировка к Scope и главе 1

| Норма / формула | Где закрыта |
| --- | --- |
| `SCOPE-IN-04` \(P(u)\) | (2.2) |
| `SCOPE-IN-05` дискретная сумма \(CF\) | (2.9), (2.10) с \(\eta\) |
| `SCOPE-IN-06` runtime-таблица | (2.12) |
| `SCOPE-IN-07` \(S=w_CS_C+w_RS_R+w_PS_P\), \([0,100]\) | (2.19)–(2.21) |
| `SCOPE-IN-08` hard constraints до carbon | (2.13), §7.2 |
| `SCOPE-IN-09` fallback | G-06…G-10 |
| `SCOPE-EDGE-02` нет зоны | G-09 |
| `SCOPE-EDGE-03` нет прогноза | (2.11) |
| `SCOPE-EDGE-04` stale | G-08 |
| `SCOPE-EDGE-07` cost | \(w_K=0\), не в (2.19) |
| Глава 1 (1.3) | (2.13a) |
| Глава 1 (1.4)–(1.7) | (2.2), (2.3), (2.4), (2.10s) |
| Глава 1 (1.8)–(1.12) | (2.11), (2.15), (2.19), (2.22), (2.23) |

Уточнения относительно инженерной спецификации (входной канон, `CONF-05`): явный \(\eta\); IDLE-NONE для \(\widehat{CF}\); \(S_P\) — линейный slack, не экспонента; частные скоринги на \([0,1]\). Смысл спецификации сохранён; сжатая запись §12 без делителя признана неполной.

---

## 18. Утверждение (Quality Gate 2 / ADR-001)

Чеклист передачи в T2.2 закрыт независимой валидацией: 48/48 проверок PASS, seed `42`, повторный контрольный прогон 2026-09-04 — `verdict=PASS checks=48 failed=0`. Блокирующих SSOT Change Request нет.

| Пункт бывшего §18 (вход T2.2) | Резолюция |
| --- | --- |
| Тождество (2.1a)–(2.1b) и оракул 14.2 | PASS, \(3{,}6\,\mathrm{gCO_2e}\) |
| Неотрицательность \(\widehat{P}_{\mathrm{pod}}\), \(E\), \(CF\) | PASS |
| \(S_C,S_R,S_P\in[0,1]\), \(\operatorname{Score}\in[0,100]\) | PASS |
| Нет \(0/0\) при G-02, G-04, G-14 | PASS; \(S_C^{\mathrm{neutral}}=1/2\) |
| Hard-отсечение 14.6 \(\neq\) `Score=0` | PASS |
| G-08 гасит итоговый \(\operatorname{Score}\) | PASS |
| (2.23) \(\neq\) (2.19) при \(w_C=1\) на \(\widehat{CF}\) | PASS, 4 горизонта расхождения |
| \(O(1)\) фазы `Score` | PASS |
| Оракулы 14.1–14.7 | PASS, фиксированный вход unit-тестов |

**Резолюция L1.** Формулы (2.1)–(2.23), каталог G-01…G-21, параметры §13 и эталоны §14 утверждены и **заблокированы**. Изменение любой из этих норм — только новым ADR. Неблокирующие замечания T2.2 (относительный порог \(\varepsilon_{CF}\), комментарий скачка \(S_C\) в коде, sweep весов через \(w_C=0{,}7\,w_R\), freeze Python 3.11) **не** входят в настоящий канон и не открывают права на правку формул.

Следующий этап: архитектурное проектирование (Stage 3) и реализация чистых функций по оракулам §14.
