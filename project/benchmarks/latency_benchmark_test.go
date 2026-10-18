package benchmarks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeapi "k8s.io/apimachinery/pkg/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
	carbonaware "carbon-aware-k8s/pkg/scheduler"
)

// ---------------------------------------------------------------------------
// Shared fixtures. Scheduler environments are expensive (informers, CDS,
// Cache Sync Workers), and a sub-benchmark body runs several times while the
// framework sizes b.N, so each cluster size is built once per process.
// ---------------------------------------------------------------------------

var (
	envMu sync.Mutex
	envs  = map[int]*schedEnv{}
)

func envFor(tb testing.TB, n int) *schedEnv {
	tb.Helper()
	envMu.Lock()
	defer envMu.Unlock()
	if e, ok := envs[n]; ok {
		return e
	}
	e := newSchedEnvShared(tb, n)
	envs[n] = e
	return e
}

// newSchedEnvShared builds an env whose lifetime is the test process, not one
// (sub)benchmark: Cleanup of the first caller must not stop it.
func newSchedEnvShared(tb testing.TB, n int) *schedEnv {
	tb.Helper()
	h := &processTB{TB: tb}
	return newSchedEnv(h, n)
}

// processTB swallows Cleanup so shared envs survive the sub-test that built them.
type processTB struct{ testing.TB }

func (p *processTB) Cleanup(func()) {}

// pluginFixture is a standalone CarbonAware instance with the production args
// of carbon-aware-scheduler and a snapshot pulled from the CDS, used to time a
// single extension point without the rest of the framework around it.
type pluginFixture struct {
	plugin *carbonaware.CarbonAwarePlugin
	nodes  []fwk.NodeInfo
	pod    *k8sframework.QueuedPodInfo
}

func newPluginFixture(tb testing.TB, n int) *pluginFixture {
	tb.Helper()
	env := envFor(tb, n)
	cdsURL := startCDS(&processTB{TB: tb})
	args := carbonAwareArgs(tb, cdsURL)
	p, err := carbonaware.NewForTests(args, nil, nil)
	if err != nil {
		tb.Fatal(err)
	}
	c := carbonaware.NewLocalCarbonCache(v1alpha1.SystemClock{}, args.ClockSkewSeconds, args.MaxDataAgeSeconds)
	client := carbonaware.NewCarbonServiceClient(cdsURL, 2*time.Second)
	if err := c.UpdateFromService(context.Background(), client); err != nil {
		tb.Fatalf("snapshot from CDS: %v", err)
	}
	snap := c.CarbonSnapshot()
	if snap == nil || len(snap.Zones) != len(zoneCI) {
		tb.Fatalf("snapshot zones=%v", snap)
	}
	p.SeedSnapshot(*snap)
	return &pluginFixture{
		plugin: p,
		nodes:  env.nodeInfos(tb),
		pod:    queued(tb, workloadPod("fixture", profileCarbonAware)),
	}
}

func carbonAwareArgs(tb testing.TB, cdsURL string) carbonaware.PluginArgs {
	tb.Helper()
	for _, prof := range loadProfiles(tb, cdsURL) {
		if prof.SchedulerName != profileCarbonAware {
			continue
		}
		for _, pc := range prof.PluginConfig {
			if pc.Name != carbonaware.Name {
				continue
			}
			u, ok := pc.Args.(*runtimeapi.Unknown)
			if !ok {
				tb.Fatalf("CarbonAware args are %T", pc.Args)
			}
			var args carbonaware.PluginArgs
			if err := json.Unmarshal(u.Raw, &args); err != nil {
				tb.Fatal(err)
			}
			return args
		}
	}
	tb.Fatal("carbon-aware-scheduler has no CarbonAware pluginConfig")
	return carbonaware.PluginArgs{}
}

func (f *pluginFixture) preScore(tb testing.TB) fwk.CycleState {
	state := k8sframework.NewCycleState()
	if st := f.plugin.PreScore(context.Background(), state, f.pod.Pod, f.nodes); !st.IsSuccess() {
		tb.Fatalf("PreScore: %v", st)
	}
	return state
}

// ---------------------------------------------------------------------------
// testing.B benchmarks: go test -bench=BenchmarkSchedule -benchmem ./benchmarks/...
// ---------------------------------------------------------------------------

