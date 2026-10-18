# Stage 9 benchmarks

Scheduling-overhead benchmarks for the `CarbonAware` plugin (T9.1) and the
scalability audit (T9.2). Report: [`docs/audit/gate_9_performance_scalability_report.md`](../docs/audit/gate_9_performance_scalability_report.md).

The harness runs the real kube-scheduler v1.37 in-process with the production
profiles from `deployments/kubernetes/scheduler-config.yaml`, a fake clientset
as the API server and the real Carbon Data Service on loopback.

| Command | What it does |
| --- | --- |
| `make bench` | `go test -bench=BenchmarkSchedule -benchmem ./benchmarks/...` |
| `make bench-report` | full pipeline (`run_benchmarks.sh`): benchmarks, 50 pods/s load per profile, `results_latency.json`, pprof profiles and plots in `docs/audit/evidence/gate9/` |

Benchmarks: `BenchmarkScheduleCycle` (full algorithm, profiles × 3/5/10/20/50
nodes, reports p50/p95/p99), `BenchmarkScheduleScorePerNode`,
`BenchmarkSchedulePreScore`, `BenchmarkScheduleNormalizeScore`,
`BenchmarkSchedulePreScoreForecast` (forecast length 0…288),
`BenchmarkSchedulePreScoreParallel` (contention probe).

`TestLatencyReport` and `TestLoad50PodsPerSecond` skip unless their
environment variables are set (see `run_benchmarks.sh`), so `go test ./...`
stays fast.
