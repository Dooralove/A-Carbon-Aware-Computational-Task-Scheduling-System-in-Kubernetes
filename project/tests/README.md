# Тесты

- `unit/` — кросс-пакетные тесты CDS (`carbon_service_test.go`: Replay-файл → кэш → REST)
- `integration/` — carbon-service + фикстуры интенсивности / API
- `e2e/` — сценарии на kind

Предпочтительное расположение модульных тестов Go — рядом с кодом (`pkg/.../*_test.go`).
`tests/unit` — связки нескольких пакетов без внешней сети (`httptest`, `FakeClock`).
