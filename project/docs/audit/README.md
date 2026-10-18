# Аудит

Журналы проверок агентов и ревью (чеклисты структуры, линта, воспроизводимости стенда). Не дублировать SSOT.

| Файл | Содержание |
| --- | --- |
| [gate_1_research_review.md](gate_1_research_review.md) | Quality Gate 1: аудит T1.1–T1.3, глава 1, вердикт PASS, Stage 2 UNLOCKED |
| [T2.1-math-model-ssot.md](T2.1-math-model-ssot.md) | T2.1: математическая модель зафиксирована в `docs/ssot/02_math_model_ssot.md`; передача в T2.2 |
| [T2.2-math-model-validation.md](T2.2-math-model-validation.md) | T2.2: валидация модели, 48/48 PASS, рекомендация Quality Gate 2 PASS |
| [gate_2_math_review.md](gate_2_math_review.md) | Quality Gate 2: SSOT Lock, ADR-001, глава 2, вердикт PASS, Stage 3 UNLOCKED |
| [T3.1-architecture-ssot.md](T3.1-architecture-ssot.md) | T3.1: компонентная архитектура в `docs/ssot/01_architecture_ssot.md`; ADR-002; передача в T3.2/T3.3 |
| [T3.4-failure-fallback-policy.md](T3.4-failure-fallback-policy.md) | T3.4: матрица сбоев, автомат Fallback, Anti-Thrashing; канон `docs/ssot/failure_and_fallback_policy.md`; передача в T5.2/T5.3 |
| [T3.2-api-contracts.md](T3.2-api-contracts.md) | T3.2: REST/OpenAPI CDS, DTO `api/v1alpha1`, annotations; канон `docs/ssot/03_api_contracts_ssot.md` |
| [gate_3_architecture_review.md](gate_3_architecture_review.md) | Quality Gate 3: SSOT Freeze архитектуры/API/Fallback/метрик, глава 3, вердикт PASS, Stage 4 и Stage 5 UNLOCKED |
| [T5.1-scheduler-plugin-skeleton.md](T5.1-scheduler-plugin-skeleton.md) | T5.1: скелет `CarbonAware`, `cmd/scheduler`, сборка `bin/kube-scheduler` против k8s 1.37; передача в T5.2 |
| [T5.2-cache-sync-worker.md](T5.2-cache-sync-worker.md) | T5.2 (задание): Cache Sync Worker, `LocalCarbonCache` (`atomic.Pointer`), last-good при отказе CDS; эстафета PreScore/Score |
| [T5.3-extension-points.md](T5.3-extension-points.md) | T5.3 (задание): PreScore/Score/NormalizeScore, CycleState, Fallback; эстафета T5.4 (формулы) |
| [T5.4-energy-carbon-integral.md](T5.4-energy-carbon-integral.md) | T5.4: `LinearPowerModel`, интеграл (2.10s)+η, оракулы T2.2; эстафета T5.5 |
| [T5.5-scheduler-unit-tests.md](T5.5-scheduler-unit-tests.md) | T5.5: unit-тесты CarbonAware (сценарии 1–5, NormalizeScore 3/5/20), покрытие 96.3%; эстафета Quality Gate 5 (T5.6) |
| [gate_5_scheduler_plugin_audit.md](gate_5_scheduler_plugin_audit.md) | Quality Gate 5: аудит плагина (модель, CycleState, Score 94 ns/op), вердикт PASS, Stage 6 UNLOCKED |
| [gate_6_k8s_integration_report.md](gate_6_k8s_integration_report.md) | Quality Gate 6: kind + CDS + custom scheduler, Binding Pod на north, вердикт PASS, Stage 7 и Stage 8 UNLOCKED |
| [gate_7_monitoring_validation.md](gate_7_monitoring_validation.md) | Quality Gate 7: метрики плагина и CDS, Prometheus (2 с) и Grafana, 5×50 Pod, счётчик решений = 50, задержка < 3 с, вердикт PASS |
| [gate_8_testing_audit.md](gate_8_testing_audit.md) | Quality Gate 8: integration, E2E и chaos-сьюты, 5 из 5 прогонов зелёные, вердикт PASS, Stage 9 UNLOCKED |
| [gate_9_performance_scalability_report.md](gate_9_performance_scalability_report.md) | Quality Gate 9: задержки 3–50 нод, overhead 0,08 мс при 20 нодах, pprof, O(N), исправлены O(F²) и аллокация в Score, вердикт PASS, Stage 10 UNLOCKED |
| [T4.1-carbon-service-skeleton.md](T4.1-carbon-service-skeleton.md) | T4.1: каркас `cmd/carbon-service`, конфиг YAML+env, stubs provider/cache/api; эстафета T4.2 |
| [T4.2-carbon-providers.md](T4.2-carbon-providers.md) | T4.2: Electricity Maps live-клиент, Replay Engine, фабрика `NewProvider`; эстафета T4.3 |
| [T4.3-cds-cache-rest.md](T4.3-cds-cache-rest.md) | T4.3: in-memory кэш (`atomic.Pointer`), REST CDS, бенчмарк Get ~155 ns/op; эстафета T4.4 |
| [T4.4-cds-tests.md](T4.4-cds-tests.md) | T4.4: тесты CDS, cache 94.7% / api 96.9%, цикл Replay→кэш→REST; эстафета Quality Gate 4 (T4.5) |
| [gate_4_carbon_service_review.md](gate_4_carbon_service_review.md) | Quality Gate 4: аудит CDS (горутины, HTTP body, кэш, OpenAPI, lint), вердикт PASS |
