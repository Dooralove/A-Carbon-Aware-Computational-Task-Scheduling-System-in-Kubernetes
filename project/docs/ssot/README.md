# Единый источник правды (SSOT)

Каталог `docs/ssot/` — канонические документы дипломного проекта **Carbon-Aware Kubernetes Scheduler**. Код, манифесты, эксперименты и текст диплома должны ссылаться сюда, а не копировать расходящиеся формулировки.

## Состав

| Файл | Назначение | Кто заполняет |
| --- | --- | --- |
| [engineering-specification.md](engineering-specification.md) | Полная инженерная спецификация (входной канон) | зафиксировано на T0.1 |
| [governance.md](governance.md) | Scope, Definition of Done, code style, версионирование, регламент SSOT, воспроизводимость | архитектор / T0.2, **нормативный документ** v1.4.0 (`SSOT-08` Freeze) |
| [02_math_model_ssot.md](02_math_model_ssot.md) | Математическая модель \(P(u)\), \(\widehat{CF}\), частных скорингов и \(\operatorname{Score}\) | **LOCKED** v1.0.0 (ADR-001, Quality Gate 2). Правка формул — только новым ADR |
| [01_architecture_ssot.md](01_architecture_ssot.md) | Компонентная модель, процессы, PlantUML, изоляция профилей планировщика | **LOCKED** v1.1.0 (ADR-002, Quality Gate 3) |
| [failure_and_fallback_policy.md](failure_and_fallback_policy.md) | Матрица сбоев, автомат Fallback, контракт `fwk.Status`, Anti-Thrashing размещения | **LOCKED** v1.1.0 (T3.4, Quality Gate 3) |
| [03_api_contracts_ssot.md](03_api_contracts_ssot.md) | REST CDS, DTO `api/v1alpha1`, labels/annotations, plugin args, CycleState | **LOCKED** v1.0.1 (T3.2, Quality Gate 3) |
| [architecture.md](architecture.md) | Указатель на `01_architecture_ssot.md` (стабильный путь `CONF-03`) | не канон; не дублировать содержание |
| [glossary.md](glossary.md) | Термины модели и архитектуры: \(CI\), CDS, snapshot, scheduler profile, Fallback | T2.1, дополнено T3.1/T3.2/T3.4 |
| [requirements.md](requirements.md) | Функциональные и нефункциональные требования | аналитик / T0.2 |
| [interfaces.md](interfaces.md) | Указатель на `03_api_contracts_ssot.md` (стабильный путь `CONF-03`) | не канон; не дублировать содержание |
| [metrics.md](metrics.md) | Имена `carbon_aware_*`, типы, лейблы, единицы; KPI ex-post | **LOCKED** v1.0.0 (Quality Gate 3). JSON Grafana — `deployments/` |
| [experiment-protocol.md](experiment-protocol.md) | Сценарии Kind, workloads, критерии успеха | Stage 6 (не блокер архитектуры) |

Черновики исследований — в `docs/research/`. Текст глав диплома — в `docs/diploma_text/`. Решения «почему» — в `docs/decisions_adr/`. Следы проверок агентов — в `docs/audit/`.

## Правила

1. Изменение поведения системы без обновления SSOT считается дефектом процесса.
2. Расхождение SSOT и кода разрешается в пользу явного ADR + правки SSOT, затем кода.
3. Не храните здесь большие датасеты и сырые логи экспериментов.
4. Документы этого каталога **неизменяемы для агентов уровня L2 и ниже** (исполнители: исследование, Go, DevOps, эксперименты, текст). Правки вносит только архитектор (L1); остальные оформляют SSOT Change Request — форма и процедура в [governance.md](governance.md), §5.
5. Перед началом любой задачи агент обязан прочитать [governance.md](governance.md): границы Scope, Definition of Done и правила разрешения конфликтов обязательны для всех.
