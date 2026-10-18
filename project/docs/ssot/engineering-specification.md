Да. Я бы сейчас уже не рассматривал это как «идею диплома», а зафиксировал бы **полноценную инженерную спецификацию**, на которую можно опираться при разговоре с руководителем и затем при реализации.

Я дополнительно сверил архитектурные ограничения с актуальной документацией Kubernetes и текущими возможностями Scheduler Framework. В частности, в актуальном Kubernetes Scheduler Framework есть `Filter`, `Score`, `Reserve`, `PreScore` и другие extension points; scheduler сначала фильтрует неподходящие nodes, затем ранжирует оставшиеся. Кастомный plugin должен быть скомпилирован в scheduler, а не просто подключён как внешний HTTP-сервис. ([Kubernetes][1])

И сразу главное:

> **Я бы изменил исходную концепцию в трёх местах:**
>
> 1. не обещать «перенос workload между регионами» — Kubernetes Scheduler выбирает node для нового Pod, а не мигрирует уже запущенный Pod;
> 2. не использовать только текущий Carbon Intensity — для длительного workload нужно учитывать ожидаемую интенсивность на всём интервале выполнения;
> 3. Carbon-aware HPA убрать из обязательного scope и оставить как возможное расширение.

Это делает проект **менее эффектным на бумаге, но намного более корректным технически и значительно более защищаемым**.

---

# 0. Финальная концепция в одном абзаце

Предлагаемая система представляет собой **углеродо-ориентированный модуль планирования Kubernetes**, который при размещении гибких вычислительных задач выбирает из допустимых nodes ту, на которой ожидаемый углеродный след выполнения минимален при соблюдении ресурсных и временных ограничений.

Система получает:

* характеристики nodes;
* CPU/RAM utilization;
* resource requests/limits Pod;
* принадлежность node к энергетической зоне;
* текущую и, при наличии, прогнозируемую Carbon Intensity;
* оценку энергопотребления node;
* параметры workload.

На основе этих данных она рассчитывает ожидаемые:

$$
Energy
$$

$$
CO_2e
$$

и многокритериальный score:

$$
Score = f(Carbon, Resources, SLA, Cost)
$$

После стандартного Kubernetes `Filter` собственный plugin выполняет `Score` оставшихся nodes и передаёт Kubernetes Scheduler результат.

В экспериментальной части система сравнивается со стандартным scheduler по:

* углеродному следу;
* энергопотреблению;
* времени выполнения;
* scheduling latency;
* resource utilization;
* нарушению SLA.

---

# 1. Концепция проекта

## 1.1. Проблема

Традиционный Kubernetes Scheduler оптимизирует размещение Pod прежде всего относительно:

* доступности ресурсов;
* affinity/anti-affinity;
* topology;
* taints/tolerations;
* других ограничений и предпочтений.

Однако он **не учитывает углеродную интенсивность электроэнергии**, используемой инфраструктурой.

При этом одинаковая вычислительная нагрузка может иметь различный углеродный след в зависимости от:

1. места размещения;
2. времени выполнения;
3. структуры генерации электроэнергии;
4. энергоэффективности вычислительной инфраструктуры.

Например:

$$
100\;gCO_2e/kWh
$$

и

$$
500\;gCO_2e/kWh
$$

дают пятикратную разницу в выбросах при одинаковом энергопотреблении.

---

# 1.2. Важнейшее уточнение: «регион»

В исходной идее есть технически слабое место.

Ты писал:

> «переносить задачи в другой регион облака».

Обычный Kubernetes cluster **не является глобальным scheduler'ом нескольких cloud regions**.

В реальном AWS, например:

```text
EKS cluster EU-West
       ↓
nodes EU-West
```

и

```text
EKS cluster EU-North
       ↓
nodes EU-North
```

— это обычно разные Kubernetes-кластеры.

Поэтому твой диплом **не должен утверждать**, что один Kubernetes Scheduler непосредственно выбирает AWS region.

### Правильная формулировка

В рамках диплома вводится абстракция:

> **энергетическая зона (energy zone)**.

Каждая node получает:

```yaml
metadata:
  labels:
    carbon-aware.io/zone: north
```

Например:

```text
node-01 → north → 90 gCO₂/kWh
node-02 → west  → 410 gCO₂/kWh
node-03 → central → 280 gCO₂/kWh
```

Физически они могут находиться на одной лабораторной машине.

Это **экспериментальная модель географически распределённой cloud infrastructure**.

Если когда-нибудь понадобится настоящий multi-cluster:

```text
Global Controller
       │
 ┌─────┼─────┐
 ▼     ▼     ▼
K8s-1 K8s-2 K8s-3
```

это уже отдельная работа.

**Для диплома я бы этого не делал.**

---

# 1.3. Актуальность

Проблема относится к направлениям:

* Green Computing;
* Sustainable IT;
* GreenOps;
* Carbon-Aware Computing;
* Cloud Computing;
* Kubernetes scheduling;
* resource optimization.

Динамические данные о carbon intensity действительно могут иметь достаточно высокую временную гранулярность; например, исторические наборы Electricity Maps предоставляются вплоть до 5-минутных интервалов и содержат показатели carbon intensity в `gCO₂eq/kWh`. ([Electricity Maps][2])

---

# 1.4. Цель

> **Разработка и экспериментальное исследование алгоритма углеродо-ориентированного планирования вычислительных задач в Kubernetes, минимизирующего ожидаемый углеродный след выполнения workload при соблюдении ресурсных и временных ограничений.**

---

# 1.5. Задачи

### Исследовательские

1. Исследовать существующие методы carbon-aware computing.
2. Исследовать архитектуру Kubernetes Scheduler.
3. Исследовать Scheduler Framework.
4. Исследовать методы оценки энергопотребления вычислительных узлов.
5. Исследовать методы оценки carbon footprint.

### Проектные

6. Разработать архитектуру системы.
7. Разработать модель данных.
8. Разработать математическую модель.
9. Разработать алгоритм scoring.
10. Определить критерии допустимого размещения.

### Практические

11. Реализовать Carbon Data Service.
12. Реализовать carbon-aware Scheduler Plugin.
13. Интегрировать систему с Prometheus.
14. Реализовать Grafana dashboard.
15. Создать экспериментальную Kubernetes-инфраструктуру.

### Исследовательские эксперименты

16. Сравнить стандартный и разработанный scheduler.
17. Оценить снижение carbon footprint.
18. Оценить влияние на runtime.
19. Оценить scheduling latency.
20. Исследовать trade-off между CO₂ и SLA.

---

# 1.6. Объект исследования

> Процессы автоматизированного планирования и размещения вычислительных workloads в Kubernetes-кластере.

# 1.7. Предмет исследования

> Методы углеродо-ориентированного выбора вычислительного узла на основе динамической углеродной интенсивности электроэнергии, энергопотребления, доступности ресурсов и требований к времени выполнения workload.

---

# 1.8. Конечный результат

Не просто исходный код.

Итоговый результат:

### 1. Работающий Kubernetes Scheduler Plugin

Например:

```text
CarbonAware
```

### 2. Carbon Data Service

Получает и нормализует данные:

```text
Carbon Intensity
Forecast
Zone
Timestamp
Data quality
```

### 3. Energy Model

Оценивает:

```text
Power
Energy
CO₂
```

### 4. Monitoring

Prometheus + Grafana.

### 5. Experimental environment

Kubernetes cluster с несколькими логическими energy zones.

### 6. Экспериментальные результаты

Например:

```text
Carbon reduction:       18.4%
Runtime overhead:        4.7%
Scheduling latency:     +1.8 ms
SLA violations:          0%
```

Цифры здесь **пример**, а не обещание результата.

---

# 2. Архитектура

Я бы сделал следующую архитектуру.

