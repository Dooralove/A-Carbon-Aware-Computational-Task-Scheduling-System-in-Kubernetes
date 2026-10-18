# Quality Gate 7 — валидация мониторинга (Prometheus + Grafana)

- **Дата:** 2026-10-18
- **Роль:** Observability / SRE Engineer
- **Статус:** **PASS**
- **Решение:** **Stage 7 — COMPLETE**; Stage 8 (e2e / chaos) по мониторингу не блокируется
- **Проверяемый переход:** метрики (T7.1) → сбор Prometheus (T7.2) → дашборд Grafana (T7.3) → проверка под нагрузкой 50 Pod (T7.4)
- **Канон:** [`metrics.md`](../ssot/metrics.md) v1.1.0 (аддитивная поправка §2a/§3a, имена v1.0.0 не менялись);
  [ADR-003](../decisions_adr/003_stage7_observability_metrics.md) (статус **proposed**, нужно подтверждение владельца);
  [`01_architecture_ssot.md`](../ssot/01_architecture_ssot.md) v1.1.0 LOCKED (§6.8, §7.1, §7.3)
- **Вход:** [`pkg/metrics/prometheus.go`](../../pkg/metrics/prometheus.go),
  [`deployments/prometheus/`](../../deployments/prometheus/), [`deployments/grafana/`](../../deployments/grafana/),
  скрипт проверки [`scripts/gate7_monitoring_check.sh`](../../scripts/gate7_monitoring_check.sh),
  сырые результаты [`evidence/gate7/`](evidence/gate7/)

## 1. Итоговый вердикт

На kind-кластере `carbon-aware` (Kubernetes 1.37.0, kind v0.33.0, 1 CP + 4 workers) работают Prometheus v3.5.0 и Grafana 12.1.0. Prometheus собирает метрики с обоих kube-scheduler (kind `default-scheduler` и `carbon-aware-scheduler`), CDS, cAdvisor всех узлов и node-exporter.

В финальной серии проверка запускалась 5 раз подряд. В каждом прогоне 50 carbon-aware Pod'ов и 10 baseline Pod'ов. Все критерии выполнены в 5 из 5 прогонов:

| Критерий (T7.4) | Порог | Результат (серия 2, 5 прогонов) |
| --- | --- | --- |
| Счётчик решений совпадает с числом Pod'ов | `increase(carbon_aware_scheduler_decisions_total)` = 50 | **50 / 50 / 50 / 50 / 50** |
| Разбивка по узлам совпадает с фактическим Binding | по каждому узлу | **совпадает** (worker 24, worker2 26 во всех прогонах) |
| Все scrape-таргеты UP | 100 % | **14/14** во всех прогонах |
| Задержка «последний Binding → значение видно в Prometheus» | < 3 с | **1.755 – 2.736 с** (макс. 2.736) |
| Задержка «последний Binding → значение видно через datasource Grafana» | < 3 с | **1.926 – 2.378 с** (макс. 2.378) |
| Все запросы панелей дашборда возвращают данные | нет «No data» | **18/18 запросов** с данными во всех прогонах |

Количество блокеров для Stage 8: **0**.

**QUALITY GATE 7: PASS.** Метрики плагина и CDS экспортируются и собираются, дашборд показывает решения шедулера с задержкой меньше 3 с. Серия 1 провалила критерий задержки из-за перегрузки хоста; это описано в §5 как найденная и устранённая проблема стенда, а не кода.

## 2. Что реализовано

### 2.1. Метрики (T7.1)

Имена совпадают с заданием T7.1 и зафиксированы в `metrics.md` §2a/§3a.

**Плагин `CarbonAware`.** Метрики регистрируются в `legacyregistry` kube-scheduler и отдаются на защищённом порту `:10259/metrics`.