// BenchmarkScheduleScorePerNode times one Score call (one node). Score is a
// CycleState lookup; it must not depend on cluster size or allocate.
func BenchmarkScheduleScorePerNode(b *testing.B) {
	for _, n := range nodeCounts {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			f := newPluginFixture(b, n)
			state := f.preScore(b)
			ni := f.nodes[len(f.nodes)/2]
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, st := f.plugin.Score(ctx, state, f.pod.Pod, ni); !st.IsSuccess() {
					b.Fatal(st)
				}
			}
		})
	}
}

// BenchmarkSchedulePreScore times the per-Pod scoring pass over all feasible
// nodes: snapshot load, P(u), the CF integral and min-max for every node.
func BenchmarkSchedulePreScore(b *testing.B) {
	for _, n := range nodeCounts {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			f := newPluginFixture(b, n)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state := k8sframework.NewCycleState()
				if st := f.plugin.PreScore(ctx, state, f.pod.Pod, f.nodes); !st.IsSuccess() {
					b.Fatal(st)
				}
			}
		})
	}
}

// BenchmarkScheduleNormalizeScore times NormalizeScore over the node list.
func BenchmarkScheduleNormalizeScore(b *testing.B) {
	for _, n := range nodeCounts {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			f := newPluginFixture(b, n)
			state := f.preScore(b)
			ctx := context.Background()
			base := make(fwk.NodeScoreList, len(f.nodes))
			for i, ni := range f.nodes {
				s, _ := f.plugin.Score(ctx, state, f.pod.Pod, ni)
				base[i] = fwk.NodeScore{Name: ni.Node().Name, Score: s}
			}
			scores := make(fwk.NodeScoreList, len(base))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				copy(scores, base)
				if st := f.plugin.NormalizeScore(ctx, state, f.pod.Pod, scores); !st.IsSuccess() {
					b.Fatal(st)
				}
			}
		})
	}
}

// BenchmarkSchedulePreScoreParallel runs PreScore from GOMAXPROCS goroutines
// against one plugin instance and one shared snapshot. It is the contention
// probe for -mutexprofile/-blockprofile (Gate 9).
func BenchmarkSchedulePreScoreParallel(b *testing.B) {
	f := newPluginFixture(b, 50)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			state := k8sframework.NewCycleState()
			if st := f.plugin.PreScore(ctx, state, f.pod.Pod, f.nodes); !st.IsSuccess() {
				b.Error(st)
				return
			}
		}
	})
}

