# Метрики runtime и экспериментальные KPI

- **Версия:** 1.0.0
- **Статус:** accepted (LOCKED) — **имена и смысл**; JSON дашбордов Grafana — реализация `deployments/grafana/`
- **Дата утверждения:** 2026-09-04
- **Владелец:** Lead System Architect
- **Уровень изменяемости:** L1
- **Основание:** [01_architecture_ssot.md](01_architecture_ssot.md) §7; [failure_and_fallback_policy.md](failure_and_fallback_policy.md) §11; [03_api_contracts_ssot.md](03_api_contracts_ssot.md) `API-META-02`; [02_math_model_ssot.md](02_math_model_ssot.md) (2.9), (2.10); `DOD-CODE-13`; Quality Gate 3; Freeze [`002_system_architecture_and_api_freeze.md`](../decisions_adr/002_system_architecture_and_api_freeze.md)

> **LOCKED / SSOT Freeze (Quality Gate 3).** Имена, типы, лейблы и единицы `carbon_aware_*` **заблокированы**. Смена имени — ADR (`VER-06`). PromQL-запросы дашбордов и scrape YAML — манифесты Stage 4+, обязаны ссылаться на эту таблицу. Планировщик **не** запрашивает Prometheus (`ARCH-11`). Экспорт best-effort: ошибка scrape не меняет `fwk.Status`.

Указатель топологии съема: [architecture.md](architecture.md) → `01_architecture_ssot.md` §7.

---

## 1. Соглашения

| Правило | Норма |
| --- | --- |
| Префикс проекта | `carbon_aware_` |
| Единицы в имени | `_seconds`, `_gco2e`, `_total` (счётчик), без голых `value` (`GO-11`, `DOD-DOC-05`) |
| `_total` | только `counter` |
| Лейбл `reason` | закрытый словарь: `empty` \| `dead` \| `all_invalid` |
| Запрет | смешивать G-08, SM-STALE и opt-out в одном счётчике без лейбла |

Стандартные метрики kube-scheduler (`scheduler_framework_extension_point_duration_seconds`, `scheduler_pending_pods`, …) **не** переименовываются. Для overhead (`SCOPE-IN-14`) канон — **несэмплируемая** `scheduler_framework_extension_point_duration_seconds`, не `scheduler_plugin_execution_duration_seconds` (~10 % семплов, T1.3 §9.3).

---

## 2. Плагин `CarbonAware` / процесс `carbon-aware-scheduler`

| Имя | Тип | Лейблы | Единица | Смысл |
| --- | --- | --- | --- | --- |
| `carbon_aware_scoring_attempts_total` | counter | `scoring_mode`=`expected-cf`\|`current-ci` | 1 | попытка `PreScore` с `enabled=true` и без SLA-Error |
| `carbon_aware_fallback_total` | counter | `reason`=`empty`\|`dead`\|`all_invalid` | 1 | G-08 (Score=50 всем). **Не** SM-STALE, **не** opt-out |
| `carbon_aware_stale_in_window_total` | counter | — | 1 | SM-STALE: полный (2.19)/(2.23) + Warning |
| `carbon_aware_opt_out_total` | counter | — | 1 | FF-OPT / `ARCH-16` |
| `carbon_aware_sla_rejection_total` | counter | — | 1 | PreScore Error G-12/G-20; не `fallback_total` |
| `carbon_aware_snapshot_age_seconds` | gauge | — | s | \(\mathrm{age}_{\mathrm{snap}}=t_{\mathrm{now}}-t_{\mathrm{fetchedAt}}\); при `nil` — не публиковать или `+Inf` (выбрать одно в коде и держать; канон реализации: не экспортировать gauge, пока `Load()==nil`) |
| `carbon_aware_sync_duration_seconds` | histogram | `status`=`success`\|`failure` | s | длительность тика Cache Sync Worker |
| `carbon_aware_sync_failure_total` | counter | — | 1 | тик Worker без `Store` |
| `carbon_aware_invalid_record_total` | counter | `stage`=`worker` | 1 | G-10 / FF-FUT на сборке snapshot |
| `carbon_aware_unknown_zone_nodes` | gauge | — | 1 | число NM-UNKNOWN в последнем успешном `PreScore` |
| `carbon_aware_prescore_duration_seconds` | histogram | — | s | собственная длительность `PreScore` (дополняет framework histogram) |
| `carbon_aware_estimated_cf_gco2e` | histogram | `zone` | gCO2e | \(\widehat{CF}(p,n)\) кандидатов на попытку (гипотеза эксперимента; не вход `Score`) |

