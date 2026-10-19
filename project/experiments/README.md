# Эксперименты (Stage 10)

| Каталог / файл | Содержимое |
| --- | --- |
| `workloads/synthetic_task.go` | синтетическая CPU-bound задача, образ `carbon-workload-runner` (T10.1) |
| `workloads/batch/` | генератор пакетов задач: классы SLA и ресурсов, пуассоновские прибытия, рендер Pod/Job (T10.1) |
| `workloads/cmd/batchgen/` | CLI: пакет задач в YAML для `kubectl apply` |
| `workloads/calibrate.sh` | калибровка длительности в кластере (T10.1) |
| `runner/` | раннер экспериментов: серия прогонов одной командой, выгрузка в CSV (T10.2) |
| `manifests/` | экспериментальный оверлей стека (kustomize) |
| `run_stage10.sh` | полный набор серий T10.3 + T10.4 |
| `check_results.py` | проверка целостности CSV и краткая сводка по ячейкам |
| `datasets/` | ряды CI: JSON Schema в `datasets/schemas/`, Replay-датасеты в `datasets/examples/` |
| `results/` | сырые данные: `raw_data_*.csv` (строка на задачу), `runs_data_*.csv` (строка на прогон), калибровка |

Отчёт этапа: [`docs/audit/stage_10_experiments_report.md`](../docs/audit/stage_10_experiments_report.md).

## Стенд

```bash
make kind-up                                   # 4 worker: north ×2, west ×2
make kind-load                                 # образы планировщика и CDS
docker build -f build/Dockerfile.workload -t carbon-workload-runner:0.1.0 .
kind load docker-image carbon-workload-runner:0.1.0 --name carbon-aware
kubectl apply -k deployments/prometheus        # scrape 2 с, нужен раннеру
kubectl apply -k experiments/manifests         # CDS + планировщик в режиме эксперимента
bash experiments/run_stage10.sh                # T10.3 и T10.4
```

Раннер ходит в Prometheus `http://localhost:9090` и в CDS `http://localhost:8080` (NodePort из `deployments/kind/cluster-config.yaml`), kubeconfig берёт из `$KUBECONFIG` или `~/.kube/config`.

Оверлей `experiments/manifests` отличается от `deployments/kubernetes` только этим:

- CDS: `cacheTTL: 2s`, датасет и ускорение replay раннер задаёт перед каждым прогоном;
- планировщик: `refreshIntervalSeconds: 2`, `staleWarnSeconds: 10`, `maxDataAgeSeconds: 30`, `httpTimeoutSeconds: 1` (как в E2E-оверлее): replay идёт с ускорением, и обновление раз в 30 с отставало бы от проигрываемой CI на часы модельного времени;
- профиль `carbon-aware-scheduler`: `allowPodCarbonWeightOverride: true`, чтобы серия по \(w_C\) задавала вес аннотацией Pod без перезапуска планировщика. Pod без аннотации получает канон \(w_C=0.6, w_R=0.2, w_P=0.2\);
- namespace `carbon-exp` для задач.

Параметры одинаковы для всех прогонов всех серий.

## T10.1. Генератор нагрузки

`synthetic_task` держит заданную загрузку CPU ровно заданное время и печатает одну JSON-строку с фактическим временем.

| Флаг | Смысл |
| --- | --- |
| `--duration` | время работы \(T\) (30s, 60s, 300s …) |
| `--cpu` | нагрузка в ядрах: 1 = одно ядро на 100 %, 0.1 = 10 % ядра; > 1 делится между `ceil(cpu)` горутинами |
| `--kernel` | `sha256` (хэши блока 1 КиБ) или `matmul` (матрицы 24×24) |
| `--memory-mib` | резидентная память, страницы реально записываются |
| `--work-units` | режим фиксированного объёма работы вместо длительности |
| `--period` | период duty-cycle, по умолчанию 100 мс |

Нагрузка — duty-cycle: в каждом периоде горутина считает ядро `cpu/workers · period`, затем спит до конца периода; последний период обрезается по дедлайну. Внешних зависимостей нет, только стандартная библиотека Go. Вывод: `started_at`, `finished_at`, `actual_duration_s`, `cpu_user_s`, `cpu_sys_s`, `cpu_utilization` = CPU-время / (время × ядра), `work_units`, `status`.

Калибровка в кластере (`workloads/calibrate.sh`, данные `results/calibration_t10_1.jsonl`, пять Pod одновременно):

