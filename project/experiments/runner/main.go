// Command runner is the Stage 10 experiment harness (T10.2). One command runs
// a series of experiment runs against the kind stand and appends the raw
// per-task data to a CSV file:
//
//	go run ./experiments/runner --experiment=matrix \
//	  --strategy=default,carbon-only,proposed --scenario=stable,dynamic \
//	  --repetitions=5 --out=experiments/results/raw_data_matrix.csv
//
// Every run: (1) deletes all task Pods and waits for host CPU to settle,
// (2) points the Carbon Data Service at the scenario's replay dataset and
// restarts it, then waits until replay model time reaches the scenario
// anchor, (3) submits the task pack with Poisson arrivals, (4) waits until
// every task finishes, (5) queries Prometheus for CI(t), cAdvisor CPU,
// scheduler latency histograms and plugin counters, and (6) appends one row
// per task to --out and one row per run to the runs file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"carbon-aware-k8s/experiments/workloads/batch"
)

type options struct {
	experiment     string
	strategies     []string
	scenarios      []string
	weights        []*float64
	factors        []float64
	repetitions    int
	firstRep       int
	seed           uint64
	tasks          int
	arrivalRate    float64
	burnFraction   float64
	namespace      string
	image          string
	out            string
	runsOut        string
	promURL        string
	cdsURL         string
	kubeconfig     string
	settleCPU      float64
	settleTimeout  time.Duration
	powerIdleWatts float64
	powerMaxWatts  float64
	resume         bool
	dryRun         bool
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		slog.Error("runner failed", "err", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	var o options
	var strategy, scenario, weights, factors string
	var seed uint
	fs.StringVar(&o.experiment, "experiment", "matrix", "series label written to every row (matrix, weights, sla, volatile, ...)")
	fs.StringVar(&strategy, "strategy", "default,carbon-only,proposed", "comma list: default|carbon-only|proposed")
	fs.StringVar(&scenario, "scenario", "stable", "comma list: stable|dynamic|volatile")
	fs.StringVar(&weights, "carbon-weight", "", "comma list of w_C for strategy proposed (Pod annotation carbon-aware.io/carbon-weight)")
	fs.StringVar(&factors, "deadline-factor", "", "comma list of SLA factors k: deadline = k * T̂ (default: per-class 1.5)")
	fs.IntVar(&o.repetitions, "repetitions", 5, "repetitions per combination")
	fs.IntVar(&o.firstRep, "first-repetition", 1, "index of the first repetition (seed = --seed + index)")
	fs.UintVar(&seed, "seed", 1000, "base seed of the task packs")
	fs.IntVar(&o.tasks, "tasks", 50, "tasks per run")
	fs.Float64Var(&o.arrivalRate, "arrival-rate", 0.33, "Poisson arrival rate, tasks/s (0 = all at once)")
	fs.Float64Var(&o.burnFraction, "burn-fraction", 0.1, "share of the CPU request each task actually burns")
	fs.StringVar(&o.namespace, "namespace", "carbon-exp", "namespace for task Pods")
	fs.StringVar(&o.image, "image", batch.DefaultImage, "workload image")
	fs.StringVar(&o.out, "out", "experiments/results/raw_data.csv", "per-task CSV (appended)")
	fs.StringVar(&o.runsOut, "runs-out", "", "per-run CSV (default: <out dir>/runs_<out base>)")
	fs.StringVar(&o.promURL, "prometheus", "http://localhost:9090", "Prometheus base URL")
	fs.StringVar(&o.cdsURL, "cds", "http://localhost:8080", "Carbon Data Service base URL (NodePort)")
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	fs.Float64Var(&o.settleCPU, "settle-cpu", 0.5, "host CPU utilization (0..1) to wait for before a run")
	fs.DurationVar(&o.settleTimeout, "settle-timeout", 90*time.Second, "max wait for --settle-cpu")
	fs.Float64Var(&o.powerIdleWatts, "power-idle-watts", 40, "P_idle of the linear power model (scheduler config)")
	fs.Float64Var(&o.powerMaxWatts, "power-max-watts", 100, "P_max of the linear power model (scheduler config)")
	fs.BoolVar(&o.resume, "resume", true, "skip run_ids already present in --runs-out")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the plan and exit")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.seed = uint64(seed)
	o.strategies = splitList(strategy)
	o.scenarios = splitList(scenario)
	var err error
	if o.weights, err = parseWeights(weights); err != nil {
		return o, err
	}
	if o.factors, err = parseFactors(factors); err != nil {
		return o, err
	}
	if o.runsOut == "" {
		o.runsOut = filepath.Join(filepath.Dir(o.out), "runs_"+strings.TrimPrefix(filepath.Base(o.out), "raw_"))
	}
	if o.tasks <= 0 || o.burnFraction <= 0 || o.burnFraction > 1 {
		return o, errors.New("--tasks must be > 0 and --burn-fraction in (0, 1]")
	}
	return o, nil
}

func run(ctx context.Context, o options) error {
	plan, err := buildPlan(o.experiment, o.strategies, o.scenarios, o.weights, o.factors, o.repetitions, o.firstRep, o.seed)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	if o.resume {
		if done, err = existingRunIDs(o.runsOut); err != nil {
			return err
		}
	}
	if o.dryRun {
		for _, p := range plan {
			mark := ""
			if done[p.ID] {
				mark = " (done)"
			}
			fmt.Printf("%s seed=%d%s\n", p.ID, p.Seed, mark)
		}
		return nil
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.kubeconfig != "" {
		rules.ExplicitPath = o.kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("kubeconfig: %w", err)
	}
	cfg.QPS, cfg.Burst = 50, 100
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	cl := &cluster{
		cs: cs, namespace: o.namespace, cdsURL: o.cdsURL,
		http: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}},
	}
	prom := newPromClient(o.promURL)
	taskW, err := openCSV(o.out, taskHeader)
	if err != nil {
		return err
	}
	defer taskW.Close()
	runW, err := openCSV(o.runsOut, runHeader)
	if err != nil {
		return err
	}
	defer runW.Close()

	if err := warmup(ctx, cl, o, plan); err != nil {
		return fmt.Errorf("warm-up: %w", err)
	}
	for i, p := range plan {
		if done[p.ID] {
			slog.Info("skip finished run", "run", p.ID)
			continue
		}
		slog.Info("run start", "n", fmt.Sprintf("%d/%d", i+1, len(plan)), "run", p.ID, "seed", p.Seed)
		res, err := executeRun(ctx, cl, prom, o, p)
		if err != nil {
			return fmt.Errorf("run %s: %w", p.ID, err)
		}
		if err := taskW.WriteRows(res.taskRows); err != nil {
			return err
		}
		if err := runW.WriteRows([][]string{res.runRow}); err != nil {
			return err
		}
		slog.Info("run done", "run", p.ID, "tasks", len(res.taskRows), "summary", res.summary)
	}
	return nil
}

