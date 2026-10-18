# Глоссарий

> Канонические термины. Новые понятия добавлять сюда до использования в коде и тексте диплома.

| Термин | Определение | Не путать с |
| --- | --- | --- |
| Carbon intensity \(CI\) | Average consumption-based lifecycle интенсивность, канон `gCO2e/kWh` | PUE; marginal emissions; NESO `gCO2/kWh` (direct) |
| Estimated carbon footprint \(\widehat{CF}(p,n)\) | Оценочный маржинальный след размещения пода \(p\) на узел \(n\), gCO2e | Измеренные выбросы Pod; cluster \(CF\) |
| Cluster carbon \(CF_{\mathrm{cluster}}\) | \(\sum_n CF_n\) по полной модельной мощности узлов, включая idle | Сумма \(\widehat{CF}(p,n)\) при IDLE-NONE |
| \(P_{\mathrm{idle}}\) | Мощность включённого узла при \(u=0\) (active idle), W | S3/sleep; nameplate |
| \(P_{\max}\) | Мощность узла при \(u=1\) в модели, W | TDP / nameplate без калибровки |
| \(P_n(u)\) | Аффинный оценщик \(P_{\mathrm{idle}}+(P_{\max}-P_{\mathrm{idle}})u\), W | Показание RAPL/ваттметра kind-node |
| \(\widehat{P}_{\mathrm{pod}}\) | Маржинальная мощность пода: \(P_n(u^+)-P_n(u^-)\), W (IDLE-NONE) | Полная \(P_n\); доля idle |
| Energy attribution | Учётное разнесение динамической мощности узла по CPU share | Физическое измерение мощности Pod |
| Persistence forecast | \(\widehat{CI}(t+\Delta)=CI(t)\) при отсутствии внешнего прогноза | Perfect foresight; ML-прогноз |
| \(\operatorname{Score}(n)\) | Внутренняя полезность `CarbonAware` на \([0,100]\) | `TotalScore` фреймворка с весами \(w^{\mathrm{fw}}\) |
| Carbon-aware scheduling | Планирование с учётом ожидаемого следа, ресурсов и SLA | Greenwashing / только «энергоэффективность CPU» |
| Deferrable workload | Нагрузка, которую допустимо отложить в пределах окна | Best-effort без дедлайна; **не** входит в runtime-канон диплома |
| SLO | Целевой уровень сервиса (латентность, дедлайн, throughput) | SLA (контракт с последствиями) |
| Kind cluster | Локальный Kubernetes для экспериментов | Продакшен-кластер |
| Energy zone | Логическая энергозона узла (label `carbon-aware.io/zone`) внутри одного кластера | Cloud region; отдельный Kubernetes-кластер |
| Carbon Data Service (CDS) | Отдельный процесс нормализации \(CI\), replay и REST; не принимает решений о размещении | Плагин `CarbonAware`; kube-scheduler |
| Replay Engine | Воспроизведение ряда \(CI\) по модельному времени сценария | Live Electricity Maps; perfect foresight |
| Cache Sync Worker | Фоновая goroutine плагина: HTTP pull CDS → in-memory snapshot | Вызов REST из `Score`/`PreScore` |
| Plugin carbon snapshot | Неизменяемый in-memory снимок \(CI\) в процессе планировщика | Кэш CDS; `CycleState` (живёт одну попытку) |
| Fresh snapshot (SM-FRESH) | Снимок плагина с \(\mathrm{age}_{\mathrm{snap}}\le\texttt{staleWarnSeconds}\) и `quality≠stale` | Stale-in-window; dead cache |
| Stale-in-window (SM-STALE) | Снимок ещё \(\le\texttt{maxDataAgeSeconds}\), но `quality=stale` или возраст выше `staleWarnSeconds`; Score полный, Warning | G-08 (отключение carbon scoring) |
| Dead cache (SM-DEAD / SM-EMPTY) | Нет снимка или \(\mathrm{age}_{\mathrm{snap}}>\texttt{maxDataAgeSeconds}\); \(\operatorname{Score}=50\) всем узлам (G-08) | Нейтраль только \(S_C\); \(S_C=0\) |
| Placement anti-concentration | Растекание burst-нагрузки через \(S_R\) и `Filter`/`assume` при первичном размещении | Relocation anti-thrashing (descheduler, cooldown) — вне Scope |
| Scheduler profile | Набор плагинов с уникальным `schedulerName` в `KubeSchedulerConfiguration` | Внутренние веса \(w_C,w_R,w_P\); Deployment |
| `default-scheduler` | Профиль/процесс стратегии A без `CarbonAware` | `carbon-aware-scheduler`; `carbon-only-scheduler` |
| SSOT | Единый источник правды в `docs/ssot/` | ADR, черновики research |
| CarbonRecord | Каноническое наблюдение \(CI\) одной зоны (`gCO2e/kWh`, RFC 3339 UTC) | Показание Electricity Maps без нормализации; \(\widehat{CF}\) |
| ForecastInterval | Кусок кусочно-постоянного прогноза \(CI\) на полуинтервале сетки | Perfect foresight; линейная интерполяция |
| WorkloadClass | Label `carbon-aware.io/workload-class`: `batch` \| `flexible` \| `latency-sensitive` | `spec.schedulerName`; QoS Kubernetes |
| CarbonProvider | Узкий интерфейс Live/Replay/Mock: `Current` / `Forecast` / `ListZones` | Плагин `CarbonAware`; REST «compute score» (запрещён) |
| Opt-in (`carbon-aware.io/enabled`) | Label `"true"` включает углеродный скоринг на профиле B/C | G-08 (dead cache); `spec.schedulerName` |

Нормативные формулы (LOCKED, ADR-001): [02_math_model_ssot.md](02_math_model_ssot.md) v1.0.0. Компонентная модель (LOCKED, ADR-002, Quality Gate 3): [01_architecture_ssot.md](01_architecture_ssot.md) v1.1.0. Fallback (LOCKED): [failure_and_fallback_policy.md](failure_and_fallback_policy.md) v1.1.0. Контракты API (LOCKED): [03_api_contracts_ssot.md](03_api_contracts_ssot.md) v1.0.1. Имена метрик (LOCKED): [metrics.md](metrics.md) v1.0.0.