| Задача | \(T\), с | Факт, с | Отклонение | Доля заданной нагрузки |
| --- | --- | --- | --- | --- |
| 1 ядро, sha256 | 60 | 60.000 | 0.000 с | 0.951 |
| 1 ядро, matmul | 60 | 60.000 | 0.000 с | 0.954 |
| 0.1 ядра, sha256 | 60 | 60.001 | 0.001 с | 0.912 |
| 0.05 ядра, 32 МиБ | 30 | 30.000 | 0.000 с | 0.949 |
| 0.1 ядра, 64 МиБ | 300 | 300.000 | 0.000 с | 0.984 |

Критерий «60 ± 1 с» выполнен с запасом: отклонение не больше 1 мс. Доля нагрузки ниже 1 из-за того, что пять Pod и kind делили 4 ядра хоста; на свободном хосте 1 ядро даёт 0.999 (`go test ./experiments/workloads/`).

Пакет задач (`workloads/batch`) — чистая функция от `Spec` и seed: один seed даёт одинаковые задачи, классы и моменты прибытия для всех стратегий. Классы по умолчанию:

| Класс | \(T=\hat T\) | CPU request | RAM request | Дедлайн | `workload-class` | Доля |
| --- | --- | --- | --- | --- | --- | --- |
| `short` | 30 с | 500m | 64 МиБ | \(1.5\,\hat T\) = 45 с | batch | 0.5 |
| `medium` | 60 с | 1000m | 128 МиБ | \(1.5\,\hat T\) = 90 с | batch | 0.5 |
| `long` (вне матрицы) | 300 с | 1000m | 128 МиБ | \(1.5\,\hat T\) | flexible | — |

Pod получает label `carbon-aware.io/enabled=true`, `carbon-aware.io/workload-class`, аннотации `expected-duration`, `sla-deadline` и, в серии весов, `carbon-weight`. Ресурсы: requests как в таблице, limit памяти равен request, лимита CPU нет. Пакет в YAML: `go run ./experiments/workloads/cmd/batchgen --kind=job --scheduler=carbon-aware-scheduler > pack.yaml`.

## T10.2. Раннер

```bash
go run ./experiments/runner --experiment=matrix \
  --strategy=default,carbon-only,proposed --scenario=stable,dynamic \
  --repetitions=5 --out=experiments/results/raw_data_matrix.csv
```

Шаги каждого прогона:

1. Удалить все Pod в `carbon-exp`, дождаться пустого namespace и загрузки CPU хоста ниже 0.5 (node-exporter).
2. Переписать ConfigMap CDS (датасет, ускорение), перезапустить CDS, дождаться свежего снимка у планировщика (`carbon_aware_snapshot_age_seconds` < 2.5 с из нового CDS) и модельного времени, равного якорю сценария. Если якорь пройден раньше, CDS перезапускается снова (до 3 раз). Так каждый прогон начинается в одной точке ряда (REPRO-03).
3. Подать 50 задач с пуассоновскими прибытиями (\(\lambda=0.33\) с⁻¹, около 150 с).
4. Дождаться завершения всех задач (не дольше 10 минут).
5. Запросить Prometheus: CI по зонам из нового Pod CDS (шаг 1 с), модельное время, CPU-секунды задач из cAdvisor, гистограммы `scheduler_scheduling_attempt_duration_seconds` и `scheduler_framework_extension_point_duration_seconds{extension_point="Score"}` профиля, счётчики `carbon_aware_scheduler_decisions_total` и `carbon_aware_scheduler_fallback_total`. Счётчики и гистограммы берутся как разность значений до \(t_0\) и после конца прогона.
6. Дописать строки в `--out` и строку прогона в `runs_<имя>.csv`, удалить Pod.

Флаги `--strategy`, `--scenario`, `--carbon-weight`, `--deadline-factor` принимают списки через запятую, раннер строит полное произведение. Внешний цикл — повторения, внутренний — стратегии, поэтому медленный дрейф хоста ложится на все стратегии одинаково. Seed пакета = `--seed` + номер повторения: все стратегии и значения параметров одного повторения получают один и тот же пакет (парный дизайн). CSV дописывается с fsync после каждого прогона; повторный запуск той же команды пропускает готовые `run_id`.