`scoring_mode` совпадает с `pluginConfig.args.scoringMode`.

---

## 3. Carbon Data Service

| Имя | Тип | Лейблы | Единица | Смысл |
| --- | --- | --- | --- | --- |
| `carbon_aware_cds_zones` | gauge | — | 1 | число зон в опубликованном снимке |
| `carbon_aware_cds_cache_age_seconds` | gauge | — | s | возраст снимка CDS (часы процесса CDS) |
| `carbon_aware_cds_provider_error_total` | counter | `provider`, `kind` | 1 | FP-01/FP-02; `kind`=`timeout`\|`http`\|`parse`\|`invalid` |
| `carbon_aware_cds_replay_model_time_seconds` | gauge | — | s | Unix UTC \(t_{\mathrm{model}}\) в replay; в live/mock не экспортировать |
| `carbon_aware_cds_quality` | gauge | `quality`=`fresh`\|`stale` | 1 | 1 на текущем качестве снимка, 0 на остальных |

Порт scrape — тот же listener **8080**, путь `/metrics` (не часть OpenAPI JSON).

---

## 4. Экспериментальные KPI (ex-post, контур D)

Считаются **офлайн** в `analysis/`, не в плагине (`ARCH-11`). Формулы — [02_math_model_ssot.md](02_math_model_ssot.md). Числа в диплом — только с `run_id` (`DOD-DOC-10`, `DOD-EXP-*`).

| KPI | Метод | Цель |
| --- | --- | --- |
| \(CF_{\mathrm{cluster}}\) | (2.9a) по полной \(P_n(u)\) включая idle | главный carbon KPI |
| \(\Delta CF\) vs A / vs B | разность mean \(CF_{\mathrm{cluster}}\) на идентичном replay | сравнение стратегий |
| Makespan / SLA miss | доля Pod с нарушением дедлайна (если задан) | `SCOPE-IN-08` |
| Scheduling latency | p50/p95 `scheduler_framework_extension_point_duration_seconds` по A/B/C | overhead |
| Fallback rate | `rate(carbon_aware_fallback_total[…])` | эксперимент отказа источника |
| Stale-in-window rate | `carbon_aware_stale_in_window_total` | не путать с Fallback rate |

Утверждение «снижение CO₂ на X %» без N, разброса и sensitivity по \(k=P_{\mathrm{idle}}/P_{\max}\) запрещено (`DOD-DOC-09`, `DOD-EXP-11`, LIM-07).

---

## 5. Grafana (состав панелей, не JSON)

Минимум панелей (гипотеза набора; JSON — `deployments/grafana/`):

1. \(CI(t)\) по зонам (из CDS);
2. возраст снимка CDS и плагина, rate fallback vs stale vs opt-out;
3. scheduling latency A vs B vs C;
4. выбранные зоны (ex-post Binding);
5. ex-post \(CF_{\mathrm{cluster}}\) (офлайн или recording rule анализа, не `Score`).

Grafana не является управляющим контуром.

---

## 6. Запреты

- Планировщик не делает PromQL и не ходит в Metrics API в цикле планирования.
- CDS не пишет в Grafana API.
- Нет метрики «выбранный Score endpoint» — CDS не считает \(\operatorname{Score}\).
- Имя `carbon_aware_fallback_total` не используется для SM-STALE и opt-out.
