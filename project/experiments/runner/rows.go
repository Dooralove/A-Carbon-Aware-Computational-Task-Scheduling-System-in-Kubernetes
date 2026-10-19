package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"

	"carbon-aware-k8s/experiments/workloads/batch"
)

// taskHeader is the per-task CSV schema (one row per task Pod). The T10.2
// required columns are pod_id, node, zone, duration, energy_kwh, carbon_gco2,
// scheduled_at, completed_at and sla_violated; experiments/README.md
// documents every column.
var taskHeader = []string{
	"run_id", "experiment", "strategy", "scheduler_name", "scenario", "dataset_id", "replay_multiplier",
	"repetition", "seed", "w_c", "w_r", "w_p", "carbon_weight_annotation", "deadline_factor",
	"task_index", "pod_id", "class", "workload_class", "cpu_request_m", "memory_request_mib", "burn_cores",
	"expected_duration_s", "deadline_s", "arrival_offset_s",
	"node", "zone", "status",
	"submitted_at", "scheduled_at", "started_at", "completed_at",
	"sched_latency_s", "wait_s", "duration", "turnaround_s", "sla_violated",
	"p_pod_w", "energy_kwh", "carbon_gco2", "ci_avg_gco2_kwh",
	"ci_at_schedule", "ci_min_at_schedule", "ci_max_at_schedule", "best_zone_at_schedule", "model_time_at_schedule",
	"cpu_seconds_task", "cpu_seconds_cadvisor", "cpu_utilization_task",
}

// runHeader is the per-run CSV schema (one row per run).
var runHeader = []string{
	"run_id", "experiment", "strategy", "scheduler_name", "scenario", "dataset_id", "replay_multiplier",
	"repetition", "seed", "w_c", "w_r", "w_p", "carbon_weight_annotation", "deadline_factor",
	"tasks", "arrival_rate", "burn_fraction", "power_idle_w", "power_max_w",
	"run_started_at", "run_finished_at", "model_time_at_start",
	"baseline_host_cpu_util", "host_cpu_util_mean", "host_cpu_util_max",
	"succeeded", "failed", "unscheduled",
	"total_energy_kwh", "total_carbon_gco2", "mean_duration_s", "mean_turnaround_s", "makespan_s",
	"sla_violations", "svr", "zone_shares",
	"sched_latency_p50_s", "sched_latency_p95_s", "sched_latency_p99_s",
	"sched_attempt_p50_s", "sched_attempt_p95_s", "sched_attempt_p99_s", "sched_attempts",
	"score_ext_p50_s", "score_ext_p99_s",
	"decisions_delta", "fallback_delta", "ci_series_complete",
}

type csvFile struct {
	f *os.File
	w *csv.Writer
}