| Метрика | Тип | Labels | Когда меняется |
| --- | --- | --- | --- |
| `carbon_aware_scheduler_decisions_total` | counter | `zone`, `node`, `scoring_mode` | PostBind, после успешного Binding |
| `carbon_aware_pod_estimated_carbon_gco2` | histogram | `zone`, `scoring_mode` | PostBind, если PreScore посчитал CF для выбранного узла |
| `carbon_aware_pod_estimated_energy_kwh` | gauge | `zone`, `scoring_mode` | там же, `P_pod · T̂ / 3.6e6` |
| `carbon_aware_scheduler_fallback_total` | counter | `reason` = `empty`/`dead`/`all_invalid`/`stale` | G-08 и SM-STALE; все причины созданы со значением 0 при старте |
| `carbon_aware_plugin_duration_seconds` | histogram | `extension_point` | PreScore, Score, NormalizeScore, PostBind |

**Carbon Data Service.** Отдельный registry; endpoint `/metrics` на `:8080` дописывает эти семейства после существующих `carbon_aware_cds_*`.

| Метрика | Тип | Labels | Источник |
| --- | --- | --- | --- |
| `carbon_aware_zone_intensity_gco2_kwh` | gauge | `zone` | кэш CDS в момент scrape; только пригодные для скоринга записи |
| `carbon_aware_data_age_seconds` | gauge | — | то же значение, что `dataAgeSeconds` в `/healthz` |
| `carbon_aware_api_requests_total` | counter | `endpoint`, `code` | обёртка над каждым маршрутом REST |

Для решений понадобилась новая точка расширения **PostBind**. Она информационная: не возвращает Status, не делает I/O и не влияет на выбор узла. Только после Binding счётчик решений равен числу размещённых Pod'ов: Score вызывается для каждого кандидата, а Reserve может откатиться. Это изменение относительно Gate 5 вынесено в ADR-003.

Горячий путь Score остался без аллокаций: observers гистограммы разрешены заранее. Проверено тестами `TestObservePluginDurationAllocationFree` и `TestScoreHotPathStaysAllocationFree`.

### 2.2. Prometheus (T7.2)

`deployments/prometheus/`: namespace `monitoring`, ServiceAccount, ClusterRole `carbon-aware-prometheus` (Pods/Endpoints/Nodes, `nodes/metrics`, nonResourceURL `/metrics`), ConfigMap конфигурации, Deployment `prom/prometheus:v3.5.0`, Service NodePort 30090, DaemonSet node-exporter v1.9.1.

| Job | Discovery | Таргеты на стенде |
| --- | --- | --- |
| `kube-scheduler` | Pod SD (kube-system + carbon-aware), https + токен SA | 2 (default-scheduler, carbon-aware-scheduler) |
| `carbon-data-service` | Endpoints SD | 1 |
| `cadvisor` | Node SD, `:10250/metrics/cadvisor` | 5 |
| `node-exporter` | Pod SD DaemonSet | 5 |
| `prometheus` | static | 1 |

`scrape_interval` и `scrape_timeout` равны 2 с. Конфигурация проверена `promtool check config`. Чтобы Prometheus достучался до kube-scheduler kind, в `deployments/kind/cluster-config.yaml` добавлен `kubeadmConfigPatches` с `bind-address: 0.0.0.0`. Без него kube-scheduler слушает только 127.0.0.1 на control-plane.

### 2.3. Grafana (T7.3)

`deployments/grafana/`: datasource Prometheus (uid `prometheus`) и provider подключаются через provisioning. Дашборд `carbon_overview.json` попадает в ConfigMap через kustomize `configMapGenerator`. Deployment `grafana/grafana:12.1.0`, Service NodePort 30300, refresh 2 с.

| Панель | Что показывает |
| --- | --- |
| 1. Carbon intensity by zone | `carbon_aware_zone_intensity_gco2_kwh` по зонам |
| 2. Scheduler decisions by zone and node | `increase(carbon_aware_scheduler_decisions_total)` за диапазон |
| 3. Estimated cumulative carbon footprint | ex-post оценка по namespace (cAdvisor CPU × линейная модель мощности × CI) и ex-ante `carbon_aware_pod_estimated_carbon_gco2_sum` |
| 4. Estimated node power (W) | `P_idle + (P_max − P_idle) · u` по узлам |
| 5. Scheduling latency p50/p95/p99 | `scheduler_scheduling_attempt_duration_seconds` по планировщикам; 5b: p99 точек расширения CarbonAware |
| 6. Fallback events & data freshness | `carbon_aware_scheduler_fallback_total` по причинам и возраст данных CDS/плагина с порогами 90/300 с |