```text
                         ┌──────────────────────┐
                         │ Carbon Intensity API │
                         └───────────┬──────────┘
                                     │
                                     ▼
                         ┌──────────────────────┐
                         │ Carbon Data Service  │
                         │                      │
                         │ API adapter          │
                         │ Cache                │
                         │ Forecast             │
                         │ Validation           │
                         └───────────┬──────────┘
                                     │
                                     │
                   ┌─────────────────▼──────────────────┐
                   │          Kubernetes Cluster        │
                   │                                    │
                   │  ┌──────────────────────────────┐  │
                   │  │      Custom kube-scheduler   │  │
                   │  │                              │  │
                   │  │ CarbonAware Plugin            │  │
                   │  │                              │  │
                   │  │ PreScore                     │  │
                   │  │ Score                        │  │
                   │  │ NormalizeScore               │  │
                   │  └──────────────┬───────────────┘  │
                   │                 │                  │
                   │                 ▼                  │
                   │       ┌───────────────────┐        │
                   │       │ Candidate Nodes   │        │
                   │       └───────┬───────────┘        │
                   │               │                    │
                   │       ┌───────┼─────────┐          │
                   │       ▼       ▼         ▼          │
                   │     Node1   Node2     Node3        │
                   │     North   Central   West         │
                   │                                    │
                   └────────────────────────────────────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │ Prometheus  │
                              └──────┬──────┘
                                     │
                                     ▼
                              ┌─────────────┐
                              │  Grafana    │
                              └─────────────┘
```

---

# 3. Компоненты

## 3.1. Carbon Data Service

Это отдельный небольшой сервис.

Его задача — **не принимать решения**.

Он отвечает за:

* получение Carbon Intensity;
* нормализацию;
* caching;
* timestamp;
* проверку freshness;
* forecast;
* fallback.

Например:

```http
GET /v1/carbon/zones/north/current
```

Ответ:

```json
{
  "zone": "north",
  "carbonIntensity": 82.4,
  "unit": "gCO2e/kWh",
  "timestamp": "2026-09-01T10:00:00Z",
  "source": "electricity-maps",
  "freshnessSeconds": 35
}
```

---

# 3.2. Почему Scheduler не должен напрямую ходить в API

Очень важный архитектурный момент.

**Не надо делать:**

```text
Scheduler
   ↓
Internet
   ↓
Electricity Maps
```

потому что scheduling cycle должен быть быстрым и предсказуемым.

Иначе один HTTP timeout может затормозить scheduler.

Правильно:

```text
External API
      ↓
Carbon Service
      ↓
local memory cache
      ↓
Scheduler
```

Scheduler получает уже подготовленное состояние.

---

# 3.3. Scheduler Plugin

Основная разработка диплома.

Я бы использовал:

```text
PreScore
Score
NormalizeScore
```

и **не трогал бы QueueSort**.

Kubernetes официально разделяет Filter и Score: Filter определяет допустимые nodes, Score ранжирует оставшиеся. ([Kubernetes][3])

---

# 3.4. Почему не писать собственный scheduler с нуля

Потому что это неправильный scope.

Kubernetes уже предоставляет Scheduler Framework как pluggable architecture. Plugins компилируются непосредственно с scheduler и подключаются через scheduling profiles. ([Kubernetes][1])

Твоя разработка:

```text
Kubernetes Scheduler
       +
CarbonAware Plugin
```

а не:

```text
Саша пишет свой Kubernetes Scheduler с нуля
```

---

# 4. Поток данных

Полный pipeline:

```text
External Carbon API
        ↓
Carbon Data Service
        ↓
Cache
        ↓
Prometheus / Scheduler
        ↓
Node carbon state
        ↓
Pod arrives
        ↓
Filter
        ↓
PreScore
        ↓
Carbon model
        ↓
Score
        ↓
NormalizeScore
        ↓
Default scheduler scoring
        ↓
Node selection
        ↓
Binding
        ↓
Workload execution
        ↓
Metrics
        ↓
Energy estimation
        ↓
CO₂ estimation
        ↓
Grafana
```

---

# 5. Что происходит в реальном времени

### Реальное время

* Pod scheduling;
* Node availability;
* resource requests;
* Carbon data cache;
* Scheduler scoring.

### Периодически

Например каждые:

```text
1–5 минут
```

обновляются:

* Carbon Intensity;
* forecast;
* node utilization;
* energy model parameters.

---

# 6. Главная математическая модель

Теперь самое важное.

Я бы **не строил модель просто на Carbon Intensity**.

Потому что:

$$
CO_2 \neq CarbonIntensity
$$

Carbon Intensity — это:

$$
gCO_2e/kWh
$$

а нам нужны выбросы конкретной workload:

$$
gCO_2e
$$

---

# 7. Модель энергопотребления

Для лабораторной инфраструктуры физическую мощность каждой VM получить достоверно практически невозможно.

Поэтому вводим модель:

$$
P_{node}(u)=P_{idle}+(P_{max}-P_{idle})u
$$

где:

* \(P_{node}\) — мощность node;
* \(P_{idle}\) — мощность в состоянии idle;
* \(P_{max}\) — мощность при полной нагрузке;
* \(u\) — CPU utilization от 0 до 1.

Например:

```text
P_idle = 40 W
P_max  = 100 W
CPU    = 0.5
```

Тогда:

$$
P=40+(100-40)\times0.5
$$

$$
P=70W
$$

---

# 8. Почему это допустимо

Потому что это **модель оценки**, а не утверждение:

> «этот сервер физически потребляет ровно 70 W».

В дипломе надо прямо написать:

> В условиях виртуализированной экспериментальной среды прямое измерение потребляемой мощности отдельного Kubernetes node невозможно или не является репрезентативным, поэтому используется калиброванная модель оценки энергопотребления.

Это нормальное научное допущение.

---

# 9. Более точная модель

Можно добавить RAM:

$$
P =
P_{idle}
+
\Delta P_{CPU}u_{CPU}
+
\Delta P_{RAM}u_{RAM}
$$

где:

* \(u_{CPU}\) — CPU utilization;
* \(u_{RAM}\) — RAM utilization;
* \(\Delta P_{CPU}\) — вклад CPU;
* \(\Delta P_{RAM}\) — вклад RAM.

Но я бы **не делал эту модель основной**, если нет возможности нормально откалибровать коэффициенты.

Основная:

$$
P=P_{idle}+(P_{max}-P_{idle})u
$$

RAM можно учитывать как дополнительный penalty в scheduler.

---

# 10. Энергия

Если workload работает \(T\) часов:

$$
E=P\cdot T
$$

Если \(P\) в ваттах:

$$
E_{kWh}=\frac{P(W)\cdot T(h)}{1000}
$$

Например:

$$
P=70W
$$

$$
T=2h
$$

получаем:

$$
E=0.14kWh
$$

---

# 11. Carbon Footprint

Основная формула:

$$
CF=E\cdot CI
$$

где:

* \(CF\) — carbon footprint;
* \(E\) — энергия в kWh;
* \(CI\) — carbon intensity в \(gCO_2e/kWh\).

Например:

$$
E=0.14kWh
$$

$$
CI=100gCO_2e/kWh
$$

тогда:

$$
CF=14gCO_2e
$$

---

# 12. Но здесь появляется главный научный нюанс

Если workload выполняется два часа, неправильно брать:

```text
CI сейчас = 100
```

и использовать его для всех двух часов.

Carbon intensity изменяется во времени.

Поэтому вводим временной ряд:

$$
CI(t)
$$

И:

$$
CF=\int_0^T P(t)\cdot CI(t)\,dt
$$

В дискретном виде:

$$
CF=\sum_{i=1}^{N}P_i\cdot\Delta t_i\cdot CI_i
$$

где:

* \(P_i\) — мощность;
* \(\Delta t_i\) — продолжительность интервала;
* \(CI_i\) — carbon intensity.

**Это значительно сильнее математически.**

---

# 13. Expected Carbon Footprint

При scheduling у нас ещё нет будущих фактических значений.

Поэтому:

$$
\widehat{CF}
=
\sum_{i=1}^{N}
\widehat{P_i}
\Delta t
\widehat{CI_i}
$$

где `^` означает прогноз.

Если прогноз отсутствует:

$$
\widehat{CI_i}=CI_{current}
$$

Но этот режим имеет более низкую точность.

---

# 14. Оценка runtime

