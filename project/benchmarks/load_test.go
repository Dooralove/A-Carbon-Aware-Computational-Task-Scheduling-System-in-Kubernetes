package benchmarks_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Load run: CARBON_BENCH_LOAD_PROFILE=<profile> CARBON_BENCH_LOAD_OUT=<file>
// go test -run TestLoad50PodsPerSecond ./benchmarks/
//
// One profile per process, so RSS and heap numbers are not polluted by the
// other profile's scheduler. The process hosts the scheduler, the fake API
// server (object tracker + watch fan-out) and the load generator, so CPU and
// RSS are an upper bound for kube-scheduler alone; the default-scheduler run
// carries exactly the same extra load, which makes the delta meaningful.
const (
	loadRatePerSec = 50
	loadDuration   = 30 * time.Second
	loadNodes      = 20
	loadSampleTick = 100 * time.Millisecond
	loadDrainLimit = 60 * time.Second
)

type loadResult struct {
	Profile         string             `json:"profile"`
	Nodes           int                `json:"nodes"`
	TargetRate      int                `json:"target_pods_per_sec"`
	DurationSec     float64            `json:"submit_duration_sec"`
	Submitted       int                `json:"submitted"`
	Bound           int                `json:"bound"`
	ThroughputPerS  float64            `json:"bound_pods_per_sec"`
	CreateToBind    latencyStats       `json:"create_to_bind"`
	CPUSeconds      float64            `json:"process_cpu_seconds"`
	CPUCoresAvg     float64            `json:"process_cpu_cores_avg"`
	HeapLiveBaseMiB float64            `json:"heap_objects_baseline_mib"`
	HeapLivePeakMiB float64            `json:"heap_objects_peak_mib"`
	HeapLiveMeanMiB float64            `json:"heap_objects_mean_mib"`
	HeapAfterGCMiB  float64            `json:"heap_objects_after_gc_mib"`
	RSSPeakMiB      float64            `json:"process_rss_peak_mib"`
	GoroutinesStart int                `json:"goroutines_start"`
	GoroutinesEnd   int                `json:"goroutines_end"`
	ZoneShare       map[string]float64 `json:"selected_zone_share"`
}

func TestLoad50PodsPerSecond(t *testing.T) {
	prof := os.Getenv("CARBON_BENCH_LOAD_PROFILE")
	out := os.Getenv("CARBON_BENCH_LOAD_OUT")
	if prof == "" || out == "" {
		t.Skip("set CARBON_BENCH_LOAD_PROFILE and CARBON_BENCH_LOAD_OUT to run the 50 pods/s load")
	}
	env := newSchedEnv(t, loadNodes)
	go env.sched.Run(env.ctx)

	runtime.GC()
	res := loadResult{Profile: prof, Nodes: loadNodes, TargetRate: loadRatePerSec}
	res.HeapLiveBaseMiB = round2(heapObjectsMiB())
	res.GoroutinesStart = runtime.NumGoroutine()

	// Heap sampler.
	var (
		sampMu   sync.Mutex
		peak     float64
		sum      float64
		nsamples int
	)
	stopSampler := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		tk := time.NewTicker(loadSampleTick)
		defer tk.Stop()
		for {
			select {
			case <-stopSampler:
				return
			case <-tk.C:
				v := heapObjectsMiB()
				sampMu.Lock()
				if v > peak {
					peak = v
				}
				sum += v
				nsamples++
				sampMu.Unlock()
			}
		}
	}()

	cpu0 := cpuSeconds()
	wall0 := time.Now()
	total := int(loadDuration.Seconds()) * loadRatePerSec
	created := make(map[string]time.Time, total)
	interval := time.Second / loadRatePerSec
	next := time.Now()
	for i := 0; i < total; i++ {
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		next = next.Add(interval)
		pod := workloadPod(fmt.Sprintf("load-%05d", i), prof)
		created[pod.Name] = time.Now()
		if _, err := env.client.CoreV1().Pods(pod.Namespace).Create(env.ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create %s: %v", pod.Name, err)
		}
	}
	res.DurationSec = round2(time.Since(wall0).Seconds())
	res.Submitted = total

	// Drain: wait for every pod to be bound.
	deadline := time.Now().Add(loadDrainLimit)
	for time.Now().Before(deadline) {
		env.bindMu.Lock()
		n := len(env.boundAt)
		env.bindMu.Unlock()
		if n >= total {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	wall := time.Since(wall0).Seconds()
	res.CPUSeconds = round3(cpuSeconds() - cpu0)
	res.CPUCoresAvg = round3(res.CPUSeconds / wall)
	close(stopSampler)
	<-samplerDone

	env.bindMu.Lock()
	lat := make([]time.Duration, 0, len(env.boundAt))
	var last time.Time
	zones := map[string]int{}
	for name, at := range env.boundAt {
		c, ok := created[name]
		if !ok {
			continue
		}
		lat = append(lat, at.Sub(c))
		if at.After(last) {
			last = at
		}
		zones[zoneOfNode(env.bindNode[name])]++
	}
	env.bindMu.Unlock()
	res.Bound = len(lat)
	res.CreateToBind = summarize(lat)
	if !last.IsZero() {
		res.ThroughputPerS = round2(float64(res.Bound) / last.Sub(wall0).Seconds())
	}
	res.ZoneShare = map[string]float64{}
	for z, c := range zones {
		res.ZoneShare[z] = round3(float64(c) / float64(max(res.Bound, 1)))
	}
	sampMu.Lock()
	res.HeapLivePeakMiB = round2(peak)
	if nsamples > 0 {
		res.HeapLiveMeanMiB = round2(sum / float64(nsamples))
	}
	sampMu.Unlock()
	env.cancel()
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	res.HeapAfterGCMiB = round2(heapObjectsMiB())
	res.RSSPeakMiB = rssPeakMiB()
	res.GoroutinesEnd = runtime.NumGoroutine()

	buf, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(buf, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", buf)
	if res.Bound != total {
		t.Errorf("bound %d of %d pods", res.Bound, total)
	}
}

// readLoadResults merges results_load_<profile>.json written next to the
// report, if the load runs were made first (make bench-report does that).
func readLoadResults(t *testing.T) map[string]any {
	t.Helper()
	dir := filepath.Dir(os.Getenv("CARBON_BENCH_REPORT"))
	out := map[string]any{}
	for _, p := range allProfiles {
		raw, err := os.ReadFile(filepath.Join(dir, "results_load_"+p+".json"))
		if err != nil {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("results_load_%s.json: %v", p, err)
		}
		out[p] = v
	}
	return out
}

var heapSample = []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}

func heapObjectsMiB() float64 {
	metrics.Read(heapSample)
	return float64(heapSample[0].Value.Uint64()) / (1 << 20)
}

func cpuSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

// rssPeakMiB reads VmHWM (peak resident set) from /proc/self/status.
func rssPeakMiB() float64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0
		}
		kb, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return 0
		}
		return round2(kb / 1024)
	}
	return 0
}
