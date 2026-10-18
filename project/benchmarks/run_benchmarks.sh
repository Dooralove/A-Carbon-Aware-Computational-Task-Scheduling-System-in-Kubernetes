#!/usr/bin/env bash
# Stage 9 benchmark pipeline (T9.1 + T9.2). Run from project/:
#   bash benchmarks/run_benchmarks.sh
# Outputs:
#   benchmarks/bench_raw.txt              go test -bench=BenchmarkSchedule output
#   benchmarks/results_load_<profile>.json 50 pods/s load runs, one process each
#   benchmarks/results_latency.json        T9.1 report (cycle, overhead, plugin, load)
#   docs/audit/evidence/gate9/             pprof profiles, top lists, graphs, plots
set -euo pipefail

GO=${GO:-go}
EVID=docs/audit/evidence/gate9
BENCHTIME=${BENCHTIME:-1s}
PROFILE_ITERS=${PROFILE_ITERS:-20000}
mkdir -p "$EVID"

echo "== 1/5 testing.B benchmarks (BenchmarkSchedule*)"
$GO test -run '^$' -bench 'BenchmarkSchedule' -benchmem -benchtime "$BENCHTIME" -count 1 ./benchmarks/ | tee benchmarks/bench_raw.txt

echo "== 2/5 load: 50 pods/s for 30 s on 20 nodes, one process per profile"
for p in default-scheduler carbon-aware-scheduler carbon-only-scheduler; do
  CARBON_BENCH_LOAD_PROFILE=$p CARBON_BENCH_LOAD_OUT="$PWD/benchmarks/results_load_$p.json" \
    $GO test -run '^TestLoad50PodsPerSecond$' -count 1 -timeout 10m ./benchmarks/
done

echo "== 3/5 latency report (>=1000 cycles per profile and size)"
CARBON_BENCH_REPORT="$PWD/benchmarks/results_latency.json" \
  $GO test -run '^TestLatencyReport$' -count 1 -timeout 30m -v ./benchmarks/ | grep -E 'nodes=|wrote|PASS|FAIL|ok'

echo "== 4/5 pprof: full cycle, carbon-aware, 50 nodes; PreScore under contention"
$GO test -c -o "$EVID/bench.test" ./benchmarks/
(cd benchmarks && "../$EVID/bench.test" -test.run '^$' \
  -test.bench 'BenchmarkScheduleCycle/profile=carbon-aware-scheduler/nodes=50$' \
  -test.benchtime "${PROFILE_ITERS}x" -test.benchmem \
  -test.cpuprofile "../$EVID/cpu_cycle_carbon50.pprof" \
  -test.memprofile "../$EVID/heap_cycle_carbon50.pprof" -test.memprofilerate 4096 \
  -test.mutexprofile "../$EVID/mutex_cycle_carbon50.pprof" -test.mutexprofilefraction 1 \
  -test.blockprofile "../$EVID/block_cycle_carbon50.pprof" -test.blockprofilerate 1000)
(cd benchmarks && "../$EVID/bench.test" -test.run '^$' \
  -test.bench 'BenchmarkSchedulePreScoreParallel$' -test.benchtime 5s -test.benchmem \
  -test.cpuprofile "../$EVID/cpu_prescore_parallel.pprof" \
  -test.mutexprofile "../$EVID/mutex_prescore_parallel.pprof" -test.mutexprofilefraction 1 \
  -test.blockprofile "../$EVID/block_prescore_parallel.pprof" -test.blockprofilerate 1000)

for prof in cpu_cycle_carbon50 cpu_prescore_parallel; do
  $GO tool pprof -top -nodecount=40 "$EVID/bench.test" "$EVID/$prof.pprof" > "$EVID/${prof}_top.txt" 2>/dev/null
done
$GO tool pprof -top -nodecount=40 -focus 'carbon-aware-k8s/pkg/' "$EVID/bench.test" "$EVID/cpu_cycle_carbon50.pprof" > "$EVID/cpu_cycle_carbon50_plugin_top.txt" 2>/dev/null
$GO tool pprof -sample_index=alloc_space -top -nodecount=40 "$EVID/bench.test" "$EVID/heap_cycle_carbon50.pprof" > "$EVID/heap_cycle_carbon50_alloc_top.txt" 2>/dev/null
$GO tool pprof -sample_index=alloc_space -top -nodecount=40 -focus 'carbon-aware-k8s/pkg/' "$EVID/bench.test" "$EVID/heap_cycle_carbon50.pprof" > "$EVID/heap_cycle_carbon50_plugin_alloc_top.txt" 2>/dev/null
$GO tool pprof -sample_index=inuse_space -top -nodecount=30 "$EVID/bench.test" "$EVID/heap_cycle_carbon50.pprof" > "$EVID/heap_cycle_carbon50_inuse_top.txt" 2>/dev/null
for prof in mutex_cycle_carbon50 block_cycle_carbon50 mutex_prescore_parallel block_prescore_parallel; do
  $GO tool pprof -top -nodecount=25 "$EVID/bench.test" "$EVID/$prof.pprof" > "$EVID/${prof}_top.txt" 2>/dev/null || true
done
if command -v dot >/dev/null; then
  $GO tool pprof -svg -nodecount=60 -focus 'carbon-aware-k8s/pkg/' "$EVID/bench.test" "$EVID/cpu_cycle_carbon50.pprof" > "$EVID/cpu_cycle_carbon50_plugin_graph.svg" 2>/dev/null
  $GO tool pprof -svg -nodecount=60 "$EVID/bench.test" "$EVID/cpu_cycle_carbon50.pprof" > "$EVID/cpu_cycle_carbon50_graph.svg" 2>/dev/null
fi
$GO tool pprof -traces "$EVID/bench.test" "$EVID/cpu_cycle_carbon50.pprof" > "$EVID/cpu_cycle_carbon50.traces" 2>/dev/null
rm -f "$EVID/bench.test"

echo "== 5/5 plots"
python3 benchmarks/plot_results.py benchmarks/results_latency.json benchmarks/bench_raw.txt "$EVID"
rm -f "$EVID/cpu_cycle_carbon50.traces"
echo "done"