Для Pod вводим:

$$
\hat T_{pod}
$$

Её можно получить:

### В простом эксперименте

из benchmark:

```text
workload class → expected runtime
```

Например:

```text
cpu-benchmark-small → 60 sec
cpu-benchmark-medium → 300 sec
cpu-benchmark-heavy → 1800 sec
```

### В реальной системе

из исторических данных:

$$
\hat T=f(CPU_{request}, workload\_class, node\_type)
$$

Для диплома достаточно первого варианта.

---

# 15. Модель стоимости

Я бы сделал стоимость **опциональным критерием**, а не обязательным.

Если node имеет:

$$
C_{kWh}
$$

то:

$$
Cost=E\cdot C_{kWh}
$$

Но если все экспериментальные nodes одинаковы:

$$
C_{kWh}=const
$$

стоимость не добавляет смысла.

Поэтому:

> **Cost включается только в эксперимент с различными типами nodes.**

---

# 16. SLA

Вводим:

$$
T_{deadline}
$$

и ожидаемое время:

$$
\hat T
$$

Если:

$$
\hat T>T_{deadline}
$$

node не должна получать максимальный экологический score.

Можно использовать hard constraint:

$$
\hat T \leq T_{deadline}
$$

Это очень важно.

**Экология не должна ломать SLA.**

---

# 17. Допустимое множество nodes

До scoring:

$$
N_{feasible}
=
\{n\in N:
Resources(n)\geq Resources(p)
\}
$$

плюс стандартные Kubernetes constraints:

* taints;
* tolerations;
* node affinity;
* pod affinity;
* topology;
* volumes;
* unschedulable;
* etc.

Именно стандартный Kubernetes Filter должен продолжать отвечать за эти вещи.

---

# 18. Нормализация

Нам нужно привести разные показатели к диапазону:

$$
[0,1]
$$

Для показателя, который **лучше минимизировать**:

$$
Norm(x)=
\frac{x-x_{min}}
{x_{max}-x_{min}}
$$

Тогда:

* 0 — лучший;
* 1 — худший.

Для scoring, где больше лучше:

$$
Score_x=1-Norm(x)
$$

---

# 19. Carbon score

$$
S_C=1-
\frac{\widehat{CF}-CF_{min}}
{CF_{max}-CF_{min}}
$$

---

# 20. Resource score

Например:

$$
U_{cpu}=
\frac{CPU_{used}+CPU_{request}}
{CPU_{allocatable}}
$$

и:

$$
U_{ram}=
\frac{RAM_{used}+RAM_{request}}
{RAM_{allocatable}}
$$

Затем:

$$
U=
\lambda U_{cpu}+(1-\lambda)U_{ram}
$$

И:

$$
S_R=1-U
$$

Но здесь я бы **не заменял стандартный NodeResourcesFit**.

Это дополнительный preference.

---

# 21. SLA score

Например:

$$
S_{SLA}=
\begin{cases}
1,&\hat T\leq T_{target}\\
e^{-k(\hat T-T_{target})},&\hat T>T_{target}
\end{cases}
$$

Но ещё лучше сделать SLA **hard constraint**, а не soft score.

То есть:

```text
если SLA impossible → node исключается
```

---

# 22. Итоговая функция

Итог:

$$
S(n)=
w_C S_C(n)
+
w_R S_R(n)
+
w_T S_T(n)
+
w_K S_K(n)
$$

где:

* \(S_C\) — carbon;
* \(S_R\) — resource efficiency;
* \(S_T\) — performance/SLA;
* \(S_K\) — cost;
* \(w_i\) — веса.

Причём:

$$
\sum w_i=1
$$

---

# 23. Режимы

Это хорошее дополнение.

### Carbon-first

$$
w_C=0.7
$$

### Balanced

$$
w_C=0.5
$$

### Performance-first

$$
w_C=0.2
$$

Например:

```yaml
policy:
  carbonWeight: 0.6
  resourceWeight: 0.2
  performanceWeight: 0.2
```

---

# 24. Но Kubernetes Score имеет свой диапазон

Scheduler plugin должен вернуть нормализованный score, а Kubernetes объединяет оценки Score plugins согласно configured weights. ([Kubernetes][1])

Поэтому внутри plugin:

$$
S\rightarrow[0,100]
$$

например:

```text
0   = worst
100 = best
```

А затем Scheduler Framework учитывает plugin weight.

---

# 25. Главный алгоритм

```text
Input:
    Pod P
    Nodes N
    CarbonState C
    Configuration W

1. Receive Pod

2. Standard Kubernetes Filter:
       remove nodes that violate:
       - CPU
       - RAM
       - taints
       - affinity
       - topology
       - storage
       - etc.

3. If N_feasible == ∅:
       return Unschedulable

4. PreScore:
       load carbon data
       load forecast
       load energy model
       estimate runtime

5. For every node n:

       zone = zone(n)

       carbon = carbon(zone)

       runtime = estimate_runtime(P, n)

       if runtime > SLA:
            mark node unsuitable

       energy = estimate_energy(P, n)

       carbonFootprint =
            integrate(energy(t) * carbon(t))

       carbonScore =
            normalize(carbonFootprint)

       resourceScore =
            calculate_resource_score(n, P)

       performanceScore =
            calculate_performance_score(n, P)

       totalScore =
            wC * carbonScore
          + wR * resourceScore
          + wP * performanceScore

6. Normalize total scores to [0,100]

7. Kubernetes selects highest score

8. Reserve / Bind

9. Export decision metrics

10. Workload runs

11. Actual metrics collected

12. Actual estimated carbon footprint calculated
```

---

# 26. Псевдокод

```text
function Score(pod, node):

    zone = node.carbonZone

    carbonForecast = CarbonCache.get(zone)

    runtime = RuntimeModel.predict(pod, node)

    if runtime > pod.SLADeadline:
        return 0

    energy = EnergyModel.predict(
        node,
        pod,
        runtime
    )

    carbonFootprint = 0

    for interval in carbonForecast:
        power = energy.power(interval)
        carbon = interval.carbonIntensity

        carbonFootprint +=
            power *
            interval.duration *
            carbon

    carbonScore =
        normalize_inverse(
            carbonFootprint
        )

    resourceScore =
        ResourceModel.score(
            node,
            pod
        )

    performanceScore =
        PerformanceModel.score(
            node,
            pod
        )

    return
        100 * (
            Wc * carbonScore +
            Wr * resourceScore +
            Wp * performanceScore
        )
```

---

# 27. Что делать при изменении Carbon Intensity

Здесь важно не совершить ошибку.

**Не надо автоматически двигать все Pods.**

Если:

```text
North: 80 → 400
```

уже запущенный Pod остаётся там.

Для нового Pod:

```text
scheduler sees 400
```

и выбирает другую node.

Это соответствует реальной модели Kubernetes.

---

# 28. А если хочется rescheduling?

Можно сделать **отдельный экспериментальный controller**, который:

```text
detects carbon deterioration
       ↓
checks workload is movable
       ↓
checks minimum carbon benefit
       ↓
checks disruption budget
       ↓
recreates workload
```

Но я бы обозначил это:

> **Optional extension: carbon-aware workload relocation.**

Не основной частью диплома.

Kubernetes сам по себе не выполняет live migration обычного Pod между nodes; перемещение обычно означает завершение/пересоздание workload контроллером. ([Discuss Kubernetes][4])

---

# 29. Anti-thrashing

Если всё-таки делать relocation:

вводим:

$$
\Delta CF_{min}
$$

и:

$$
T_{cooldown}
$$

Перемещение разрешается только если:

$$
ExpectedSaving>\Delta CF_{min}
$$

и:

$$
time_{since\_last\_move}>T_{cooldown}
$$

Например:

```text
minimum carbon benefit = 15%
cooldown = 30 min
```

---

# 30. Workload classification

Очень полезно ввести два типа:

### Flexible

```yaml
carbonAware: true
```

Например:

* batch;
* ML training;
* rendering;
* backup;
* ETL.

### Latency-sensitive

```yaml
carbonAware: false
```

Например:

* API;
* database;
* realtime service.

Это фундаментальный принцип.

**Не вся нагрузка должна оптимизироваться по carbon.**

---

# 31. Как передавать policy

Я бы **не создавал CRD на первом этапе**.

Простейший вариант:

```yaml
metadata:
  labels:
    carbon-aware.io/enabled: "true"
    carbon-aware.io/class: "batch"
```

и annotations:

```yaml
annotations:
  carbon-aware.io/carbon-weight: "0.7"
  carbon-aware.io/sla-seconds: "1800"
```

Но потом, если хочется сделать API красивее:

```yaml
apiVersion: carbon-aware.io/v1alpha1
kind: CarbonPolicy
metadata:
  name: batch-default
spec:
  carbonWeight: 0.7
  performanceWeight: 0.3
  maxCarbonDataAge: 300
```

### Моя рекомендация

**CRD не делать обязательным.**

Для диплома достаточно ConfigMap + Pod annotations.

CRD можно реализовать как расширение.

---

# 32. Модель данных

## CarbonRecord

```text
CarbonRecord
------------
zone
timestamp
carbonIntensity
carbonFreePercentage
source
quality
expiresAt
```

---

## NodeProfile

```text
NodeProfile
-----------
nodeName
zone
cpuCapacity
memoryCapacity
cpuUtilization
memoryUtilization
powerIdle
powerMax
```

---

## WorkloadProfile

```text
WorkloadProfile
---------------
namespace
pod
workloadClass
cpuRequest
memoryRequest
expectedRuntime
sla
carbonAware
```

---

## SchedulingDecision

```text
SchedulingDecision
------------------
pod
timestamp
selectedNode

carbonScore
resourceScore
performanceScore
totalScore

estimatedEnergy
estimatedCarbon

alternatives
```

---

# 33. Метрики Prometheus

Я бы сделал:

```text
carbon_aware_node_carbon_intensity_gco2_kwh
```

```text
carbon_aware_node_estimated_power_watts
```

```text
carbon_aware_pod_estimated_energy_kwh
```

```text
carbon_aware_pod_estimated_carbon_gco2
```

```text
carbon_aware_scheduler_score
```

```text
carbon_aware_scheduler_decisions_total
```

```text
carbon_aware_scheduler_fallback_total
```

```text
carbon_aware_data_age_seconds
```

```text
carbon_aware_plugin_duration_seconds
```

---

# 34. Kubernetes уже предоставляет часть нужных метрик

Это хорошо.

Актуальный Kubernetes публикует метрики scheduler, включая:

* scheduling attempt duration;
* scheduling algorithm duration;
* plugin execution duration;
* pending pods;
* scheduling attempts;
* SLI scheduling latency.

([Kubernetes][5])

То есть не нужно самостоятельно изобретать всю систему измерения scheduler performance.

---

# 35. CPU/RAM

Для workload/node utilization можно использовать Kubernetes metrics pipeline.

Kubelet предоставляет `/metrics/resource`, а Metrics API предоставляет CPU/memory usage; в Kubernetes 1.37 `metrics.k8s.io` API получил stable `v1`. ([Kubernetes][6])

Для диплома я бы:

```text
Prometheus
    +
kube-state-metrics
    +
kubelet/cAdvisor
```

использовал для исторической аналитики.

А scheduler для принятия решения использует **состояние scheduler cache и Pod requests**, а не делает HTTP-запрос в Prometheus во время каждого `Score`.

Это принципиально важно для производительности.

---

# 36. Почему scheduler не должен ходить в Prometheus

Плохой вариант:

```text
Score(node)
   ↓
HTTP request Prometheus
   ↓
wait
```

Это может вызываться для множества nodes.

Правильнее:

```text
Prometheus
    ↓
Metrics Collector
    ↓
cached NodeState
    ↓
Scheduler Plugin
```

или использовать scheduler state там, где данные уже доступны.

---

# 37. Получение Carbon Intensity

Внешний источник:

### Electricity Maps

Можно использовать как production-like источник.

Carbon intensity измеряется в:

$$
gCO_2eq/kWh
$$

и доступные исторические наборы имеют различные временные гранулярности. ([Electricity Maps][7])

Но архитектура должна поддерживать:

```text
ElectricityMapsProvider
MockProvider
HistoricalDatasetProvider
```

---

# 38. Это очень важно для экспериментов

Реальный API плохо подходит для научного воспроизведения.

Допустим сегодня:

```text
North = 70
West = 400
```

а завтра:

```text
North = 250
West = 150
```

Получишь другой результат.

Поэтому эксперимент должен использовать **replayable dataset**.

---

# 39. Идеальная схема эксперимента

```text
Historical Carbon Dataset
          ↓
Replay Engine
          ↓
Carbon Data Service
          ↓
Scheduler
```

Replay Engine воспроизводит:

```text
10:00 → 100
10:05 → 120
10:10 → 300
10:15 → 280
...
```

Таким образом можно повторить эксперимент.

---

# 40. Реальный API используется для демонстрации

На защите:

```text
Live mode
```

А в исследовании:

```text
Replay mode
```

Это **намного профессиональнее**.

---

# 41. Energy Model calibration

Нужно определить:

$$
P_{idle}
$$

и:

$$
P_{max}
$$

Есть несколько вариантов.

### Вариант A — теоретический

Задать параметры по hardware specification.

### Вариант B — измерить физически

Если лабораторная машина доступна:

```text
idle
25%
50%
75%
100%
```

и измерить потребление внешним ваттметром.

### Вариант C — RAPL

Если CPU/OS позволяет:

```text
Intel RAPL
```

Но для VM это может быть недоступно.

### Для диплома

Я бы выбрал:

> **физическое измерение host + калибровка модели для экспериментальных сценариев.**

Если невозможно — использовать синтетические параметры и обязательно провести sensitivity analysis.

---

# 42. Ключевая проблема с энергопотреблением

Нельзя говорить:

> «мы измеряем энергопотребление Pod».

Правильнее:

> «мы оцениваем энергопотребление node и распределяем его вклад между workload согласно модели».

Это существенное научное отличие.

---

# 43. Модель attribution

Для Pod:

$$
P_{pod}=P_{idleShare}+P_{dynamic}
$$

Можно использовать CPU share:

$$
P_{dynamic}
=
(P_{node}-P_{idle})
\frac{CPU_{pod}}
{CPU_{node}}
$$

Но это только **модель атрибуции**.

Для сравнения scheduler лучше использовать **общую энергию cluster**, потому что именно она физически имеет смысл.

---

# 44. Поэтому я бы определил два KPI

### Cluster Carbon

$$
CF_{cluster}
$$

Главный.

### Workload Estimated Carbon

$$
\widehat{CF}_{pod}
$$

Вспомогательный.

Так диплом будет методологически аккуратнее.

---

# 45. Экспериментальная инфраструктура

Например:

```text
kind / kubeadm
```

Я бы выбрал:

### `kind`

потому что:

* легко автоматизировать;
* удобно создавать несколько worker nodes;
* хорошо подходит для CI;
* легко воспроизводится.

Например:

```text
control-plane

worker-01
worker-02
worker-03
worker-04
```

---

# 46. Логические energy zones

```text
worker-01 → north
worker-02 → north

worker-03 → west
worker-04 → west
```

Каждая зона имеет:

```text
carbonIntensity
powerModel
```

---

# 47. Node labels

```yaml
labels:
  carbon-aware.io/zone: north
```

Можно также:

```yaml
carbon-aware.io/power-profile: standard
```

---

# 48. Scheduler configuration

Например:

```yaml
apiVersion: kubescheduler.config.k8s.io/v1
kind: KubeSchedulerConfiguration

profiles:
  - schedulerName: carbon-aware-scheduler

    plugins:
      multiPoint:
        enabled:
          - name: CarbonAware
```

Конкретная конфигурация должна соответствовать версии Kubernetes, которую ты зафиксируешь в дипломе; актуальная конфигурация использует `kubescheduler.config.k8s.io/v1`. ([Kubernetes][8])

---

# 49. Я бы использовал второй scheduler profile

