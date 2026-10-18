# Интерфейсы

Нормативный документ контрактов REST, DTO, labels/annotations, plugin args и `CycleState`: [`03_api_contracts_ssot.md`](03_api_contracts_ssot.md) v1.0.1 (`accepted` **LOCKED**, T3.2, Quality Gate 3).

Код группы API: [`api/v1alpha1/types.go`](../../api/v1alpha1/types.go). OpenAPI 3.0: [`api/v1alpha1/openapi.yaml`](../../api/v1alpha1/openapi.yaml).

Настоящий файл сохранён как стабильный путь из `CONF-03` и **не** является вторым каноном. Содержание, противоречащее `03_api_contracts_ssot.md`, недействительно.

Краткий указатель:

| Контракт | Где описан |
| --- | --- |
| CDS REST (`/api/v1/healthz`, `/zones`, `/current`, `/forecast`) | `03_api_contracts_ssot.md` §3 |
| `CarbonRecord`, `ForecastInterval`, `ZoneProfile` | §4; `api/v1alpha1` |
| `CarbonProvider`, `EnergyModel`, `CarbonCalculator`, `Clock` | §5 |
| Pod labels/annotations `carbon-aware.io/*` | §6 |
| Node labels `zone`, `power-profile` | §6.7 |
| `pluginConfig.args` / `CarbonAwarePluginArgs` | §4.7 |
| `CycleState` / `CarbonCycleState` | §7 |
| G-* → HTTP / `fwk.Status` | §8 |
| CRD `CarbonPolicy` | **не** входит в MVP (`SCOPE-OUT-12`) |