// openCSV opens path for appending and writes header if the file is new or
// empty. An existing file with a different header is an error.
func openCSV(path string, header []string) (*csvFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	existing, err := readHeader(path)
	if err != nil {
		return nil, err
	}
	if existing != nil && strings.Join(existing, ",") != strings.Join(header, ",") {
		return nil, fmt.Errorf("%s: header differs from the runner schema", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	c := &csvFile{f: f, w: csv.NewWriter(f)}
	if existing == nil {
		if err := c.WriteRows([][]string{header}); err != nil {
			f.Close()
			return nil, err
		}
	}
	return c, nil
}

func readHeader(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h, err := csv.NewReader(f).Read()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	return h, err
}

// WriteRows writes and fsyncs, so an interrupted series keeps every finished run.
func (c *csvFile) WriteRows(rows [][]string) error {
	if err := c.w.WriteAll(rows); err != nil {
		return err
	}
	return c.f.Sync()
}

func (c *csvFile) Close() error { return c.f.Close() }

func existingRunIDs(path string) (map[string]bool, error) {
	out := map[string]bool{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	for i, r := range rows {
		if i == 0 || len(r) == 0 {
			continue
		}
		out[r[0]] = true
	}
	return out, nil
}

type rowContext struct {
	opts        options
	plan        RunPlan
	nodes       map[string]nodeInfo
	metrics     *runMetrics
	t0, tEnd    time.Time
	modelAt     time.Time
	baselineCPU float64
}

const (
	statusSucceeded   = "succeeded"
	statusFailed      = "failed"
	statusUnscheduled = "unscheduled"
	statusCreateError = "create_error"
)

func buildRows(rc rowContext, tasks []batch.Task, traces map[string]podTrace, logs map[string]*taskResult) *runResult {
	p := rc.plan
	meta := planColumns(p)
	var rows [][]string
	var totalE, totalC, sumDur, sumTurn float64
	var nDur, nTurn, succeeded, failed, unscheduled, violations int
	var makespanEnd time.Time
	var schedLat []float64
	zoneCount := map[string]int{}

	for _, t := range tasks {
		tr := traces[t.Name]
		status := statusUnscheduled
		switch {
		case tr.createErr != nil:
			status = statusCreateError
		case tr.phase == v1.PodSucceeded:
			status = statusSucceeded
		case tr.phase == v1.PodFailed:
			status = statusFailed
		case tr.node != "":
			status = "unfinished"
		}
		zone := rc.nodes[tr.node].zone
		if tr.node != "" {
			zoneCount[zone]++
		}
		res := logs[t.Name]
		var started, completed time.Time
		if res != nil {
			started, _ = time.Parse(time.RFC3339Nano, res.StartedAt)
			completed, _ = time.Parse(time.RFC3339Nano, res.FinishedAt)
		}
		if completed.IsZero() && tr.final != nil {
			if cs := terminated(tr.final); cs != nil {
				started, completed = cs.StartedAt.Time.UTC(), cs.FinishedAt.Time.UTC()
			}
		}

		pPod := math.NaN()
		if n, ok := rc.nodes[tr.node]; ok && n.allocCPUMilli > 0 {
			// Linear marginal power of the Pod (SSOT 2.4, idle not attributed):
			// (P_max - P_idle) * cpu_request / allocatable.
			pPod = (rc.opts.powerMaxWatts - rc.opts.powerIdleWatts) * float64(t.Class.CPUMilli) / float64(n.allocCPUMilli)
		}
		duration, energy, carbon, ciAvg := math.NaN(), math.NaN(), math.NaN(), math.NaN()
		if !started.IsZero() && !completed.IsZero() && completed.After(started) {
			duration = completed.Sub(started).Seconds()
			if res != nil && res.ActualDurationS > 0 {
				duration = res.ActualDurationS
			}
			energy = pPod * duration / 3.6e6
			if s, ok := rc.metrics.ci[zone]; ok {
				integral, covered := s.Integrate(started, completed)
				if covered {
					carbon = pPod * integral / 3.6e6
					ciAvg = integral / completed.Sub(started).Seconds()
				}
			}
		}
		turnaround := math.NaN()
		if !completed.IsZero() && !tr.submittedAt.IsZero() {
			turnaround = completed.Sub(tr.submittedAt).Seconds()
		}
		violated := true
		if status == statusSucceeded && !math.IsNaN(turnaround) {
			violated = t.Deadline > 0 && turnaround > t.Deadline.Seconds()
		}
		if t.Deadline == 0 && status == statusSucceeded {
			violated = false
		}
		schedLatency := math.NaN()
		if !tr.boundAt.IsZero() && !tr.submittedAt.IsZero() {
			schedLatency = tr.boundAt.Sub(tr.submittedAt).Seconds()
			schedLat = append(schedLat, schedLatency)
		}
		wait := math.NaN()
		if !started.IsZero() && !tr.submittedAt.IsZero() {
			wait = started.Sub(tr.submittedAt).Seconds()
		}
		ciAt, ciMin, ciMax, best := ciAtSchedule(rc.metrics.ci, zone, tr.boundAt)
		modelAt := ""
		if !tr.boundAt.IsZero() && rc.metrics.modelTime != nil {
			if v, ok := rc.metrics.modelTime.At(tr.boundAt); ok {
				modelAt = time.Unix(0, int64(v*1e9)).UTC().Format(time.RFC3339)
			}
		}
		cpuTask, cpuUtil := math.NaN(), math.NaN()
		if res != nil {
			cpuTask = res.CPUUserS + res.CPUSysS
			cpuUtil = res.CPUUtilization
		}
		cpuCad, ok := rc.metrics.cpuSeconds[t.Name]
		if !ok {
			cpuCad = math.NaN()
		}

		switch status {
		case statusSucceeded:
			succeeded++
		case statusFailed:
			failed++
		default:
			unscheduled++
		}
		if violated {
			violations++
		}
		if !math.IsNaN(energy) {
			totalE += energy
		}
		if !math.IsNaN(carbon) {
			totalC += carbon
		}
		if !math.IsNaN(duration) {
			sumDur += duration
			nDur++
		}
		if !math.IsNaN(turnaround) {
			sumTurn += turnaround
			nTurn++
		}
		if completed.After(makespanEnd) {
			makespanEnd = completed
		}

		row := append([]string{}, meta...)
		row = append(row,
			itoa(t.Index), t.Name, t.Class.Name, string(t.Class.WorkloadClass),
			strconv.FormatInt(t.Class.CPUMilli, 10), strconv.FormatInt(t.Class.MemoryMiB, 10), ff(t.BurnCores),
			ff(t.Duration.Seconds()), ff(t.Deadline.Seconds()), ff(t.ArrivalOffset.Seconds()),
			tr.node, zone, status,
			ts(tr.submittedAt), ts(tr.boundAt), ts(started), ts(completed),
			ff(schedLatency), ff(wait), ff(duration), ff(turnaround), strconv.FormatBool(violated),
			ff(pPod), fe(energy), fe(carbon), ff(ciAvg),
			ff(ciAt), ff(ciMin), ff(ciMax), best, modelAt,
			ff(cpuTask), ff(cpuCad), ff(cpuUtil),
		)
		rows = append(rows, row)
	}

	n := len(tasks)
	shares := make([]string, 0, len(zoneCount))
	for _, z := range sortedKeys(zoneCount) {
		shares = append(shares, fmt.Sprintf("%s:%.3f", z, float64(zoneCount[z])/float64(n)))
	}
	m := rc.metrics
	makespan := math.NaN()
	if !makespanEnd.IsZero() {
		makespan = makespanEnd.Sub(rc.t0).Seconds()
	}
	lq := quantiles(schedLat, 0.5, 0.95, 0.99)
	runRow := append([]string{}, meta...)
	runRow = append(runRow,
		itoa(n), ff(rc.opts.arrivalRate), ff(rc.opts.burnFraction), ff(rc.opts.powerIdleWatts), ff(rc.opts.powerMaxWatts),
		ts(rc.t0), ts(rc.tEnd), rc.modelAt.Format(time.RFC3339),
		ff(rc.baselineCPU), ff(m.hostCPUMean), ff(m.hostCPUMax),
		itoa(succeeded), itoa(failed), itoa(unscheduled),
		fe(totalE), fe(totalC), ff(mean(sumDur, nDur)), ff(mean(sumTurn, nTurn)), ff(makespan),
		itoa(violations), ff(float64(violations)/float64(n)), strings.Join(shares, ";"),
		ff(lq[0]), ff(lq[1]), ff(lq[2]),
		ff(m.attemptQ[0]), ff(m.attemptQ[1]), ff(m.attemptQ[2]), ff(m.attempts),
		ff(m.scoreQ[0]), ff(m.scoreQ[1]),
		ff(m.decisions), ff(m.fallbacks), strconv.FormatBool(allCovered(rows)),
	)
	summary := fmt.Sprintf("ok=%d fail=%d unsched=%d CF=%.4fg E=%.6fkWh SVR=%.2f zones=%s",
		succeeded, failed, unscheduled, totalC, totalE, float64(violations)/float64(n), strings.Join(shares, ";"))
	return &runResult{taskRows: rows, runRow: runRow, summary: summary}
}

// allCovered reports whether every succeeded task got a carbon value.
func allCovered(rows [][]string) bool {
	iStatus, iCarbon := indexOf(taskHeader, "status"), indexOf(taskHeader, "carbon_gco2")
	for _, r := range rows {
		if r[iStatus] == statusSucceeded && r[iCarbon] == "" {
			return false
		}
	}
	return true
}

func indexOf(h []string, name string) int {
	for i, v := range h {
		if v == name {
			return i
		}
	}
	return -1
}

func planColumns(p RunPlan) []string {
	wc, wr, wp := "", "", ""
	if w := p.EffectiveWeights(); w != nil {
		wc, wr, wp = ff(w[0]), ff(w[1]), ff(w[2])
	}
	ann := ""
	if p.CarbonWeight != nil {
		ann = ff(*p.CarbonWeight)
	}
	df := "1.5"
	if p.DeadlineFactor > 0 {
		df = ff(p.DeadlineFactor)
	}
	return []string{
		p.ID, p.Experiment, p.Strategy.Name, p.Strategy.SchedulerName, p.Scenario.Name, p.Scenario.DatasetID,
		ff(p.Scenario.Multiplier), itoa(p.Repetition), strconv.FormatUint(p.Seed, 10), wc, wr, wp, ann, df,
	}
}

// ciAtSchedule returns CI of the chosen zone, the min/max over zones and the
// lowest-CI zone at the bind instant.
func ciAtSchedule(ci map[string]stepSeries, zone string, at time.Time) (cur, lo, hi float64, best string) {
	cur, lo, hi = math.NaN(), math.NaN(), math.NaN()
	if at.IsZero() {
		return
	}
	for _, z := range sortedKeys(ci) {
		v, ok := ci[z].At(at)
		if !ok {
			continue
		}
		if z == zone {
			cur = v
		}
		if math.IsNaN(lo) || v < lo {
			lo, best = v, z
		}
		if math.IsNaN(hi) || v > hi {
			hi = v
		}
	}
	return
}

func terminated(p *v1.Pod) *v1.ContainerStateTerminated {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name == batch.ContainerName && cs.State.Terminated != nil {
			return cs.State.Terminated
		}
	}
	return nil
}

func quantiles(xs []float64, qs ...float64) []float64 {
	out := make([]float64, len(qs))
	if len(xs) == 0 {
		for i := range out {
			out[i] = math.NaN()
		}
		return out
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	for i, q := range qs {
		// Linear interpolation between closest ranks (numpy default).
		pos := q * float64(len(s)-1)
		lo := int(math.Floor(pos))
		hi := int(math.Ceil(pos))
		out[i] = s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
	}
	return out
}

func mean(sum float64, n int) float64 {
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

func itoa(v int) string { return strconv.Itoa(v) }

func ff(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// fe formats small energy/carbon values without losing precision.
func fe(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return ""
	}
	return strconv.FormatFloat(v, 'g', 10, 64)
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