Не заменять стандартный сразу.

```text
default-scheduler
carbon-aware-scheduler
```

Тогда:

```yaml
spec:
  schedulerName: carbon-aware-scheduler
```

выбирает экспериментальный scheduler.

А baseline:

```yaml
spec:
  schedulerName: default-scheduler
```

Это великолепно для экспериментов.

Можно запускать оба сценария в одинаковом cluster.

---

# 50. Почему это лучше для защиты

Ты можешь буквально показать:

```text
same cluster
same workload
same nodes
same carbon data
```

### Experiment A

```text
default-scheduler
```

### Experiment B

```text
carbon-aware-scheduler
```

Разница только в алгоритме.

Это существенно повышает валидность эксперимента.

---

# 51. Технологический стек

## Core

### Go

Обязателен.

Потому что Kubernetes Scheduler Framework написан на Go.

---

## Kubernetes

Например:

```text
Kubernetes 1.37.x
```

На момент подготовки диплома конкретную patch-версию надо зафиксировать.

---

## Scheduler Framework

Основная технология проекта.

---

## Prometheus

Для:

* time series;
* scheduler metrics;
* workload metrics;
* carbon metrics.

---

## Grafana

Для:

* dashboards;
* comparison;
* experiments.

---

## Python

**Только для анализа экспериментов.**

Например:

```text
pandas
numpy
matplotlib
scipy
```

Не нужно писать часть production scheduler на Python.

---

## Database

Я бы **не использовал PostgreSQL**.

Для диплома достаточно:

```text
Prometheus
```

для временных рядов.

Если нужен historical metadata store:

```text
SQLite
```

или обычные CSV/Parquet datasets для experiment replay.

---

# 52. REST API

Carbon Data Service:

```text
GET /api/v1/zones
GET /api/v1/zones/{zone}/current
GET /api/v1/zones/{zone}/forecast
GET /api/v1/health
```

---

# 53. Containerization

Docker/OCI.

Образы:

```text
carbon-aware-scheduler
carbon-data-service
workload-generator
```

---

# 54. Deployment

Для лаборатории:

```text
kind
+
Helm
```

Но Helm **не обязателен**.

Можно начать с:

```text
Kubernetes manifests
```

и сделать Helm только если остаётся время.

---

# 55. CRD

Не обязательна.

### MVP

```text
ConfigMap
+
Pod labels/annotations
```

### Advanced

```text
CarbonPolicy CRD
```

Я бы CRD реализовал только после полностью работающего scheduler.

---

# 56. Безопасность

## Scheduler ServiceAccount

Минимальные права:

* чтение Nodes;
* чтение Pods;
* scheduling/binding;
* чтение необходимых ресурсов.

Не давать:

```text
cluster-admin
```

---

# 57. Carbon Service

Если внешний API требует API key:

```text
Secret
```

а не:

```text
ConfigMap
```

---

# 58. NetworkPolicy

Разрешить:

```text
Scheduler → Carbon Service
Carbon Service → external API
Prometheus → exporters
Grafana → Prometheus
```

Остальное закрыть.

---

# 59. TLS

В лабораторной среде можно использовать внутренний cluster networking.

Но внешние API:

```text
HTTPS
```

обязательно.

---

# 60. Edge case: Carbon API недоступен

Нельзя сделать:

```text
API unavailable → scheduler broken
```

Правильный fallback:

```text
fresh cache
     ↓
stale cache
     ↓
default scheduling
```

---

# 61. Freshness

Например:

```yaml
maxDataAgeSeconds: 300
```

Если:

$$
age>300
$$

carbon score отключается.

Scheduler возвращается к baseline behaviour.

---

# 62. Prometheus недоступен

Scheduler **не должен падать**.

Используем:

```text
last known state
```

или:

```text
disable dynamic utilization factor
```

---

# 63. Некорректный Carbon Intensity

Проверки:

```text
CI < 0 → invalid
timestamp in future → invalid
NaN → invalid
zone unknown → invalid
```

---

# 64. Резкое изменение Carbon Intensity

Допустим:

```text
100 → 900
```

Это не должно автоматически мигрировать workload.

Для **нового Pod** используется новое значение.

Для уже работающего:

```text
continue
```

если relocation не входит в расширенный режим.

---

# 65. Все nodes перегружены

Стандартный Kubernetes Scheduler:

```text
Unschedulable
```

Carbon plugin не должен пытаться:

> «выбрать самый зелёный перегруженный node».

Resource feasibility — **hard constraint**.

---

# 66. Новый node

Scheduler автоматически увидит новый Node через cluster state.

Carbon zone определяется:

```text
label
```

Если zone неизвестна:

```text
carbon score = neutral
```

или node исключается для carbon-aware workloads — зависит от policy.

Я бы:

> неизвестную carbon zone не исключать, а давать нейтральный score.

Иначе появление нового node может неожиданно блокировать scheduling.

---

# 67. Удаление node

Обычное Kubernetes поведение.

Carbon Service просто перестаёт получать состояние node.

---

# 68. Scheduler crash

Kubernetes control plane должен восстановить scheduler.

Но важнее:

> carbon-aware plugin не должен хранить критическое состояние, потеря которого ломает scheduling.

Поэтому plugin должен быть максимально stateless.

---

# 69. Критически важное архитектурное правило

**Carbon optimization никогда не должна быть единственным механизмом scheduler.**

То есть:

```text
Kubernetes constraints
        ↓
mandatory
        ↓
Carbon optimization
        ↓
preference
```

Не наоборот.

---

# 70. Repository

Я бы сделал:

```text
carbon-aware-k8s/
│
├── cmd/
│   ├── scheduler/
│   │   └── main.go
│   │
│   └── carbon-service/
│       └── main.go
│
├── pkg/
│   ├── scheduler/
│   │   ├── plugin.go
│   │   ├── pre_score.go
│   │   ├── score.go
│   │   ├── normalize.go
│   │   └── config.go
│   │
│   ├── carbon/
│   │   ├── provider.go
│   │   ├── electricity_maps.go
│   │   ├── replay.go
│   │   └── cache.go
│   │
│   ├── energy/
│   │   ├── model.go
│   │   └── calibration.go
│   │
│   ├── runtime/
│   │   └── predictor.go
│   │
│   └── metrics/
│       └── prometheus.go
│
├── api/
│   └── v1alpha1/
│
├── deployments/
│   ├── kubernetes/
│   ├── prometheus/
│   └── grafana/
│
├── experiments/
│   ├── workloads/
│   ├── datasets/
│   ├── scenarios/
│   └── results/
│
├── analysis/
│   ├── notebooks/
│   └── scripts/
│
├── tests/
│   ├── unit/
│   ├── integration/
│   └── e2e/
│
├── docs/
│   ├── architecture.md
│   ├── model.md
│   └── experiments.md
│
├── Dockerfile
├── Makefile
├── go.mod
└── README.md
```

---

# 71. Testing

## Unit

Проверяем:

```text
Carbon normalization
Energy model
Carbon calculation
Score calculation
Fallback
Data validation
```

---

## Integration

```text
Carbon Service
       ↓
Scheduler Plugin
       ↓
Kubernetes
```

---

## E2E

Сценарий:

```text
create Pod
↓
scheduler
↓
node selected
↓
verify node
```

---

# 72. Experiment №1 — Baseline

Создать:

```text
default scheduler
```

100 одинаковых workload.

Получить:

```text
total energy
total carbon
runtime
```

---

# 73. Experiment №2 — Carbon-aware

То же самое:

```text
same workloads
same nodes
same carbon dataset
```

но:

```text
carbon-aware scheduler
```

---

# 74. Experiment №3 — Different weights

Запустить:

```text
carbonWeight = 0.0
0.25
0.5
0.75
1.0
```

Получишь кривую:

```text
Carbon ↓
          \
           \
            \
             \____
                  Runtime →
```

Это может стать **одним из главных графиков диплома**.

---

# 75. Experiment №4 — SLA

Например:

```text
SLA = 10 min
SLA = 20 min
SLA = 30 min
```

Исследовать:

```text
carbon reduction
vs
SLA constraint
```

---

# 76. Experiment №5 — Carbon volatility

Сделать сценарии:

### Stable

```text
100 → 110 → 105 → 100
```

### Variable

```text
100 → 400 → 80 → 500
```

### Highly volatile

```text
50 → 700 → 100 → 800
```

И проверить robustness.

---

# 77. Experiment №6 — API failure

```text
API available
API unavailable
stale data
invalid data
```

Измерить:

```text
scheduler availability
fallback decisions
```

---

# 78. Experiment №7 — Scheduler overhead

Сравнить:

```text
default scheduler
vs
carbon-aware scheduler
```

по:

$$
SchedulingLatency
$$

Kubernetes уже предоставляет соответствующие scheduler metrics, включая execution duration для plugin и scheduling attempt latency. ([Kubernetes][5])

---

# 79. Experiment №8 — Number of nodes

Например:

```text
3
5
10
20
```

И измерить:

$$
T_{score}
$$

Это показывает масштабируемость алгоритма.

---

# 80. KPI

## Carbon Reduction

$$
CR=
\frac{CF_{baseline}-CF_{carbon}}
{CF_{baseline}}
\times100\%
$$

---

## Energy Reduction

$$
ER=
\frac{E_{baseline}-E_{carbon}}
{E_{baseline}}
\times100\%
$$

---

## Runtime degradation

$$
RD=
\frac{T_{carbon}-T_{baseline}}
{T_{baseline}}
\times100\%
$$

---

## Scheduling overhead

$$
SO=
\frac{L_{carbon}-L_{baseline}}
{L_{baseline}}
\times100\%
$$

---

# 81. SLA violation rate

$$
SVR=
\frac{N_{violations}}
{N_{workloads}}
\times100\%
$$

Очень хороший KPI.

---

# 82. Carbon efficiency

Можно ввести:

$$
CE=
\frac{CarbonReduction}
{RuntimeDegradation}
$$

Но использовать осторожно: при runtime degradation около нуля показатель становится нестабильным.

Лучше использовать его как дополнительный показатель.

---

# 83. Scheduling success rate

$$
SSR=
\frac{N_{scheduled}}
{N_{submitted}}
\times100\%
$$

---

# 84. Data freshness

$$
Freshness=t_{now}-t_{data}
$$

---

# 85. Fallback rate

$$
FR=
\frac{N_{fallback}}
{N_{decisions}}
\times100\%
$$

---

# 86. Какие графики должны быть в дипломе

Минимально:

### График 1

Carbon intensity по времени.

### График 2

Carbon footprint:

```text
Default
vs
Carbon-aware
```

### График 3

Runtime:

```text
Default
vs
Carbon-aware
```

### График 4

Carbon reduction vs weight.

### График 5

SLA violation rate.

### График 6

Scheduling latency.

### График 7

Energy consumption.

### График 8

Node utilization.

---

# 87. Самый важный график

Я бы сделал:

## Pareto frontier

X:

$$
Runtime
$$

Y:

$$
Carbon
$$

Каждая точка:

```text
different weight configuration
```

Получится:

```text
Carbon
  ↑
  │ ●
  │   ●
  │      ●
  │         ●
  │             ●
  └────────────────→ Runtime
```

Это очень хорошо демонстрирует trade-off.

---

# 88. Baseline

Нужно минимум два baseline.

### Baseline 1

Стандартный:

```text
default-scheduler
```

### Baseline 2

Простой carbon-only:

```text
min CarbonIntensity
```

Это очень полезно.

Потому что ты тогда доказываешь:

> мой алгоритм лучше не только default scheduler, но и наивной стратегии «всегда выбирай самый зелёный node».

---

# 89. Ещё лучше

Три стратегии:

```text
A — Default
B — Carbon-only
C — Multi-objective Carbon-aware
```

И:

```text
A → baseline
B → naive optimization
C → proposed algorithm
```

Это уже очень хорошая экспериментальная постановка.

---

# 90. Научная новизна

Здесь надо быть осторожным.

**Не заявляй:**

> «Я впервые придумал carbon-aware Kubernetes scheduler».

Это почти наверняка невозможно доказать.

Правильнее:

> разработан и экспериментально исследован алгоритм многокритериального углеродо-ориентированного планирования вычислительных workloads в Kubernetes, учитывающий динамическую углеродную интенсивность, оценку энергопотребления, ресурсные ограничения и SLA.

Новизна может быть:

### 1.

Комбинация:

$$
Carbon + Energy + SLA + Resources
$$

### 2.

Использование прогнозируемого carbon footprint:

$$
\widehat{CF}
$$

вместо текущего:

$$
CI_{current}
$$

### 3.

Адаптивный scoring.

### 4.

Экспериментальное исследование trade-off.

---

# 91. Что реально защищать

На защите:

> **Разработан scheduler plugin для Kubernetes.**

> **Разработана модель оценки энергопотребления nodes.**

> **Разработана модель расчёта ожидаемого carbon footprint.**

> **Разработан многокритериальный алгоритм выбора node.**

> **Разработана система мониторинга.**

> **Проведено экспериментальное сравнение с default scheduler и carbon-only strategy.**

> **Получена количественная оценка снижения carbon footprint и влияния на SLA.**

Вот это хороший набор тезисов.

---

# 92. Самое слабое место проекта

Теперь критика.

### №1 — энергия

Это **главная научная проблема**.

Если ты просто скажешь:

> CPU 50% → 70 W

комиссия может спросить:

> «Почему 70?»

Поэтому нужно:

**либо**

реально измерить:

```text
idle
25%
50%
75%
100%
```

**либо**

чётко обосновать calibration model.

---

# 93. Второе слабое место — Carbon API

Если ты будешь использовать live API и говорить:

> «мы доказали снижение выбросов»

это методологически слабее.

Поэтому:

```text
Live API → demonstration
Historical/replay dataset → experiments
```

---

# 94. Третье слабое место — региональность

Не говори:

> Kubernetes Scheduler переносит Pod между AWS regions.

Говори:

> В экспериментальной модели nodes принадлежат различным логическим энергетическим зонам, характеризующимся различной динамикой углеродной интенсивности.

Это абсолютно корректно.

---

# 95. Четвёртое слабое место — «экология всегда лучше»

Нет.

Если:

```text
Node A:
50 gCO₂/kWh
runtime 2h

Node B:
200 gCO₂/kWh
runtime 5min
```

возможно B имеет меньший total carbon footprint.

Поэтому:

> **нельзя оптимизировать только Carbon Intensity.**

Именно поэтому тебе нужна:

$$
CF=P\times T\times CI
$$

а не просто:

$$
CI
$$

---

# 96. Пятое слабое место — HPA

Я бы **убрал Carbon-Aware HPA из основного диплома**.

Почему?

HPA отвечает на:

> «сколько реплик нужно?»

Carbon scheduler отвечает:

> «куда разместить workload?»

Если заставить HPA менять replicas из-за carbon intensity, можно получить странную семантику:

```text
грязная энергия
↓
уменьшаем replicas
↓
производительность падает
```

Это может быть допустимо для batch workloads, но требует совершенно другой SLA-модели.

### Решение

В дипломе:

> Carbon-aware HPA — перспективное направление дальнейшей работы.

---

# 97. Шестое слабое место — relocation

Тоже убрать из MVP.

Основная система:

```text
new Pod
→
optimal node
```

Не:

```text
existing Pod
→
migration
```

---

# 98. Седьмое слабое место — прогноз

Если прогноз будет слишком сложным, проект может уйти в ML.

Я бы **не строил нейросеть для carbon forecasting**.

Для диплома:

```text
external forecast
```

или:

```text
historical replay
```

достаточно.

Если нужен fallback:

### persistence model

$$
\hat{CI}(t+\Delta)=CI(t)
$$

Простейшая, но прозрачная.

---

# 99. Что я бы убрал окончательно

Из обязательной версии:

❌ multi-cloud federation

❌ настоящий multi-region Kubernetes

❌ ML prediction

❌ carbon-aware HPA

❌ live migration