Сверху расположены stat-панели: возраст данных CDS, возраст snapshot плагина, число размещённых Pod'ов, число fallback за диапазон. Скриншот после прогона: [`evidence/gate7/grafana_carbon_overview.png`](evidence/gate7/grafana_carbon_overview.png).

Числа на панели 3 иллюстративны и не являются результатом эксперимента. Constants мощности (`p_idle=40`, `p_max=100`) взяты для отображения; эксперимент baseline vs carbon-aware относится к Stage 9+.

## 3. Методика проверки (T7.4)

Скрипт `scripts/gate7_monitoring_check.sh`:

1. Проверяет, что snapshot плагина свежий (`carbon_aware_snapshot_age_seconds` < 90 с). Иначе проверка останавливается, потому что решения шли бы через fallback.
2. Запоминает `sum(carbon_aware_scheduler_decisions_total)`, затем создаёт 50 Pod'ов `busybox:1.37.0` со `schedulerName: carbon-aware-scheduler` в `exp-carbon-aware` и 10 baseline Pod'ов в `exp-baseline`. Каждый Pod жжёт CPU 20 с, `requests = limits = 20m`.
3. Ждёт Binding всех Pod'ов и опрашивает каждые 0.25 с Prometheus HTTP API и Grafana datasource proxy (`/api/datasources/proxy/uid/prometheus/...`), пока сумма не вырастет на 50.
4. Время последнего Binding берётся из лога `carbon-aware-scheduler` (`Successfully bound pod to node`, точность klog до микросекунд; все узлы kind используют часы хоста). Задержка считается как разница между этим временем и первым опросом, в котором значение стало видно.
5. Сверяет разбивку счётчика по узлам с `spec.nodeName` Pod'ов, проверяет `/api/v1/targets` и выполняет каждый запрос каждой панели дашборда.
6. Пишет JSON-сводку.

Задержка ограничена снизу самим конвейером. Это до 2 с ожидания следующего scrape, затем время scrape, затем шаг опроса 0.25 с. Порог 3 с оставляет запас около 1 с. Refresh дашборда (2 с) в измерение не входит: проверяется, что значение доступно Grafana. Отрисовка в браузере зависит от того, когда сработает таймер refresh.

## 4. Результаты серии 2 (финальная, `cpu 20m`)

Файлы: [`evidence/gate7/series2_cpu20m/`](evidence/gate7/series2_cpu20m/).

| Прогон | Δ decisions | worker / worker2 | Submit → все Bound, с | Bind → Prometheus, с | Bind → Grafana, с | Таргеты UP | Панели с данными |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 50 | 24 / 26 | 4.484 | 2.145 | 2.297 | 14/14 | 18/18 |
| 2 | 50 | 24 / 26 | 3.393 | 2.197 | 1.940 | 14/14 | 18/18 |
| 3 | 50 | 24 / 26 | 3.725 | 1.755 | 1.926 | 14/14 | 18/18 |
| 4 | 50 | 24 / 26 | 5.012 | 2.046 | 2.335 | 14/14 | 18/18 |
| 5 | 50 | 24 / 26 | 4.040 | 2.736 | 2.378 | 14/14 | 18/18 |

Все carbon-aware Pod'ы попали на узлы зоны **north** (CI 85 gCO2e/kWh), baseline Pod'ы `default-scheduler` разместил на **west**. Ожидаемо, что счётчик fallback не рос: snapshot был свежим.

Load average хоста (по `uptime` во время прогонов, в JSON не сохранялся) рос с 7.5 до 53 к пятому прогону. Задержка при этом осталась ниже порога.

## 5. Серия 1 (`cpu 200m`): найдено и устранено

Файлы: [`evidence/gate7/series1_cpu200m/`](evidence/gate7/series1_cpu200m/). Первая серия использовала `requests = limits = 200m`. 50 таких Pod'ов требуют 10 CPU, а у контейнера стенда 4 ядра.

