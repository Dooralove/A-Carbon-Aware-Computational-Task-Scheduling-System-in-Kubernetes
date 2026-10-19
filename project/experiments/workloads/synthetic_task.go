// Command synthetic_task is the CPU-bound batch task of the Stage 10
// experiments (T10.1). It is the entrypoint of the carbon-workload-runner image.
//
// Duration mode (default) keeps a controlled CPU load for exactly --duration:
// --cpu cores are split over ceil(--cpu) worker goroutines, and each worker
// runs a duty cycle of busy compute and sleep inside every --period. Work mode
// (--work-units > 0) instead computes a fixed number of kernel iterations at
// full speed, so its runtime depends on the CPU the node actually gives it.
//
// The task needs nothing outside the Go standard library and prints exactly
// one JSON line (Result) to stdout when it finishes or is terminated.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Result is the single stdout line the experiment runner parses.
type Result struct {
	TaskID             string  `json:"task_id"`
	Mode               string  `json:"mode"`
	Kernel             string  `json:"kernel"`
	CPUCores           float64 `json:"cpu_cores"`
	Workers            int     `json:"workers"`
	MemoryMiB          int     `json:"memory_mib"`
	RequestedDurationS float64 `json:"requested_duration_s"`
	StartedAt          string  `json:"started_at"`
	FinishedAt         string  `json:"finished_at"`
	ActualDurationS    float64 `json:"actual_duration_s"`
	WorkUnits          int64   `json:"work_units"`
	CPUUserS           float64 `json:"cpu_user_s"`
	CPUSysS            float64 `json:"cpu_sys_s"`
	// CPUUtilization is (user+sys) / (actual_duration * cpu_cores); 1.0 means
	// the requested load was delivered.
	CPUUtilization float64 `json:"cpu_utilization"`
	Status         string  `json:"status"`
}

type config struct {
	taskID    string
	mode      string
	kernel    string
	cpu       float64
	duration  time.Duration
	workUnits int64
	memoryMiB int
	period    time.Duration
}

const (
	kernelSHA256 = "sha256"
	kernelMatmul = "matmul"

	statusCompleted  = "completed"
	statusTerminated = "terminated"
)

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	res := run(ctx, cfg)
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		os.Exit(1)
	}
	if res.Status != statusCompleted {
		os.Exit(143)
	}
}

func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("synthetic_task", flag.ContinueOnError)
	cfg := config{}
	fs.StringVar(&cfg.taskID, "task-id", os.Getenv("TASK_ID"), "task identifier echoed in the result")
	fs.StringVar(&cfg.kernel, "kernel", kernelSHA256, "compute kernel: sha256 | matmul")
	fs.Float64Var(&cfg.cpu, "cpu", 1, "CPU load in cores (1 = one core at 100%, 0.25 = a quarter of a core)")
	fs.DurationVar(&cfg.duration, "duration", 60*time.Second, "duration mode: run time (e.g. 30s, 60s, 300s)")
	fs.Int64Var(&cfg.workUnits, "work-units", 0, "work mode: fixed number of kernel iterations; overrides --duration")
	fs.IntVar(&cfg.memoryMiB, "memory-mib", 0, "resident memory to allocate and touch, MiB")
	fs.DurationVar(&cfg.period, "period", 100*time.Millisecond, "duty-cycle period")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.kernel != kernelSHA256 && cfg.kernel != kernelMatmul {
		return config{}, fmt.Errorf("--kernel %q: want sha256 or matmul", cfg.kernel)
	}
	if cfg.cpu <= 0 || math.IsNaN(cfg.cpu) || math.IsInf(cfg.cpu, 0) {
		return config{}, errors.New("--cpu must be > 0")
	}
	if cfg.workUnits < 0 || cfg.memoryMiB < 0 {
		return config{}, errors.New("--work-units and --memory-mib must be >= 0")
	}
	if cfg.workUnits == 0 && cfg.duration <= 0 {
		return config{}, errors.New("--duration must be > 0")
	}
	if cfg.period < 10*time.Millisecond {
		return config{}, errors.New("--period must be >= 10ms")
	}
	cfg.mode = "duration"
	if cfg.workUnits > 0 {
		cfg.mode = "work"
	}
	return cfg, nil
}

