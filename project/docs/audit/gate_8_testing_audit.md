# Quality Gate 8: комплексное тестирование (Unit, Integration, E2E, Chaos)

- **Дата:** 2026-10-18
- **Роль:** Head of Quality Assurance & Reliability Director
- **Статус:** **PASS** (с оговорками из §6)
- **Решение:** Stage 8 завершён, Stage 9 (бенчмаркинг) разблокирован.
- **Проверяемый код:** коммит `277077b` (PR #1 с сьютами T8.1–T8.3, влит в `main` как `0fc21aa`).
- **Канон:** [`failure_and_fallback_policy.md`](../ssot/failure_and_fallback_policy.md), [`metrics.md`](../ssot/metrics.md), [`01_architecture_ssot.md`](../ssot/01_architecture_ssot.md).
- **Сырые результаты:** [`gate_8/test_all_5x.log`](gate_8/test_all_5x.log) (вывод `make test-all TEST_REPEAT=5`).

## 1. Итоговый вердикт

`make test-all TEST_REPEAT=5` завершился с кодом 0. Каждый из 5 последовательных прогонов включал два шага: `go test -race ./...` (unit, `tests/unit`, `tests/integration`) и `go test -tags=e2e ./tests/e2e/...` против живого kind-кластера.

| Показатель | Результат |
| --- | --- |
| Прогонов подряд | 5 из 5 зелёные |
| Упавших тестов | 0 |
| Flaky (разный исход на одном коммите) | 0 |
| `panic` / `DATA RACE` в выводе | 0 / 0 |
| Зависаний (тайм-аут `go test`) | 0 |
| Поды, оставшиеся в `Pending` при наличии ресурсов | 0 |

Время одного прогона: интеграционный сьют около 20,5 с, E2E + chaos около 195–203 с.

## 2. Состав тестов

### 2.1. T8.1: интеграционный сьют (`tests/integration`, без тега, `-race`)

Реальный CDS (provider → `cache.Cache` + `Loop` → REST `api.Server` на `httptest`) соединён через loopback HTTP с плагином. Плагин создаётся производственной фабрикой `scheduler.New`, поэтому Worker-горутина тоже настоящая.

| Тест | Что доказывает |
| --- | --- |
| `TestCacheSyncDynamicProviderChange` | North 80 → 500 у провайдера доходит до снимка плагина, а решение PreScore/Score/Normalize переключается с north на west |
| `TestCacheSyncPropagationLatency` | задержка провайдер → снимок плагина на 6 изменениях; в verbose-прогоне p50 = 1,002 с, max = 1,010 с при тике Worker 1 с (граница 2,1 с) |
| `TestCacheValidityWhenServiceUnavailable` | после закрытия CDS тик падает быстро, last-good и `IsValid` сохраняются (FF-INV-12); возраст 0 / 91 / 300 с даёт углеродное ранжирование, 301 с даёт G-08 (`fallback{dead}` +1); холодный кэш даёт `fallback{empty}` +1 |
| `TestWorkerOutageAndRecovery` | CDS отвечает 503, Worker пишет `sync_failure_total` и не подменяет снимок; после восстановления `fetchedAt` движется вперёд |
| `TestNetworkJitterWithinTimeout` | jitter 0–300 мс на каждый ответ, 8 горутин крутят скоринг, провайдер переключается; более 50 000 циклов без ошибки и без fallback; распространение около 1,5 с |
| `TestNetworkDelayBeyondTimeoutKeepsLastGood` | задержка 1 с при таймауте 200 мс: тик завершается не позже timeout + 300 мс, last-good сохраняется |
| `TestIntermittentJitterNeverPublishesPartialSnapshot` | 10 % запросов дольше таймаута (seed фиксирован): 26 успешных и 14 упавших тиков, около 977 000 конкурентных чтений, 0 частичных снимков |

### 2.2. T8.2: E2E (`tests/e2e/scheduling_e2e_test.go`, тег `e2e`, client-go)

| Тест | Результат в каждом из 5 прогонов |
| --- | --- |
| `TestE2EBasicPlacementOnGreenestZone` | 10/10 подов в зоне north (CDS: north = 100, west = 400); `scoring_attempts` ≥ +10, fallback +0 |
| `TestE2ESchedulerIsolation` | 10 подов `default-scheduler`, даже с меткой opt-in: событие `Scheduled` от `default-scheduler`, счётчики CarbonAware не меняются, распределение north:6 / west:4. Подтест opt-out: `opt_out_total` +4, `scoring_attempts` +0, fallback +0 (FF-INV-13) |
| `TestE2EPodMetadataAndStatus` | `schedulerName`, `nodeName`, условия `PodScheduled`/`Ready`, `hostIP` принадлежит узлу, входные `carbon-aware.io/*` не переписаны, событие `Scheduled` от `carbon-aware-scheduler` с именем узла |

Ресурсы убираются автоматически: каждый тест работает в своём namespace `e2e-*`, который удаляется (с ожиданием) в `t.Cleanup`.

### 2.3. T8.3: Chaos (`tests/e2e/chaos_fallback_test.go`, тег `e2e`)

| Сценарий | Инъекция | Наблюдение (5/5 прогонов) |
| --- | --- | --- |
| Chaos 1: CDS остановлен во время планирования | `replicas=0` после 4-го из 20 подов | 20/20 подов Running, все в north (last-good в окне); после age > maxDataAge 5/5 подов Running, `fallback{dead}` ≥ +5, в логе есть `cache sync tick failed`. Рестарт планировщика при лежащем CDS: 3/3 пода Running, `fallback{empty}` ≥ 3 (FF-CRASH) |
| Chaos 2: устаревание данных | процесс CDS заморожен (`SIGSTOP` через `docker exec` на узле kind), под не пересоздаётся | при staleWarn < age < maxDataAge поды идут в north, `stale_in_window_total` ≥ +3, `fallback{dead}` +0; при age > maxDataAge 6/6 подов Running, `fallback{dead}` ≥ +6, распределение north:3 / west:3; после `SIGCONT` снимок снова свежий и поды снова идут в north |
| Chaos 3: перегрузка CPU | filler-поды занимают north до allocatable − 100m | 5/5 проб по 200m ушли в west, на north нет оверкоммита; под больше любого свободного остатка остаётся Pending с `FailedScheduling: Insufficient cpu` от NodeResourcesFit; fallback +0 |
| Chaos 4: скачок CI (north 100 → 900, west 400 → 100) | смена replay-датасета CDS и рестарт CDS | 4 ранее запущенных пода: тот же UID, тот же узел, `restartCount=0`; 6/6 новых подов в west; fallback +0 |

## 3. Покрытие матрицы сбоев (§4 политики)

| ID | Где проверено |
| --- | --- |
| FF-CDS (down / 5xx / timeout) | integration (`WorkerOutageAndRecovery`, `NetworkDelayBeyondTimeout`), chaos 1 |
| FF-CDS-COLD / FF-EMPTY | integration (cold plugin), chaos 1 (рестарт планировщика), unit `TestPreScoreEmptySnapshotIsG08` |
| FF-STALE | integration (возраст 91 и 300 с), chaos 2 (окно staleWarn..maxDataAge), unit `TestPreScoreStaleInWindowStillRanks` |
| FF-DEAD | integration (граница 300/301 с), chaos 1 и 2, unit `TestPreScoreDeadSnapshotIsG08` |
| FF-INV-12 (all-or-nothing publish) | integration (`IntermittentJitter…`, `ValidityWhenServiceUnavailable`) |
| FF-OPT | E2E isolation / opt-out, unit `TestPreScoreOptOutDoesNotUseSnapshotOrFallback` |
| FF-RES, FF-RES-ONE | chaos 3 |
| FF-CRASH | chaos 1, подтест рестарта |
| FF-NAN / FF-NEG / FF-FUT | unit (`TestPreScoreInvalidCIIsG09ThenAllInvalid`, `TestPreScoreFutureTimestampInvalid`, `TestRefreshRejectsInvalidAndFuture`) |
| FF-ZONE / FF-ZONE-404 / FF-ALL-BAD | unit (`TestPreScoreMissingZoneIsG09NotUnschedulable`, `TestPreScoreUnknownZoneInSnapshotIsG09`, `TestPreScoreAllUnknownIsG08`) |
| FF-SLA | unit (`TestPreScoreHardSLA`, `TestPreScoreG20MissingHorizon`) |
| FF-STATE | unit (`TestScoreMissingCycleStateIsSuccessNeutral`) |
| FF-EM (live Electricity Maps) | unit провайдера (`TestElectricityMapsTimeout`, `…RetryThenSuccess`); в kind не проверялось, стенд работает на replay |
| FF-PROM (Prometheus down) | не проверено: Prometheus разворачивает Stage 7 |
| FF-NO-FC | неявно: датасеты E2E без прогноза (`forecastMode: none`), поэтому скоринг везде идёт на persistence (2.11) |

Не покрыты только FF-PROM (зависит от Stage 7) и FF-EM в кластере (нужен live-ключ).

## 4. Стенд и как воспроизвести

```bash
make kind-up kind-load            # kind v0.33.0, Kubernetes 1.37.0, 1 CP + 4 workers
make e2e-deploy                   # kubectl apply -k tests/e2e/manifests
make test-all TEST_REPEAT=5
```

Оверлей `tests/e2e/manifests` отличается от `deployments/kubernetes` только следующим:

- CDS читает детерминированные replay-датасеты `e2e_baseline.json` и `e2e_spike.json`, тик CDS 2 с;
- пороги плагина уменьшены в 10 раз: refresh 2 с, staleWarn 10 с, maxDataAge 30 с, httpTimeout 1 с (канон 30/90/300/2). Тесты читают их из ConfigMap, поэтому вернуть канон можно правкой одного файла;
- `--authorization-always-allow-paths` включает `/metrics`, чтобы тесты читали счётчики через pod proxy API-сервера;
- liveness CDS терпит заморозку до 3 мин (нужно chaos 2).

Корректность оверлея проверяется без кластера (`TestE2EDatasetsLoadInReplayEngine`, `TestE2ESchedulerConfigPatchIsValid`).

## 5. Отклонения от формулировок задач

1. **Порог 300 с в кластере.** В kind он масштабирован до 30 с. Точная граница 300/301 с проверена в интеграционном тесте на `FakeClock`.
2. **«Счётчик fallback растёт при каждом сценарии сбоя».** По канону (`FF-INV-11`, `metrics.md`) `carbon_aware_fallback_total` считает только G-08. Поэтому в chaos 3 (нехватка ресурсов, это Filter) и chaos 4 (скачок CI, это свежие данные) тесты требуют, чтобы счётчик **не** рос. В chaos 1 и 2 рост проверяется.
3. **Имя метрики.** Stage 7 переименовывает счётчик в `carbon_aware_scheduler_fallback_total`. Тесты принимают оба имени, без TODO и без build-тега. В этом прогоне фактически читался `carbon_aware_fallback_total`, так как Stage 7 ещё не влит.
4. **Chaos 2.** Первая версия резала endpoints через смену селектора Service, но планировщик продолжал синхронизацию по keep-alive соединению. Поэтому сценарий переведён на `SIGSTOP` процесса CDS. Это наблюдение полезно и для эксплуатации: снятие пода с endpoints не обрывает уже открытое соединение Worker.

## 6. Оговорки (что не проверялось или проверялось не штатным путём)

- **CI в GitHub нет.** Все результаты получены локально в облачном контейнере Linux (4 vCPU, 15 GiB), а не в CI.
- **Окружение контейнера.** Хост на cgroup v1 и не позволяет отрицательный `oom_score_adj`. Для kind применены патчи `failCgroupV1: false` (kubelet) и `restrict_oom_score_adj = true` (containerd). Репозиторный `cluster-config.yaml` не менялся. На обычной машине с cgroup v2 патчи не нужны.
- **Образы.** `make docker-build` в контейнере не прошёл: `go mod download` внутри сборки упёрся в TLS прокси песочницы. Образы `*:0.1.0` собраны из тех же исходников (`go build` на хосте, `CGO_ENABLED=0`) поверх того же distroless-образа. Штатные Dockerfile этим прогоном не проверены.
- **Race detector** включён для unit- и интеграционных тестов. E2E проверяет поведение кластера, гонки в нём не ищутся.
- **golangci-lint** не запускался (не установлен). Запускались `go vet ./...`, `go vet -tags=e2e ./tests/...` и `gofmt`, замечаний нет.

## 7. Заключение

Ни один сценарий отказа источника углеродных данных не оставил ресурсно допустимый под в `Pending`, не вызвал panic или зависание. Пять прогонов подряд дали одинаковый результат. Матрица сбоев покрыта, кроме FF-PROM (за Stage 7) и live FF-EM.

**QUALITY GATE 8: PASS. Stage 9 (нагрузочный бенчмаркинг) разблокирован.**