| Прогон | Δ decisions | Submit → все Bound, с | Bind → Prometheus, с | Bind → Grafana, с | Таргеты UP | Замечание |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 50 | 3.6 | 2.398 | 2.142 | 14/14 | панель 4 пустая |
| 2 | 50 | 5.8 | 1.492 | 2.019 | 14/14 | панель 4 пустая |
| 3 | 50 | 28.1 | **3.469** | **3.740** | 14/14 | |
| 4 | 50 | 53.2 | **3.233** | **3.817** | **11/14** | cadvisor worker2, node-exporter worker/worker2 down |
| 5 | 50 | 10.9 | 2.935 | **3.542** | 14/14 | |

Счётчик решений совпал в 5 из 5 прогонов, но порог задержки нарушен в прогонах 3–5. Причина — насыщение хоста, а не конвейер метрик. Binding растягивался до 53 с, scrape cAdvisor и node-exporter на загруженных узлах не укладывались в таймаут 2 с, kubelet не успевал отдать CPU-метрики для панели 4.

Исправление касается только нагрузки: лимит снижен до 20m (параметр `GATE7_CPU_LIMIT`). Код метрик, конфигурация Prometheus и дашборд между сериями не менялись, кроме цвета stat-панели «Pods placed». Перед серией 2 кластер пересоздан: после перезапуска dockerd сеть узла worker4 не восстановилась.

Вывод для экспериментов Stage 9+: на машине с 4 ядрами при 2 с scrape нагрузка не должна насыщать CPU. Иначе часть таргетов выпадает, и данные cAdvisor за эти интервалы теряются.

## 6. Тесты и статический анализ

- `go vet ./...` — без замечаний.
- `go test -race -count=1 ./...` — все пакеты PASS. Новые тесты:
  - `pkg/metrics`: экспозиция парсится `expfmt`, `testutil.CollectAndLint`, коллектор CDS молчит при пустом кэше, коды ответов в `api_requests_total`, ноль аллокаций в `ObservePluginDuration`.
  - `pkg/scheduler/decision_test.go`: PostBind записывает решение и CF = E·CI при постоянной CI; решения на fallback считаются без оценки; PostBind без PreScore и без informer пишет зону `unknown`; stale увеличивает `fallback_total{reason="stale"}`; длительность считается по каждой точке расширения; Score не более 1 аллокации.
  - `pkg/carbon/api`: `TestMetricsExposeStage7Families`.
- `promtool check config deployments/prometheus/prometheus-config.yaml` — SUCCESS.

## 7. Что не проверено и отличия стенда

| Пункт | Состояние |
| --- | --- |
| `golangci-lint` | не запускался: не установлен в среде |
| `make docker-build` (репозиторные Dockerfile) | не запускался. Образы собраны из бинарников хоста на той же distroless-базе |
| Машина пользователя (Docker Desktop / WSL2, cgroup v2) | не проверялась. Стенд — контейнер с cgroup v1 |
| Патчи kind для контейнера | `failCgroupV1: false` и `restrict_oom_score_adj` применялись только в локальной копии конфигурации. В репозитории их нет, на cgroup v2 они не нужны |
| Отрисовка дашборда в браузере | проверена скриншотом; задержка измерялась до datasource proxy, а не до пикселей |
| Профиль C (`expected-cf`) под нагрузкой | серия шла через профиль `carbon-aware-scheduler`; гистограмма CF проверена unit-тестами и панелью 3 |

## 8. Решения, требующие подтверждения

- **ADR-003 (proposed):** точка расширения PostBind и аддитивная поправка `metrics.md` v1.1.0 (§2a/§3a). Имена v1.0.0 не переименованы и не удалены.

## 9. Передача в Stage 8

- Метрики для проверок e2e и chaos уже есть: `carbon_aware_scheduler_fallback_total{reason}` (CDS недоступен → `dead`, устаревшие данные → `stale`, невалидные → `all_invalid`) и `carbon_aware_data_age_seconds`.
- Для подсчёта размещённых Pod'ов подходит `carbon_aware_scheduler_decisions_total`.
- Скрипт `scripts/gate7_monitoring_check.sh` можно переиспользовать как smoke-проверку стенда после `setup_kind.sh` и применения `deployments/prometheus` и `deployments/grafana`.