| `--strategy` | Профиль | Что это |
| --- | --- | --- |
| `default` | `default-scheduler` (kube-scheduler kind) | A: стандартный планировщик |
| `carbon-only` | `carbon-only-scheduler` | B: `current-ci`, \(w_C=1\), остальные score-плагины выключены |
| `proposed` | `carbon-aware-scheduler` | C: `expected-cf`, \(w_C=0.6, w_R=0.2, w_P=0.2\) + стандартные плагины |

| `--scenario` | Датасет | Ускорение | Якорь (старт подачи) |
| --- | --- | --- | --- |
| `stable` | `synthetic_multizone_stable_24h_v1` | 300× (1 с = 5 мин) | 02:00 модельного времени |
| `dynamic` | `synthetic_multizone_dynamic_24h_v1` | 300× | 02:00 |
| `volatile` | `synthetic_multizone_volatile_2h_v1` | 30× (1 с = 30 с) | 10:15 |

### Колонки `raw_data_*.csv` (строка на задачу)

| Колонка | Смысл |
| --- | --- |
| `run_id`, `experiment`, `strategy`, `scheduler_name`, `scenario`, `dataset_id`, `replay_multiplier`, `repetition`, `seed` | метаданные прогона |
| `w_c`, `w_r`, `w_p` | веса, которые применяет плагин (пусто для `default`); при аннотации \(w_R, w_P\) масштабируются на \(1-w_C\) |
| `carbon_weight_annotation`, `deadline_factor` | значение аннотации `carbon-weight` и множитель дедлайна \(k\) |
| `task_index`, `pod_id`, `class`, `workload_class`, `cpu_request_m`, `memory_request_mib`, `burn_cores` | задача |
| `expected_duration_s`, `deadline_s`, `arrival_offset_s` | \(\hat T\), дедлайн \(\lceil k\hat T\rceil\), смещение прибытия от \(t_0\) |
| `node`, `zone`, `status` | узел, зона, `succeeded` / `failed` / `unscheduled` / `unfinished` |
| `submitted_at`, `scheduled_at` | создание Pod и появление `spec.nodeName` в watch раннера (мс) |
| `started_at`, `completed_at` | начало и конец работы задачи по её JSON-выводу |
| `sched_latency_s` | `scheduled_at − submitted_at`: создание → Binding, включая ожидание в очереди |
| `wait_s`, `duration`, `turnaround_s` | `started − submitted`; фактическое время работы, с; `completed − submitted` |
| `sla_violated` | `turnaround_s > deadline_s`; незавершённая задача — нарушение |
| `p_pod_w` | \(P_{pod}=(P_{max}-P_{idle})\cdot \mathrm{req}_{CPU}/\mathrm{alloc}_{CPU}\), Вт (40/100 Вт, линейная модель SSOT 2.4, idle не атрибутируется) |
| `energy_kwh` | \(P_{pod}\cdot\) `duration` / 3.6·10⁶ |
| `carbon_gco2` | \(\int P_{pod}\,CI_{zone}(t)\,dt\) по [`started_at`, `completed_at`], \(CI(t)\) — ступенчатый ряд CDS из Prometheus |
| `ci_avg_gco2_kwh` | средняя CI зоны за время работы задачи |
| `ci_at_schedule`, `ci_min_at_schedule`, `ci_max_at_schedule`, `best_zone_at_schedule` | CI выбранной зоны, минимум и максимум по зонам и лучшая зона в момент Binding |
| `model_time_at_schedule` | модельное время replay в момент Binding |
| `cpu_seconds_task`, `cpu_seconds_cadvisor`, `cpu_utilization_task` | CPU-время по rusage задачи и по cAdvisor; доля заданной нагрузки |

### Колонки `runs_data_*.csv` (строка на прогон)

Метаданные как выше, плюс: `tasks`, `arrival_rate`, `burn_fraction`, `power_idle_w`, `power_max_w`; `run_started_at` (\(t_0\)), `run_finished_at`, `model_time_at_start`; `baseline_host_cpu_util`, `host_cpu_util_mean`, `host_cpu_util_max`; `succeeded`, `failed`, `unscheduled`; `total_energy_kwh`, `total_carbon_gco2`, `mean_duration_s`, `mean_turnaround_s`, `makespan_s`; `sla_violations`, `svr`; `zone_shares`; `sched_latency_p50/p95/p99_s` (по задачам прогона); `sched_attempt_p50/p95/p99_s` и `sched_attempts` (гистограмма планировщика, Prometheus); `score_ext_p50/p99_s` (точка Score); `decisions_delta`, `fallback_delta` (счётчики плагина за прогон); `ci_series_complete`.