❌ PostgreSQL

❌ сложная distributed database

❌ собственный Kubernetes scheduler с нуля

❌ service mesh

❌ Kafka

❌ Redis, если он не нужен для cache

---

# 100. Что обязательно оставить

✅ Kubernetes Scheduler Framework

✅ собственный Go plugin

✅ Carbon Data Service

✅ Carbon Intensity

✅ energy model

✅ carbon footprint model

✅ multi-objective scoring

✅ Prometheus

✅ Grafana

✅ replayable datasets

✅ baseline comparison

✅ SLA analysis

---

# 101. Итоговый MVP

Если всё остальное убрать, диплом всё ещё остаётся полноценным:

```text
                Carbon Dataset/API
                       │
                       ▼
              Carbon Data Service
                       │
                       ▼
               Carbon State Cache
                       │
                       ▼
                Kubernetes
                       │
               ┌───────▼───────┐
               │ kube-scheduler│
               │               │
               │ CarbonAware   │
               │ Plugin        │
               └───────┬───────┘
                       │
              ┌────────┼────────┐
              ▼        ▼        ▼
            Node A   Node B   Node C
            400 CI    80 CI    250 CI
                       │
                       ▼
                    Workload
                       │
                       ▼
                  Prometheus
                       │
                       ▼
                    Grafana
```

---

# 102. Этап 1 — исследование

### Сделать

* Kubernetes Scheduler Framework;
* Green Computing;
* Carbon-aware scheduling;
* energy estimation;
* carbon accounting.

### Результат

Глава:

> Анализ предметной области и существующих решений.

---

# 103. Этап 2 — проектирование

### Сделать

* architecture diagram;
* component diagram;
* sequence diagram;
* data model;
* API.

### Результат

Полная техническая архитектура.

---

# 104. Этап 3 — математическая модель

### Реализовать

$$
P(u)
$$

$$
E=P\cdot T
$$

$$
CF=E\cdot CI
$$

$$
CF=\sum P_i\Delta t_iCI_i
$$

$$
Score=w_CS_C+w_RS_R+w_PS_P
$$

### Результат

Формальная модель.

---

# 105. Этап 4 — Carbon Service

### Реализовать

```text
Provider
Cache
Validation
Replay
REST API
Metrics
```

### Результат

Рабочий сервис.

---

# 106. Этап 5 — Scheduler Plugin

### Сначала

```text
Score(node)
```

с фиктивным carbon data.

### Затем

```text
Carbon Service
```

### Потом

```text
Energy Model
```

### Потом

```text
SLA
```

---

# 107. Этап 6 — Kubernetes integration

```text
kind
+
custom scheduler image
+
scheduler profile
+
RBAC
+
workloads
```

Результат:

> реальный Kubernetes cluster, в котором Pod действительно размещается разработанным алгоритмом.

---

# 108. Этап 7 — monitoring

Prometheus:

```text
metrics
```

Grafana:

```text
dashboard
```

---

# 109. Этап 8 — testing

```text
unit
integration
e2e
failure scenarios
```

---

# 110. Этап 9 — experiments

Минимум:

```text
Default
Carbon-only
Proposed
```

на нескольких:

```text
carbon profiles
workload types
SLA levels
cluster sizes
```

---

# 111. Этап 10 — анализ

Python:

```text
pandas
numpy
matplotlib
```

Статистика:

* mean;
* median;
* p95;
* confidence intervals, если объём данных позволяет.

Не стоит делать вывод:

> «carbon reduction = 27.4%»

по одному запуску.

Нужно:

```text
N repetitions
```

---

# 112. Какой workload использовать

Я бы сделал три:

### CPU-bound

Например:

```text
численное вычисление
```

### Memory-intensive

```text
memory benchmark
```

### Batch

```text
длительная вычислительная задача
```

Основной эксперимент — CPU-bound, потому что энергетическая модель проще.

---

# 113. Почему не ML training

ML training красиво звучит, но для диплома это не нужно.

Можно использовать его **как демонстрационный workload**, но не делать ML частью архитектуры.

---

# 114. Что показать на защите

Я бы построил защиту вокруг одного сценария.

На экране:

```text
Node             Carbon
--------------------------------
north-01          85
central-01       260
west-01          410
```

Создаёшь:

```yaml
carbonAware: true
```

Scheduler:

```text
Filter:
north ✓
central ✓
west ✓

Score:
north = 92
central = 61
west = 34
```

Результат:

```text
Pod → north-01
```

Затем меняешь replay dataset:

```text
north = 450
central = 250
west = 70
```

Создаёшь **второй Pod**:

```text
Pod → west-01
```

Grafana показывает:

```text
Carbon ↓
Energy ↓
Runtime
Scheduling latency
```

Это очень сильная демонстрация.

---

# 115. Вопрос комиссии №1

### «Почему нельзя просто использовать nodeSelector?»

Ответ:

> `nodeSelector` является статическим механизмом. Он не позволяет динамически ранжировать nodes в зависимости от изменяющейся Carbon Intensity и ожидаемого carbon footprint workload.

---

# 116. Вопрос №2

### «Почему не использовать обычный scheduler?»

Ответ:

> Стандартный scheduler не использует углеродную интенсивность как критерий оптимизации. Разработанный plugin расширяет стандартный процесс scheduling через Scheduler Framework, не заменяя базовые механизмы проверки ресурсов и ограничений.

---

# 117. Вопрос №3

### «Почему нельзя просто выбрать node с минимальным Carbon Intensity?»

Ответ:

> Потому что carbon footprint зависит не только от Carbon Intensity, но и от энергопотребления и продолжительности выполнения workload:

$$
CF=\int P(t)CI(t)dt
$$

Поэтому минимальная текущая Carbon Intensity не гарантирует минимальный суммарный carbon footprint.

---

# 118. Вопрос №4

### «Откуда вы знаете энергопотребление Pod?»

Ответ:

> Напрямую в виртуализированной лабораторной инфраструктуре оно недоступно с необходимой точностью. Поэтому используется калиброванная модель энергопотребления node, основанная на CPU utilization и параметрах idle/max power. Полученное значение рассматривается как оценка, а не как прямое измерение.

---

# 119. Вопрос №5

### «А если Carbon Intensity изменится через 10 минут?»

Ответ:

> Для длительных workloads используется временной ряд прогнозируемой Carbon Intensity. Ожидаемый carbon footprint вычисляется по интервалам выполнения. При отсутствии прогноза используется текущая интенсивность либо historical replay model. Для уже запущенных Pod автоматическая миграция не является частью базового алгоритма.

---

# 120. Вопрос №6

### «Почему вы не переносите уже запущенный Pod?»

Ответ:

> Kubernetes Scheduler отвечает за первоначальное размещение Pod. Изменение оптимального node после запуска требует отдельного механизма controlled rescheduling, который приводит к disruption workload. Чтобы не нарушать SLA и не смешивать задачи scheduler и workload relocation, базовая система принимает решение только при первоначальном scheduling.

---

# 121. Вопрос №7

### «Что будет, если зелёный node перегружен?»

Ответ:

> Carbon optimization применяется только после выполнения hard scheduling constraints. Если node не удовлетворяет CPU/RAM или другим Kubernetes constraints, она исключается до carbon scoring.

---

# 122. Вопрос №8

### «Что будет при отказе Carbon API?»

Ответ:

> Scheduler не зависит от доступности внешнего API в реальном времени. Carbon Data Service использует cache. При превышении допустимого возраста данных carbon-aware scoring отключается и scheduler возвращается к безопасной baseline strategy.

---

# 123. Вопрос №9

### «Почему вы используете модель, а не реальный датчик?»

Ответ:

> Цель диплома — исследование алгоритма планирования, а не разработка аппаратного измерителя энергопотребления. Модель позволяет контролируемо исследовать влияние scheduling decisions. При наличии аппаратных измерений модель дополнительно калибруется.

---

# 124. Вопрос №10

### «Какая научная новизна?»

Ответ:

> В работе разработан и исследован алгоритм многокритериального carbon-aware scheduling, объединяющий динамическую Carbon Intensity, оценку энергопотребления, ожидаемую продолжительность workload и SLA constraints в единой функции выбора Kubernetes node.

---

# 125. Главный критерий успешности диплома

Не:

> «Я сделал scheduler, который выбирает зелёные nodes».

А:

> **«Я разработал алгоритм, формализовал критерии его работы и экспериментально доказал, в каких условиях он снижает estimated carbon footprint и какой performance/SLA trade-off при этом возникает».**

Это принципиально более сильная работа.

---

# 126. Финальная спецификация

## Название

Я бы окончательно предложил:

> **Разработка алгоритма углеродо-ориентированного планирования вычислительных задач в Kubernetes**

Это лучше твоего первоначального названия: короче, технически точнее и непосредственно указывает на объект разработки.

---

## Цель

Разработать и экспериментально исследовать Kubernetes Scheduler Plugin, который выбирает node для гибких вычислительных workloads с учётом ожидаемого carbon footprint, доступности ресурсов и SLA.

---

## Основные компоненты

```text
1. Carbon Data Service
2. Carbon Provider
3. Carbon Cache
4. Forecast/Replay subsystem
5. Energy Model
6. Runtime Model
7. CarbonAware Scheduler Plugin
8. Prometheus
9. Grafana
10. Kubernetes experimental cluster
```

---

## Kubernetes

```text
Kubernetes 1.37.x
```

с двумя scheduler profiles:

```text
default-scheduler
carbon-aware-scheduler
```

Конкретную patch-версию следует зафиксировать при начале реализации.

---

## Scheduler Plugin

Основные extension points:

```text
PreScore
Score
NormalizeScore
```

Опционально:

```text
Reserve
```

если понадобится runtime state.

**Не использовать QueueSort без крайней необходимости.**

Kubernetes отдельно предупреждает, что `queueSort`, `filter` и `permit` могут существенно влиять на работу scheduler, поэтому для дипломного plugin разумно ограничиться наиболее узким необходимым набором extension points. ([Kubernetes][9])

---

## Основная математическая модель

### Power

$$
P(t)=P_{idle}+
(P_{max}-P_{idle})u(t)
$$

### Energy

$$
E=\int_0^T P(t)dt
$$

### Carbon footprint

$$
CF=
\int_0^T
P(t)CI(t)dt
$$

### Discrete

$$
CF=
\sum_i
P_i\Delta t_iCI_i
$$

### Objective

$$
\boxed{
Score(n)=
w_C S_C(n)+
w_R S_R(n)+
w_P S_P(n)
}
$$

при:

$$
w_C+w_R+w_P=1
$$

и hard constraints:

$$
CPU_{available}\ge CPU_{request}
$$

$$
RAM_{available}\ge RAM_{request}
$$

$$
\hat T\le SLA
$$

плюс стандартные Kubernetes scheduling constraints.

---

# 127. Входные данные

```text
Pod:
    CPU request
    RAM request
    workload class
    expected runtime
    SLA
    carbon policy

Node:
    CPU capacity
    RAM capacity
    utilization
    energy profile
    carbon zone

Carbon:
    current CI
    forecast CI
    timestamp
    freshness
```

---

# 128. Выход

```text
Selected Node
+
Scheduling Score
+
Estimated Energy
+
Estimated Carbon Footprint
+
Decision Reason
```

---

# 129. Главный эксперимент

Три стратегии:

```text
A. Kubernetes default scheduler

B. Carbon-only:
   min Carbon Intensity

C. Proposed:
   Carbon + Energy + Runtime + SLA
```

И сравнение:

```text
Carbon Footprint
Energy
Runtime
Scheduling latency
SLA violations
Resource utilization
```

---

# 130. Ожидаемый результат

Ты **не должен заранее обещать конкретный процент снижения CO₂**.

Правильная формулировка:

> Ожидается снижение оценочного углеродного следа гибких вычислительных workloads относительно стандартной стратегии размещения при допустимом увеличении времени выполнения, причём величина эффекта должна быть определена экспериментально в зависимости от динамики Carbon Intensity, workload и выбранных весов оптимизации.

Это научно корректно.

---

# 131. Ограничения

В дипломе прямо указать:

1. Carbon footprint является **оценочным**, а не прямым физическим измерением.
2. Energy model аппроксимирует энергопотребление инфраструктуры.
3. Логические energy zones моделируют географически распределённые инфраструктуры.
4. Базовый scheduler не выполняет live migration.
5. Carbon-aware scheduling применяется преимущественно к flexible/batch workloads.
6. Качество результата зависит от качества Carbon Intensity data.
7. Прогноз будущей Carbon Intensity имеет неопределённость.
8. Network/storage/embodied carbon не включаются в базовую модель.
9. Стоимость не является обязательным критерием и включается только при наличии различающихся energy profiles.
10. Результаты относятся к заданной экспериментальной модели и не должны автоматически интерпретироваться как точный accounting реального дата-центра.

---

# 132. Моя окончательная оценка проекта

После такой проработки я бы оценивал тему уже не на **9/10**, а примерно так:

| Критерий                                     |     Оценка |
| -------------------------------------------- | ---------: |
| Соответствие Computer Engineering            |  **10/10** |
| Инженерная глубина                           | **9.5/10** |
| Научная составляющая                         | **9.5/10** |
| Kubernetes relevance                         |  **10/10** |
| Возможность сделать прототип                 |   **9/10** |
| Возможность количественно доказать результат | **9.5/10** |
| Демонстрация                                 |  **10/10** |
| Масштаб для диплома                          |   **9/10** |
| Риск scope creep                             |   **7/10** |
| Итог                                         | **9.5/10** |

### И самое главное

**Я бы не расширял проект дальше этой спецификации.**

В частности, я бы **не добавлял сейчас**:

> multi-cluster federation → HPA → ML forecasting → live migration → cloud billing → Kubernetes operator → Kafka → PostgreSQL → service mesh.

Это не сделает диплом пропорционально лучше.

Текущий вариант уже содержит всё, что нужно для серьёзной дипломной работы:

```text
теория
   ↓
математическая модель
   ↓
архитектура
   ↓
Kubernetes Scheduler Framework
   ↓
собственный Go plugin
   ↓
динамические данные
   ↓
energy model
   ↓
carbon model
   ↓
многокритериальная оптимизация
   ↓
реальная Kubernetes-система
   ↓
Prometheus/Grafana
   ↓
эксперимент
   ↓
quantitative comparison
   ↓
научный вывод
```

**Именно такую версию я бы нёс руководителю на утверждение.** Она достаточно сложная, чтобы выглядеть как полноценная работа по компьютерной инженерии, но при этом у каждого компонента есть чёткая функция, а главный результат можно **реально реализовать, измерить и защитить**.

[1]: https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/?utm_source=chatgpt.com "Scheduling Framework | Kubernetes"
[2]: https://portal.electricitymaps.com/datasets/US?utm_source=chatgpt.com "Datasets | Electricity Maps"
[3]: https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/?utm_source=chatgpt.com "Kubernetes Scheduler | Kubernetes"
[4]: https://discuss.kubernetes.io/t/rescheduling-pod-after-scale-up/18967?utm_source=chatgpt.com "Rescheduling pod after scale up - General Discussions - Discuss Kubernetes"
[5]: https://kubernetes.io/docs/reference/instrumentation/metrics/?utm_source=chatgpt.com "Kubernetes Metrics Reference | Kubernetes"
[6]: https://kubernetes.io/docs/concepts/cluster-administration/system-metrics/?utm_source=chatgpt.com "Metrics For Kubernetes System Components | Kubernetes"
[7]: https://portal.electricitymaps.com/datasets/Chile?utm_source=chatgpt.com "Datasets | Electricity Maps"
[8]: https://kubernetes.io/docs/reference/scheduling/config/?utm_source=chatgpt.com "Scheduler Configuration | Kubernetes"
[9]: https://kubernetes.io/docs/concepts/security/hardening-guide/scheduler/?utm_source=chatgpt.com "Hardening Guide - Scheduler Configuration | Kubernetes"
