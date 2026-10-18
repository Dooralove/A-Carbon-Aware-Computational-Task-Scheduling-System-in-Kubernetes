# Архитектура

Нормативный документ компонентной модели: [`01_architecture_ssot.md`](01_architecture_ssot.md) v1.1.0 (`accepted` **LOCKED**, ADR-002, Quality Gate 3).

Настоящий файл сохранён как стабильный путь из `CONF-03` и **не** является вторым каноном. Содержание, противоречащее `01_architecture_ssot.md`, недействительно.

Краткий указатель:

| Подсистема | Где описана |
| --- | --- |
| Carbon Data Service | `01_architecture_ssot.md` §5 |
| Custom kube-scheduler / `CarbonAware` | §6 |
| Observability | §7; имена — [`metrics.md`](metrics.md) |
| kind multi-zone стенд | §8 |
| Изоляция `default-scheduler` vs `carbon-aware-scheduler` | §12 |
| Формулы \(P(u)\), \(\widehat{CF}\), \(\operatorname{Score}\) | [`02_math_model_ssot.md`](02_math_model_ssot.md) (LOCKED) |
| Отказы, Fallback, Anti-Thrashing | [`failure_and_fallback_policy.md`](failure_and_fallback_policy.md) (LOCKED) |
| REST / DTO / annotations | [`03_api_contracts_ssot.md`](03_api_contracts_ssot.md) (LOCKED) |
| Заморозка пакета | [`002_system_architecture_and_api_freeze.md`](../decisions_adr/002_system_architecture_and_api_freeze.md) |