## T10.3 и T10.4. Серии

| Серия | `experiment` | Файл | Прогоны |
| --- | --- | --- | --- |
| Матрица A/B/C | `matrix` | `raw_data_matrix.csv` | 3 стратегии × stable, dynamic × 5 повторений |
| Волатильность | `volatile` | `raw_data_stress.csv` | 3 стратегии × volatile × 5 |
| Веса | `weights` | `raw_data_stress.csv` | proposed, \(w_C\in\{0, 0.2, 0.4, 0.6, 0.8, 1.0\}\) × volatile × 3 |
| SLA | `sla` | `raw_data_stress.csv` | 3 стратегии × \(k\in\{1.1, 2.0\}\) × volatile × 3; \(k=1.5\) — серия `volatile` (те же seed для повторений 1–3) |

Каждый прогон — 50 задач. Проверка: `python3 experiments/check_results.py experiments/results/raw_data_matrix.csv experiments/results/raw_data_stress.csv`.

## Ограничения стенда

Эти решения приняты, чтобы стенд не стал узким местом, и их нужно учитывать при интерпретации в Stage 11.

1. **Физическая нагрузка уменьшена в 10 раз.** Все 5 узлов kind работают на одном хосте с 4 ядрами. Планировщик и модель энергии видят номинальные requests (500m и 1000m), а задача реально сжигает 10 % своего request (`--burn-fraction=0.1`, 0.05 и 0.1 ядра). Энергия и углерод в CSV — модельные (линейная модель по requests), а не измеренные. Средняя загрузка хоста в прогонах записана в `runs_*.csv`.
2. **Длительности 30 и 60 с.** Класс 300 с откалиброван, но в серии не входит: прогон занимает около 5 минут вместо 15+.
3. **Ускоренный replay.** 24-часовые ряды идут в 300 раз быстрее, двухчасовой volatile — в 30. Задача 60 с покрывает 5 модельных часов (stable/dynamic) или 30 минут (volatile).
4. **Две зоны.** Топология Stage 6: `north` ×2 и `west` ×2. Зона `central` есть в датасетах, но узлов в ней нет. В stable и dynamic `north` всегда чище `west`, поэтому стратегии различаются только тем, какая доля нагрузки остаётся в `north` до её заполнения. Смены лидера есть только в volatile.
5. **Прогноз в replay не используется плагином.** Плагин интегрирует \(\widehat{CF}\) от `now` планировщика (часы хоста), а интервалы прогноза CDS в replay помечены модельным временем (2026-09-01). Все интервалы оказываются в прошлом, и интеграл сводится к текущей CI на весь \(\hat T\) (2.11). Для stable и dynamic это ничего не меняет (`forecastMode: persistence`), но recorded-прогноз volatile на стенде не работает. Это находка этапа, код плагина в Stage 10 не менялся; подробности в отчёте.
6. **Дедлайн не влияет на выбор узла.** \(S_P\) одинаков для всех узлов (зависит только от \(\hat T\) и дедлайна), поэтому серия SLA меняет порог нарушения, а не размещение. Размещение меняют только \(w_C\) и стратегия.
7. **Энергия по построению одинакова.** Узлы однородны (`standard`, 40/100 Вт), длительность фиксирована, поэтому `energy_kwh` у стратегий совпадает, и ER ≈ 0. Различается углерод.
8. **Runtime не зависит от узла.** Задачи работают ровно \(T\), поэтому деградация видна в `wait_s` и `turnaround_s` (очередь), а не в `duration`.
9. **cAdvisor недосчитывает CPU** коротких контейнеров примерно на 15 % (последние секунды до завершения не попадают в scrape 2 с). Точное значение — `cpu_seconds_task`.
10. **Метка stale при ускорении 300×.** CDS считает свежесть записи по модельным часам: кэш обновляется раз в 2 с wall = 10 модельных минут, а порог `staleWarn` 90 с модельного времени проходит за 0.3 с wall. Поэтому в stable и dynamic CDS часто помечает только что обновлённую запись как `stale`, плагин засчитывает попытку в `carbon_aware_scheduler_fallback_total{reason="stale"}` (колонка `fallback_delta`) и продолжает полный скоринг с теми же данными. На размещение это не влияет (SM-STALE не меняет Score), но `fallback_delta` в этих сценариях не означает отказа источника. В volatile (30×) порог не достигается.
