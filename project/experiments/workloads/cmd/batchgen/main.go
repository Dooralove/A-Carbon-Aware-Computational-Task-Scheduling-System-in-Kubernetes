// Command batchgen renders a reproducible task pack (experiments/workloads/batch)
// as a multi-document YAML of Jobs or Pods for kubectl (T10.1):
//
//	go run ./experiments/workloads/cmd/batchgen --tasks=50 --seed=1001 \
//	  --scheduler=carbon-aware-scheduler > pack.yaml
//	kubectl apply -f pack.yaml
//
// kubectl creates everything at once; Poisson arrival offsets are written as
// the annotation experiments.carbon-aware.io/arrival-offset for reference.
// The experiment runner submits the same pack with the real arrival times.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"sigs.k8s.io/yaml"

	"carbon-aware-k8s/experiments/workloads/batch"
)

const annArrival = "experiments.carbon-aware.io/arrival-offset"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run() error {
	var (
		tasks     = flag.Int("tasks", 50, "tasks in the pack")
		rate      = flag.Float64("arrival-rate", 0.33, "Poisson arrival rate, tasks/s (0 = all at once)")
		seed      = flag.Uint64("seed", 1001, "pack seed")
		burn      = flag.Float64("burn-fraction", 0.1, "share of the CPU request the task burns")
		deadline  = flag.Float64("deadline-factor", 0, "override SLA factor k (deadline = k*T̂); 0 keeps per-class 1.5")
		long      = flag.Bool("with-long", false, "add the 300 s class to the mix")
		kind      = flag.String("kind", "job", "job | pod")
		ns        = flag.String("namespace", "carbon-exp", "namespace")
		scheduler = flag.String("scheduler", "carbon-aware-scheduler", "spec.schedulerName")
		image     = flag.String("image", batch.DefaultImage, "workload image")
		runID     = flag.String("run-id", "manual", "run-id label")
		weight    = flag.String("carbon-weight", "", "optional carbon-aware.io/carbon-weight")
	)
	flag.Parse()

	classes := batch.DefaultClasses()
	if *long {
		c := batch.LongClass()
		c.Weight = 0.2
		classes = append(classes, c)
	}
	pack, err := batch.Generate(batch.Spec{
		Tasks: *tasks, ArrivalRate: *rate, Seed: *seed, Classes: classes,
		BurnFraction: *burn, DeadlineFactor: *deadline,
	})
	if err != nil {
		return err
	}
	opts := batch.PodOptions{Namespace: *ns, RunID: *runID, SchedulerName: *scheduler, Image: *image}
	if *weight != "" {
		w, err := strconv.ParseFloat(*weight, 64)
		if err != nil {
			return fmt.Errorf("--carbon-weight: %w", err)
		}
		opts.CarbonWeight = &w
	}
	for _, t := range pack {
		var obj any
		switch *kind {
		case "job":
			j := t.Job(opts)
			j.Annotations = map[string]string{annArrival: t.ArrivalOffset.Round(time.Millisecond).String()}
			obj = j
		case "pod":
			p := t.Pod(opts)
			p.Annotations[annArrival] = t.ArrivalOffset.Round(time.Millisecond).String()
			obj = p
		default:
			return fmt.Errorf("--kind %q: want job or pod", *kind)
		}
		b, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		fmt.Printf("---\n%s", b)
	}
	return nil
}
