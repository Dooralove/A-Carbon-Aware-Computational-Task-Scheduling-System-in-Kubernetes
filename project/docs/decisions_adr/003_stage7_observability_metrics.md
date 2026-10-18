# ADR-003: Метрики Stage 7 и информационный PostBind плагина

- **Статус:** proposed (ждёт подтверждения владельца проекта)
- **Дата:** 2026-10-18
- **Авторы:** Metrics & Observability Engineer (T7.1)
- **Связанные документы:** `docs/ssot/metrics.md` (v1.1.0), `docs/ssot/01_architecture_ssot.md` §6.7, §7; ADR-002 Freeze
- **Supersedes:** —
- **Superseded by:** —

## Контекст

Задача T7.1 требует экспортировать восемь метрик с фиксированными именами
(`carbon_aware_zone_intensity_gco2_kwh`, `carbon_aware_data_age_seconds`,
`carbon_aware_api_requests_total`, `carbon_aware_pod_estimated_carbon_gco2`,
`carbon_aware_pod_estimated_energy_kwh`, `carbon_aware_scheduler_decisions_total`,
`carbon_aware_scheduler_fallback_total`, `carbon_aware_plugin_duration_seconds`),
которых нет в замороженной таблице `metrics.md` v1.0.0. Две из них описывают
**выбранный** узел (решения по зонам/нодам, оценка CF/энергии Pod'а на нём).
Архитектура §6.7 фиксирует, что выбранный узел восстанавливается ex-post из
Binding / kube-state-metrics и что «отдельный `PostBind` не вводится в Scope».
Gate 7 сверяет число поданных подов со счётчиком решений, поэтому счётчик должен
обновляться в процессе планировщика при каждом решении.

## Решение

1. Метрики T7.1 добавляются **аддитивно**: ни одно имя v1.0.0 не переименовано и
   не удалено, смысл существующих метрик не меняется. Таблица — `metrics.md` §2a, §3a.
2. Плагин `CarbonAware` реализует `PostBind`. Хук только инкрементирует
   `carbon_aware_scheduler_decisions_total` и пишет оценки выбранного узла; он не
   возвращает `Status`, не делает I/O и не влияет на размещение. `Bind`, `Reserve`,
   `Permit`, `Filter` по-прежнему запрещены (SCOPE-OUT-16).
3. Оценки узлов передаются из `PreScore` в `PostBind` через отдельный ключ
   `CycleState` (`CarbonAware/decision`); замороженный DTO `CarbonCycleState`
   (API-STATE-01) не меняется.
4. `carbon_aware_scheduler_fallback_total{reason}` объединяет G-08 (`empty`,
   `dead`, `all_invalid`) и SM-STALE (`stale`) **с лейблом**, что разрешено
   `metrics.md` §1. Канонический `carbon_aware_fallback_total` остаётся только для G-08.

## Обоснование

`PostBind` вызывается фреймворком после успешного `Bind`, поэтому счётчик равен
числу размещённых подов (а не попыток, как было бы с `Reserve`). Хук выполняется
в binding goroutine, вне цикла планирования, и не добавляет задержку в `Score`.
Альтернатива через kube-state-metrics требует ещё одного компонента и не
удовлетворяет требованию T7.1 экспортировать метрику из плагина.

## Последствия

### Положительные

- Метрики решения доступны в реальном времени, без офлайн-джойна Binding.
- Дашборд Grafana и Gate 7 опираются на одни имена.

### Отрицательные / компромиссы

- Если только один узел feasible, фреймворк пропускает `PreScore`/`Score`; тогда
  `PostBind` берёт зону из Node informer, а оценки CF/энергии нет.
- Метрики дублируют часть смысла v1.0.0 (`fallback_total` ⊂ `scheduler_fallback_total`).

### Влияние на SSOT и код

- Каталоги / пакеты: `pkg/metrics/prometheus.go` (новый), `pkg/scheduler/decision.go`, `pkg/carbon/api`.
- CRD / API: REST/OpenAPI не меняется; `/metrics` получает новые семейства.
- Эксперименты / метрики: `metrics.md` → v1.1.0 (§2a, §3a); `01_architecture_ssot.md` §6.7 читать вместе с этим ADR.

## Альтернативы

| Вариант | Почему отклонён |
| --- | --- |
| `Reserve` | считает попытки, которые могут быть откатаны `Unreserve`; Gate 7 требует число размещений |
| kube-state-metrics + PromQL-джойн | не метрика плагина; ещё один компонент на стенде |
| Переименовать метрики v1.0.0 под имена T7.1 | нарушает Freeze (VER-06), ломает существующие тесты и анализ |

## Проверка

- `pkg/scheduler/plugin_test.go`: плагин реализует `PostBind`, не реализует `Bind`/`Reserve`/`Filter`/`Permit`.
- `pkg/scheduler/decision_test.go`: счётчик решений +1 на `PostBind`, оценка CF = E·CI при постоянной CI, `Score` без новых аллокаций.
- Gate 7 (`docs/audit/gate_7_monitoring_validation.md`): число подов = прирост `carbon_aware_scheduler_decisions_total`.