// warmup schedules one short Pod through each scheduler profile in the plan,
// so their latency histogram series exist in Prometheus before the first
// measured window (increase() needs a sample before the window's first event).
func warmup(ctx context.Context, cl *cluster, o options, plan []RunPlan) error {
	if err := cl.cleanNamespace(ctx); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range plan {
		seen[p.Strategy.SchedulerName] = true
	}
	tasks, err := batch.Generate(batch.Spec{
		Tasks: 1, Seed: 1, BurnFraction: o.burnFraction,
		Classes: []batch.Class{{Name: "warmup", Duration: 2 * time.Second, CPUMilli: 100, MemoryMiB: 16, WorkloadClass: "batch", Weight: 1}},
	})
	if err != nil {
		return err
	}
	for name := range seen {
		pod := tasks[0].Pod(batch.PodOptions{Namespace: o.namespace, RunID: "warmup", SchedulerName: name, Image: o.image, NamePrefix: "warmup-" + name + "-"})
		if _, err := cl.cs.CoreV1().Pods(o.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		l, err := cl.cs.CoreV1().Pods(o.namespace).List(ctx, metav1.ListOptions{LabelSelector: batch.LabelRunID + "=warmup"})
		if err == nil {
			finished := 0
			for _, p := range l.Items {
				if p.Status.Phase == v1.PodSucceeded || p.Status.Phase == v1.PodFailed {
					finished++
				}
			}
			if finished == len(l.Items) {
				// Two scrape intervals so the histograms carry the warm-up sample.
				time.Sleep(5 * time.Second)
				return cl.cleanNamespace(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("warm-up pods did not finish")
}

type runResult struct {
	taskRows [][]string
	runRow   []string
	summary  string
}

func executeRun(ctx context.Context, cl *cluster, prom *promClient, o options, p RunPlan) (*runResult, error) {
	// Step 1: clean state and wait for the host to settle.
	if err := cl.cleanNamespace(ctx); err != nil {
		return nil, fmt.Errorf("clean: %w", err)
	}
	baseline := settleHost(ctx, prom, o.settleCPU, o.settleTimeout)

	// Step 2: replay engine on the scenario, anchored model time.
	cdsPod, modelAt, err := startReplay(ctx, cl, prom, p.Scenario)
	if err != nil {
		return nil, err
	}

	// Step 3: submit the pack.
	tasks, err := batch.Generate(batch.Spec{
		Tasks: o.tasks, ArrivalRate: o.arrivalRate, Seed: p.Seed,
		Classes: batch.DefaultClasses(), BurnFraction: o.burnFraction,
		DeadlineFactor: p.DeadlineFactor,
	})
	if err != nil {
		return nil, err
	}
	nodes, err := cl.nodes(ctx)
	if err != nil {
		return nil, err
	}
	tr := newTracker()
	runLabel := batch.LabelRunID + "=" + p.ID
	stopWatch, err := cl.watchRun(runLabel, tr)
	if err != nil {
		return nil, err
	}
	defer stopWatch()

	t0 := time.Now().UTC()
	var wg sync.WaitGroup
	for _, t := range tasks {
		pod := t.Pod(batch.PodOptions{
			Namespace: o.namespace, RunID: p.ID, SchedulerName: p.Strategy.SchedulerName,
			Image: o.image, CarbonWeight: p.CarbonWeight,
		})
		if d := time.Until(t0.Add(t.ArrivalOffset)); d > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		wg.Add(1)
		go func(pod *v1.Pod) {
			defer wg.Done()
			at := time.Now().UTC()
			_, err := cl.cs.CoreV1().Pods(o.namespace).Create(ctx, pod, metav1.CreateOptions{})
			tr.submitted(pod.Name, at, err)
		}(pod)
	}
	wg.Wait()

	// Step 4: wait for every task (bounded: queueing can stretch a run).
	limit := time.Now().Add(10 * time.Minute)
	for tr.finished() < len(tasks) && time.Now().Before(limit) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	tEnd := time.Now().UTC()
	// Let Prometheus scrape the last samples (2 s interval).
	time.Sleep(6 * time.Second)

	// Step 5: Prometheus + task logs.
	traces := tr.snapshot()
	logs := fetchLogs(ctx, cl, traces)
	m, err := collectMetrics(ctx, prom, o, p, cdsPod, t0, tEnd)
	if err != nil {
		return nil, err
	}

	// Step 6: rows.
	rc := rowContext{opts: o, plan: p, nodes: nodes, metrics: m, t0: t0, tEnd: tEnd, modelAt: modelAt, baselineCPU: baseline}
	res := buildRows(rc, tasks, traces, logs)
	if err := cl.cleanNamespace(ctx); err != nil {
		return nil, fmt.Errorf("cleanup: %w", err)
	}
	return res, nil
}

// startReplay restarts the CDS on the scenario, waits until the plugin's
// Cache Sync Worker has a fresh snapshot from the new CDS Pod, then waits for
// the replay model time to reach the scenario anchor. If the plugin recovers
// only after the anchor has passed, the CDS is restarted again, so every run
// starts at the same model time with fresh scheduler data.
func startReplay(ctx context.Context, cl *cluster, prom *promClient, sc Scenario) (string, time.Time, error) {
	anchor := sc.ModelStart.Add(sc.Anchor)
	slack := time.Duration(float64(2*time.Second) * sc.Multiplier)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		pod, readyAt, err := cl.setReplay(ctx, sc)
		if err != nil {
			return "", time.Time{}, err
		}
		if err := waitPluginFresh(ctx, prom, readyAt, 2*time.Minute); err != nil {
			return "", time.Time{}, err
		}
		modelAt, err := cl.waitModelTime(ctx, anchor, slack)
		if err == nil {
			return pod, modelAt, nil
		}
		lastErr = err
		slog.Warn("replay anchor missed, restarting CDS", "attempt", attempt, "err", err)
	}
	return "", time.Time{}, lastErr
}

// waitPluginFresh waits for a carbon_aware_snapshot_age_seconds sample taken
// after the new CDS became ready that shows a successful sync (age < 2.5 s).
func waitPluginFresh(ctx context.Context, prom *promClient, readyAt time.Time, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		now := time.Now()
		age := scalar(ctx, prom, `max(carbon_aware_snapshot_age_seconds)`, now)
		at := scalar(ctx, prom, `min(timestamp(carbon_aware_snapshot_age_seconds))`, now)
		if !math.IsNaN(age) && !math.IsNaN(at) && at > float64(readyAt.Unix())+1 && age < 2.5 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("scheduler snapshot did not refresh from the restarted CDS")
}

// settleHost waits until host CPU utilization (node-exporter; every kind node
// sees the same host) stays under threshold for two consecutive checks.
func settleHost(ctx context.Context, prom *promClient, threshold float64, timeout time.Duration) float64 {
	const expr = `1 - avg(rate(node_cpu_seconds_total{job="node-exporter",mode="idle"}[10s]))`
	deadline := time.Now().Add(timeout)
	last, below := math.NaN(), 0
	for time.Now().Before(deadline) {
		v, err := prom.QueryScalar(ctx, expr, time.Now())
		if err == nil && !math.IsNaN(v) {
			last = v
			if v < threshold {
				below++
				if below >= 2 {
					return v
				}
			} else {
				below = 0
			}
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(2 * time.Second):
		}
	}
	slog.Warn("host CPU did not settle", "util", last, "threshold", threshold)
	return last
}

func fetchLogs(ctx context.Context, cl *cluster, traces map[string]podTrace) map[string]*taskResult {
	out := map[string]*taskResult{}
	var mu sync.Mutex
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for name, t := range traces {
		if t.phase != v1.PodSucceeded && t.phase != v1.PodFailed {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := cl.taskLog(ctx, name)
			if err != nil {
				slog.Warn("task log", "pod", name, "err", err)
				return
			}
			mu.Lock()
			out[name] = res
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	return out
}

// runMetrics holds what the runner pulls from Prometheus for one run.
type runMetrics struct {
	ci           map[string]stepSeries // zone -> CI(t), gCO2e/kWh
	modelTime    stepSeries            // CDS replay model time, Unix s
	cpuSeconds   map[string]float64    // pod -> cAdvisor CPU seconds
	attemptQ     [3]float64            // p50/p95/p99 scheduling attempt, s
	attempts     float64
	scoreQ       [2]float64 // Score extension point p50/p99, s
	decisions    float64
	fallbacks    float64
	hostCPUMean  float64
	hostCPUMax   float64
	ciCovered    bool
	queryWindowS int
}

func collectMetrics(ctx context.Context, prom *promClient, o options, p RunPlan, cdsPod string, t0, tEnd time.Time) (*runMetrics, error) {
	m := &runMetrics{ci: map[string]stepSeries{}, cpuSeconds: map[string]float64{}}
	// Counters and histograms are read as instant values just before t0 and
	// after the last scrape, and differenced: increase() would extrapolate
	// and miss the first event of a series created inside the window.
	start := t0.Add(-time.Second)
	end := tEnd.Add(4 * time.Second)
	win := int(math.Ceil(end.Sub(t0).Seconds())) + 4
	m.queryWindowS = win
	w := fmt.Sprintf("[%ds]", win)

	cds := fmt.Sprintf(`{job="carbon-data-service",pod=%q}`, cdsPod)
	series, err := prom.QueryRange(ctx, `carbon_aware_zone_intensity_gco2_kwh`+cds, t0.Add(-30*time.Second), end, time.Second)
	if err != nil {
		return nil, fmt.Errorf("ci series: %w", err)
	}
	for _, s := range series {
		m.ci[s.Labels["zone"]] = newStepSeries(s.Points)
	}
	if len(m.ci) == 0 {
		return nil, errors.New("prometheus has no carbon_aware_zone_intensity_gco2_kwh samples for the run")
	}
	mt, err := prom.QueryRange(ctx, `carbon_aware_cds_replay_model_time_seconds`+cds, t0.Add(-30*time.Second), end, time.Second)
	if err == nil && len(mt) > 0 {
		m.modelTime = newStepSeries(mt[0].Points)
	}

	cpu, err := prom.Query(ctx, fmt.Sprintf(`max by (pod) (max_over_time(container_cpu_usage_seconds_total{namespace=%q,container="task"}%s))`, o.namespace, w), end)
	if err != nil {
		return nil, fmt.Errorf("cadvisor: %w", err)
	}
	for _, s := range cpu {
		m.cpuSeconds[s.Labels["pod"]] = s.V
	}

	prof := p.Strategy.SchedulerName
	attempt, n := histDelta(ctx, prom, fmt.Sprintf(`scheduler_scheduling_attempt_duration_seconds_bucket{profile=%q,result="scheduled"}`, prof), start, end)
	m.attempts = n
	for i, q := range []float64{0.5, 0.95, 0.99} {
		m.attemptQ[i] = attempt.quantile(q)
	}
	score, _ := histDelta(ctx, prom, fmt.Sprintf(`scheduler_framework_extension_point_duration_seconds_bucket{profile=%q,extension_point="Score"}`, prof), start, end)
	m.scoreQ = [2]float64{score.quantile(0.5), score.quantile(0.99)}
	if p.Strategy.Weights != nil {
		mode := "expected-cf"
		if p.Strategy.Name == "carbon-only" {
			mode = "current-ci"
		}
		m.decisions = counterDelta(ctx, prom, fmt.Sprintf(`carbon_aware_scheduler_decisions_total{scoring_mode=%q}`, mode), start, end)
		m.fallbacks = counterDelta(ctx, prom, `carbon_aware_scheduler_fallback_total`, start, end)
	} else {
		m.decisions, m.fallbacks = math.NaN(), math.NaN()
	}
	host := `(1 - avg(rate(node_cpu_seconds_total{job="node-exporter",mode="idle"}[10s])))`
	m.hostCPUMean = scalar(ctx, prom, fmt.Sprintf(`avg_over_time(%s[%ds:2s])`, host, win), end)
	m.hostCPUMax = scalar(ctx, prom, fmt.Sprintf(`max_over_time(%s[%ds:2s])`, host, win), end)
	return m, nil
}

// counterDelta is sum(selector)@end - sum(selector)@start; series created
// inside the window count from 0. No component restarts during a run, so
// counter resets do not occur.
func counterDelta(ctx context.Context, prom *promClient, selector string, start, end time.Time) float64 {
	a := scalar(ctx, prom, "sum("+selector+")", start)
	b := scalar(ctx, prom, "sum("+selector+")", end)
	if math.IsNaN(b) {
		return math.NaN()
	}
	if math.IsNaN(a) {
		a = 0
	}
	return b - a
}

// histDelta differences cumulative bucket counts (summed by le) between
// start and end and returns the window histogram and its event count.
func histDelta(ctx context.Context, prom *promClient, selector string, start, end time.Time) (histogram, float64) {
	read := func(at time.Time) map[float64]float64 {
		out := map[float64]float64{}
		s, err := prom.Query(ctx, "sum by (le) ("+selector+")", at)
		if err != nil {
			slog.Warn("prometheus query", "expr", selector, "err", err)
			return out
		}
		for _, x := range s {
			le, err := strconv.ParseFloat(x.Labels["le"], 64)
			if err == nil {
				out[le] = x.V
			}
		}
		return out
	}
	a, b := read(start), read(end)
	var h histogram
	for le, v := range b {
		h = append(h, bucket{le: le, count: v - a[le]})
	}
	sort.Slice(h, func(i, j int) bool { return h[i].le < h[j].le })
	if len(h) == 0 {
		return h, math.NaN()
	}
	return h, h[len(h)-1].count
}

type bucket struct{ le, count float64 }

// histogram is cumulative buckets sorted by le, the last one +Inf.
type histogram []bucket

// quantile mirrors PromQL histogram_quantile: linear interpolation inside
// the bucket that holds rank q*total; the +Inf bucket returns the highest
// finite bound.
func (h histogram) quantile(q float64) float64 {
	if len(h) < 2 {
		return math.NaN()
	}
	total := h[len(h)-1].count
	if total <= 0 {
		return math.NaN()
	}
	rank := q * total
	i := sort.Search(len(h), func(i int) bool { return h[i].count >= rank })
	if i >= len(h)-1 {
		return h[len(h)-2].le
	}
	lo, loCount := 0.0, 0.0
	if i > 0 {
		lo, loCount = h[i-1].le, h[i-1].count
	}
	in := h[i].count - loCount
	if in <= 0 {
		return h[i].le
	}
	return lo + (h[i].le-lo)*(rank-loCount)/in
}

func scalar(ctx context.Context, prom *promClient, expr string, at time.Time) float64 {
	v, err := prom.QueryScalar(ctx, expr, at)
	if err != nil {
		slog.Warn("prometheus query", "expr", expr, "err", err)
		return math.NaN()
	}
	return v
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