func run(ctx context.Context, cfg config) Result {
	workers := int(math.Ceil(cfg.cpu))
	if runtime.GOMAXPROCS(0) < workers {
		runtime.GOMAXPROCS(workers)
	}
	ballast := touchMemory(cfg.memoryMiB)

	ru0 := rusage()
	start := time.Now()
	var units atomic.Int64
	var wg sync.WaitGroup
	completed := true

	switch cfg.mode {
	case "work":
		var remaining atomic.Int64
		remaining.Store(cfg.workUnits)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				k := newKernel(cfg.kernel, seed)
				for ctx.Err() == nil && remaining.Add(-1) >= 0 {
					k.step()
					units.Add(1)
				}
			}(w)
		}
	default:
		deadline := start.Add(cfg.duration)
		share := cfg.cpu / float64(workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(seed int) {
				defer wg.Done()
				units.Add(dutyCycle(ctx, newKernel(cfg.kernel, seed), deadline, cfg.period, share))
			}(w)
		}
	}
	wg.Wait()
	end := time.Now()
	if ctx.Err() != nil {
		completed = false
	}
	ru1 := rusage()
	runtime.KeepAlive(ballast)

	elapsed := end.Sub(start).Seconds()
	user := ru1.user - ru0.user
	sys := ru1.sys - ru0.sys
	res := Result{
		TaskID:          cfg.taskID,
		Mode:            cfg.mode,
		Kernel:          cfg.kernel,
		CPUCores:        cfg.cpu,
		Workers:         workers,
		MemoryMiB:       cfg.memoryMiB,
		StartedAt:       start.UTC().Format(time.RFC3339Nano),
		FinishedAt:      end.UTC().Format(time.RFC3339Nano),
		ActualDurationS: round3(elapsed),
		WorkUnits:       units.Load(),
		CPUUserS:        round3(user),
		CPUSysS:         round3(sys),
		Status:          statusCompleted,
	}
	if cfg.mode == "duration" {
		res.RequestedDurationS = cfg.duration.Seconds()
	}
	if elapsed > 0 {
		res.CPUUtilization = round3((user + sys) / (elapsed * cfg.cpu))
	}
	if !completed {
		res.Status = statusTerminated
	}
	return res
}

// dutyCycle keeps one goroutine busy for share*period of every period until
// deadline. The last period is truncated so the task ends at deadline.
func dutyCycle(ctx context.Context, k kernel, deadline time.Time, period time.Duration, share float64) int64 {
	busy := time.Duration(float64(period) * share)
	var n int64
	for {
		periodStart := time.Now()
		if !periodStart.Before(deadline) || ctx.Err() != nil {
			return n
		}
		busyEnd := periodStart.Add(busy)
		if busyEnd.After(deadline) {
			busyEnd = deadline
		}
		for time.Now().Before(busyEnd) {
			k.step()
			n++
		}
		periodEnd := periodStart.Add(period)
		if periodEnd.After(deadline) {
			periodEnd = deadline
		}
		if d := time.Until(periodEnd); d > 0 {
			select {
			case <-ctx.Done():
				return n
			case <-time.After(d):
			}
		}
	}
}

// kernel is one unit of CPU work, a few microseconds long, so the duty cycle
// can check the clock between steps.
type kernel interface{ step() }

func newKernel(name string, seed int) kernel {
	if name == kernelMatmul {
		return newMatmul(seed)
	}
	return newHasher(seed)
}

type hasher struct {
	buf [1024]byte
}

func newHasher(seed int) *hasher {
	h := &hasher{}
	for i := range h.buf {
		h.buf[i] = byte(i*31 + seed)
	}
	return h
}

func (h *hasher) step() {
	sum := sha256.Sum256(h.buf[:])
	copy(h.buf[:len(sum)], sum[:])
}

const matN = 24

type matmul struct {
	a, b, c [matN * matN]float64
}

func newMatmul(seed int) *matmul {
	m := &matmul{}
	for i := range m.a {
		m.a[i] = float64((i+seed)%7) * 0.5
		m.b[i] = float64((i*3+seed)%5) * 0.25
	}
	return m
}

func (m *matmul) step() {
	for i := 0; i < matN; i++ {
		for j := 0; j < matN; j++ {
			var s float64
			for k := 0; k < matN; k++ {
				s += m.a[i*matN+k] * m.b[k*matN+j]
			}
			m.c[i*matN+j] = s
		}
	}
	m.a, m.c = m.c, m.a
	// Keep values bounded so the kernel never reaches Inf/denormals.
	for i := range m.a {
		m.a[i] = math.Mod(m.a[i], 8)
	}
}

func touchMemory(mib int) []byte {
	if mib <= 0 {
		return nil
	}
	b := make([]byte, mib<<20)
	for i := 0; i < len(b); i += 4096 {
		b[i] = byte(i)
	}
	return b
}

type cpuTimes struct{ user, sys float64 }

func rusage() cpuTimes {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return cpuTimes{}
	}
	return cpuTimes{user: tv(ru.Utime), sys: tv(ru.Stime)}
}

func tv(t syscall.Timeval) float64 {
	return float64(t.Sec) + float64(t.Usec)/1e6
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