// BenchmarkScheduleCycle times the full scheduling algorithm of the real
// kube-scheduler (snapshot refresh + SchedulePod) for each profile and
// cluster size, and reports p50/p95/p99 next to ns/op.
func BenchmarkScheduleCycle(b *testing.B) {
	for _, prof := range allProfiles {
		for _, n := range nodeCounts {
			b.Run(fmt.Sprintf("profile=%s/nodes=%d", prof, n), func(b *testing.B) {
				env := envFor(b, n)
				qpi := queued(b, workloadPod("cycle", prof))
				samples := make([]time.Duration, 0, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_, d, err := env.cycle(prof, qpi)
					if err != nil {
						b.Fatal(err)
					}
					samples = append(samples, d)
				}
				b.StopTimer()
				s := summarize(samples)
				b.ReportMetric(s.P50Us, "p50-µs")
				b.ReportMetric(s.P95Us, "p95-µs")
				b.ReportMetric(s.P99Us, "p99-µs")
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Report: CARBON_BENCH_REPORT=benchmarks/results_latency.json go test -run TestLatencyReport ./benchmarks/
// ---------------------------------------------------------------------------

const (
	reportWarmup        = 300
	reportCycleRounds   = 3000 // ≥1000 per T9.1, per profile and cluster size
	reportPluginIters   = 5000
	reportScoreBatch    = 200 // Score calls per timed sample (time.Now costs ~30 ns)
	overheadBudgetMs20  = 5.0
	overheadBudgetNodes = 20
)

type cycleResult struct {
	Profile         string             `json:"profile"`
	Nodes           int                `json:"nodes"`
	Latency         latencyStats       `json:"latency"`
	SelectedZones   map[string]int     `json:"selected_zones"`
	SelectedZoneMix map[string]float64 `json:"selected_zone_share"`
}

type overheadResult struct {
	Profile    string  `json:"profile"`
	Nodes      int     `json:"nodes"`
	MeanUs     float64 `json:"mean_delta_us"`
	MeanCI95Us float64 `json:"mean_delta_ci95_us"`
	P50Us      float64 `json:"p50_delta_us"`
	P95Us      float64 `json:"p95_delta_us"`
	P99Us      float64 `json:"p99_delta_us"`
	RelP50Pct  float64 `json:"p50_delta_pct"`
}

type pluginPointResult struct {
	Nodes             int          `json:"nodes"`
	PreScore          latencyStats `json:"prescore"`
	ScorePerNode      latencyStats `json:"score_per_node"`
	NormalizeScore    latencyStats `json:"normalize_score"`
	PluginTotalMeanUs float64      `json:"plugin_total_mean_us"`
	PluginTotalP99Us  float64      `json:"plugin_total_p99_us"`
}

type latencyReport struct {
	GeneratedAt string              `json:"generated_at"`
	Environment map[string]any      `json:"environment"`
	Method      map[string]any      `json:"method"`
	Cycle       []cycleResult       `json:"scheduling_cycle"`
	Overhead    []overheadResult    `json:"overhead_vs_default"`
	Plugin      []pluginPointResult `json:"plugin_extension_points"`
	Acceptance  map[string]any      `json:"acceptance"`
	Load        map[string]any      `json:"load_50_pods_per_sec,omitempty"`
	Heap        heapStability       `json:"heap_stability"`
}

type heapCheckpoint struct {
	Cycles     int     `json:"cycles"`
	HeapMiB    float64 `json:"heap_objects_after_gc_mib"`
	Goroutines int     `json:"goroutines"`
}

type heapStability struct {
	Profile         string           `json:"profile"`
	Nodes           int              `json:"nodes"`
	Checkpoints     []heapCheckpoint `json:"checkpoints"`
	SlopeKiBPer10k  float64          `json:"slope_kib_per_10k_cycles"`
	GrowthFirstLast float64          `json:"growth_first_to_last_kib"`
}

func TestLatencyReport(t *testing.T) {
	out := os.Getenv("CARBON_BENCH_REPORT")
	if out == "" {
		t.Skip("set CARBON_BENCH_REPORT=<path> to run the T9.1 latency report")
	}
	rep := latencyReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Environment: environment(),
		Method: map[string]any{
			"scheduler":        "k8s.io/kubernetes/pkg/scheduler v1.37.0, in-process, fake clientset",
			"profiles_source":  schedulerConfigMap + " (+ upstream default profile)",
			"carbon_data":      "real CDS cache + REST on loopback, 3 zones, 12x5min forecast",
			"cycle_definition": "Cache.UpdateSnapshot + Scheduler.SchedulePod (PreFilter..NormalizeScore, host selection); no bind",
			"rounds":           reportCycleRounds,
			"warmup":           reportWarmup,
			"interleaving":     "profiles alternate within each round, order rotated per round",
			"plugin_iters":     reportPluginIters,
			"score_batch":      reportScoreBatch,
			"pod":              "100m CPU, 128Mi, expected-duration 3600s, sla-deadline 14400s",
			"node":             nodeCPU + " CPU, " + nodeMemory + ", one resident pod (500..1500m)",
		},
	}

	for _, n := range nodeCounts {
		env := envFor(t, n)
		cyc := measureCycles(t, env, n)
		rep.Cycle = append(rep.Cycle, cyc...)
		def := cyc[0]
		for _, c := range cyc[1:] {
			rep.Overhead = append(rep.Overhead, overhead(def, c, env.lastSamples))
		}
		rep.Plugin = append(rep.Plugin, measurePluginPoints(t, n))
		t.Logf("nodes=%d default p50=%.1fµs carbon-aware p50=%.1fµs", n, def.Latency.P50Us, cyc[1].Latency.P50Us)
	}

	var ov20 overheadResult
	for _, o := range rep.Overhead {
		if o.Profile == profileCarbonAware && o.Nodes == overheadBudgetNodes {
			ov20 = o
		}
	}
	worst := math.Max(ov20.P99Us, math.Max(ov20.P50Us, ov20.MeanUs+ov20.MeanCI95Us)) / 1e3
	rep.Acceptance = map[string]any{
		"criterion":                 "carbon-aware scheduling overhead < 5 ms per pod at 20 nodes",
		"overhead_mean_ms":          round3(ov20.MeanUs / 1e3),
		"overhead_p50_ms":           round3(ov20.P50Us / 1e3),
		"overhead_p99_ms":           round3(ov20.P99Us / 1e3),
		"worst_of_mean_ci_p50_p99":  round3(worst),
		"budget_ms":                 overheadBudgetMs20,
		"pass":                      worst < overheadBudgetMs20,
		"carbon_fallbacks_observed": counterValue(t, "carbon_aware_scheduler_fallback_total", nil),
	}
	rep.Heap = measureHeapStability(t)
	if load := readLoadResults(t); len(load) > 0 {
		rep.Load = load
	}

	buf, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(buf, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s; acceptance %v", out, rep.Acceptance)
	if worst >= overheadBudgetMs20 {
		t.Errorf("overhead %.3f ms ≥ %.1f ms at %d nodes", worst, overheadBudgetMs20, overheadBudgetNodes)
	}
}

// measureCycles interleaves the three profiles so drift (GC, CPU frequency,
// noisy neighbours) hits all of them equally.
func measureCycles(t *testing.T, env *schedEnv, n int) []cycleResult {
	t.Helper()
	qpis := make(map[string]*k8sframework.QueuedPodInfo, len(allProfiles))
	for _, p := range allProfiles {
		qpis[p] = queued(t, workloadPod("report-"+p, p))
	}
	for i := 0; i < reportWarmup; i++ {
		for _, p := range allProfiles {
			if _, _, err := env.cycle(p, qpis[p]); err != nil {
				t.Fatal(err)
			}
		}
	}
	runtime.GC()
	samples := make(map[string][]time.Duration, len(allProfiles))
	zones := make(map[string]map[string]int, len(allProfiles))
	for _, p := range allProfiles {
		samples[p] = make([]time.Duration, 0, reportCycleRounds)
		zones[p] = map[string]int{}
	}
	for r := 0; r < reportCycleRounds; r++ {
		for k := range allProfiles {
			p := allProfiles[(r+k)%len(allProfiles)]
			host, d, err := env.cycle(p, qpis[p])
			if err != nil {
				t.Fatal(err)
			}
			samples[p] = append(samples[p], d)
			zones[p][zoneOfNode(host)]++
		}
	}
	env.lastSamples = samples
	out := make([]cycleResult, 0, len(allProfiles))
	for _, p := range allProfiles {
		share := map[string]float64{}
		for z, c := range zones[p] {
			share[z] = round3(float64(c) / float64(reportCycleRounds))
		}
		out = append(out, cycleResult{
			Profile:         p,
			Nodes:           n,
			Latency:         summarize(samples[p]),
			SelectedZones:   zones[p],
			SelectedZoneMix: share,
		})
	}
	return out
}

// measureHeapStability runs carbon-aware cycles at 50 nodes and reads the
// live heap after a forced GC every heapStep cycles. A leak per cycle (for
// example a CycleState or metric series kept alive) shows as a positive slope.
func measureHeapStability(t *testing.T) heapStability {
	t.Helper()
	const (
		nodes    = 50
		heapStep = 5000
		steps    = 6
	)
	env := envFor(t, nodes)
	qpi := queued(t, workloadPod("heap", profileCarbonAware))
	res := heapStability{Profile: profileCarbonAware, Nodes: nodes}
	for s := 0; s <= steps; s++ {
		if s > 0 {
			for i := 0; i < heapStep; i++ {
				if _, _, err := env.cycle(profileCarbonAware, qpi); err != nil {
					t.Fatal(err)
				}
			}
		}
		runtime.GC()
		runtime.GC()
		res.Checkpoints = append(res.Checkpoints, heapCheckpoint{
			Cycles:     s * heapStep,
			HeapMiB:    round3(heapObjectsMiB()),
			Goroutines: runtime.NumGoroutine(),
		})
	}
	// Fit from the first full step on; checkpoint 0 precedes warm caches.
	xs := make([]float64, 0, steps)
	ys := make([]float64, 0, steps)
	for _, c := range res.Checkpoints[1:] {
		xs = append(xs, float64(c.Cycles)/10000)
		ys = append(ys, c.HeapMiB*1024)
	}
	res.SlopeKiBPer10k = round2(slope(xs, ys))
	res.GrowthFirstLast = round2((res.Checkpoints[len(res.Checkpoints)-1].HeapMiB - res.Checkpoints[1].HeapMiB) * 1024)
	return res
}

func slope(xs, ys []float64) float64 {
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(ys))
	var sxx, sxy float64
	for i := range xs {
		sxx += (xs[i] - mx) * (xs[i] - mx)
		sxy += (xs[i] - mx) * (ys[i] - my)
	}
	if sxx == 0 {
		return 0
	}
	return sxy / sxx
}

func overhead(def, c cycleResult, samples map[string][]time.Duration) overheadResult {
	a, b := samples[def.Profile], samples[c.Profile]
	ma, va := meanVar(a)
	mb, vb := meanVar(b)
	se := math.Sqrt(va/float64(len(a)) + vb/float64(len(b)))
	rel := 0.0
	if def.Latency.P50Us > 0 {
		rel = (c.Latency.P50Us - def.Latency.P50Us) / def.Latency.P50Us * 100
	}
	return overheadResult{
		Profile:    c.Profile,
		Nodes:      c.Nodes,
		MeanUs:     round2(mb - ma),
		MeanCI95Us: round2(1.96 * se),
		P50Us:      round2(c.Latency.P50Us - def.Latency.P50Us),
		P95Us:      round2(c.Latency.P95Us - def.Latency.P95Us),
		P99Us:      round2(c.Latency.P99Us - def.Latency.P99Us),
		RelP50Pct:  round2(rel),
	}
}

func meanVar(ds []time.Duration) (mean, variance float64) {
	if len(ds) == 0 {
		return 0, 0
	}
	var sum float64
	for _, d := range ds {
		sum += float64(d.Nanoseconds()) / 1e3
	}
	mean = sum / float64(len(ds))
	if len(ds) < 2 {
		return mean, 0
	}
	var ss float64
	for _, d := range ds {
		v := float64(d.Nanoseconds())/1e3 - mean
		ss += v * v
	}
	return mean, ss / float64(len(ds)-1)
}

func measurePluginPoints(t *testing.T, n int) pluginPointResult {
	t.Helper()
	f := newPluginFixture(t, n)
	ctx := context.Background()

	pre := make([]time.Duration, 0, reportPluginIters)
	var state fwk.CycleState
	for i := 0; i < reportWarmup+reportPluginIters; i++ {
		s := k8sframework.NewCycleState()
		start := time.Now()
		st := f.plugin.PreScore(ctx, s, f.pod.Pod, f.nodes)
		d := time.Since(start)
		if !st.IsSuccess() {
			t.Fatalf("PreScore: %v", st)
		}
		if i >= reportWarmup {
			pre = append(pre, d)
		}
		state = s
	}

	ni := f.nodes[len(f.nodes)/2]
	score := make([]time.Duration, 0, reportPluginIters)
	for i := 0; i < reportPluginIters; i++ {
		start := time.Now()
		for j := 0; j < reportScoreBatch; j++ {
			if _, st := f.plugin.Score(ctx, state, f.pod.Pod, ni); !st.IsSuccess() {
				t.Fatalf("Score: %v", st)
			}
		}
		score = append(score, time.Since(start)/reportScoreBatch)
	}

	base := make(fwk.NodeScoreList, len(f.nodes))
	for i, ni := range f.nodes {
		s, _ := f.plugin.Score(ctx, state, f.pod.Pod, ni)
		base[i] = fwk.NodeScore{Name: ni.Node().Name, Score: s}
	}
	scores := make(fwk.NodeScoreList, len(base))
	norm := make([]time.Duration, 0, reportPluginIters)
	for i := 0; i < reportPluginIters; i++ {
		copy(scores, base)
		start := time.Now()
		st := f.plugin.NormalizeScore(ctx, state, f.pod.Pod, scores)
		norm = append(norm, time.Since(start))
		if !st.IsSuccess() {
			t.Fatalf("NormalizeScore: %v", st)
		}
	}

	ps, ss, ns := summarize(pre), summarize(score), summarize(norm)
	return pluginPointResult{
		Nodes:             n,
		PreScore:          ps,
		ScorePerNode:      ss,
		NormalizeScore:    ns,
		PluginTotalMeanUs: round2(ps.MeanUs + float64(n)*ss.MeanUs + ns.MeanUs),
		PluginTotalP99Us:  round2(ps.P99Us + float64(n)*ss.P99Us + ns.P99Us),
	}
}

func environment() map[string]any {
	host, _ := os.Hostname()
	return map[string]any{
		"go":         runtime.Version(),
		"goos":       runtime.GOOS,
		"goarch":     runtime.GOARCH,
		"cpus":       runtime.NumCPU(),
		"gomaxprocs": runtime.GOMAXPROCS(0),
		"cpu_model":  cpuModel(),
		"host":       host,
	}
}

func cpuModel() string {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if len(line) > 10 && line[:10] == "model name" {
			for i := 0; i < len(line); i++ {
				if line[i] == ':' {
					return strings.TrimSpace(line[i+1:])
				}
			}
		}
	}
	return ""
}

func sqrt(v float64) float64   { return math.Sqrt(v) }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// forecastSnapshot builds a snapshot whose zones each carry intervals 5-minute
// forecast points starting now (intervals ≤ v1alpha1.MaxForecastIntervals).
func forecastSnapshot(now time.Time, intervals int) v1alpha1.CarbonSnapshot {
	zones := make(map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState, len(zoneCI))
	for z, ci := range zoneCI {
		fc := make([]v1alpha1.ForecastInterval, 0, intervals)
		for k := 0; k < intervals; k++ {
			ts := now.Add(time.Duration(k) * 5 * time.Minute)
			fc = append(fc, v1alpha1.ForecastInterval{
				Timestamp:                  v1alpha1.RFC3339Time{Time: ts},
				ExpiresAt:                  v1alpha1.RFC3339Time{Time: ts.Add(5 * time.Minute)},
				DurationSeconds:            300,
				HorizonSeconds:             int64(k) * 300,
				CarbonIntensityGCO2ePerKWh: ci * (0.9 + 0.1*float64(k%3)),
				Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
			})
		}
		zones[v1alpha1.ZoneID(z)] = v1alpha1.ZoneCarbonState{
			Current: v1alpha1.CarbonRecord{
				Zone:                       v1alpha1.ZoneID(z),
				Timestamp:                  v1alpha1.RFC3339Time{Time: now},
				CarbonIntensityGCO2ePerKWh: ci,
				Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
				Source:                     v1alpha1.SourceMock,
				EmissionFactorType:         v1alpha1.EmissionLifecycle,
				Quality:                    v1alpha1.QualityFresh,
			},
			Forecast: fc,
		}
	}
	return v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: now},
		Quality:   v1alpha1.QualityFresh,
		Zones:     zones,
	}
}

// forecastLengths spans no forecast (persistence) up to the SSOT cap of
// 288 five-minute intervals (24 h).
var forecastLengths = []int{0, 12, 48, 144, 288}

// BenchmarkSchedulePreScoreForecast fixes N=20 and a 24 h job and varies the
// forecast length F, the second size parameter of PreScore.
func BenchmarkSchedulePreScoreForecast(b *testing.B) {
	for _, fl := range forecastLengths {
		b.Run(fmt.Sprintf("nodes=20/intervals=%d", fl), func(b *testing.B) {
			f := newPluginFixture(b, 20)
			f.plugin.SeedSnapshot(forecastSnapshot(time.Now().UTC().Truncate(time.Second), fl))
			pod := workloadPod("forecast", profileCarbonAware)
			pod.Annotations[v1alpha1.AnnotationExpectedDuration] = "86400s"
			pod.Annotations[v1alpha1.AnnotationSLADeadline] = "172800s"
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state := k8sframework.NewCycleState()
				if st := f.plugin.PreScore(ctx, state, pod, f.nodes); !st.IsSuccess() {
					b.Fatal(st)
				}
			}
		})
	}
}
