package batch

import (
	"math"
	"reflect"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

func spec() Spec {
	return Spec{Tasks: 200, ArrivalRate: 0.5, Seed: 7, Classes: DefaultClasses(), BurnFraction: 0.1}
}

func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate(spec())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate(spec())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same spec produced different packs")
	}
	s := spec()
	s.Seed = 8
	c, _ := Generate(s)
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds produced the same pack")
	}
}

func TestGenerateArrivalsAndMix(t *testing.T) {
	tasks, err := Generate(spec())
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].ArrivalOffset != 0 {
		t.Fatalf("first arrival = %v, want 0", tasks[0].ArrivalOffset)
	}
	short := 0
	for i, task := range tasks {
		if i > 0 && task.ArrivalOffset < tasks[i-1].ArrivalOffset {
			t.Fatalf("arrivals not monotone at %d", i)
		}
		if task.Class.Name == "short" {
			short++
		}
		if task.Deadline != time.Duration(math.Ceil(task.Duration.Seconds()*1.5))*time.Second {
			t.Fatalf("task %d deadline %v for T=%v", i, task.Deadline, task.Duration)
		}
		if want := float64(task.Class.CPUMilli) / 1000 * 0.1; math.Abs(task.BurnCores-want) > 1e-12 {
			t.Fatalf("burn cores %v, want %v", task.BurnCores, want)
		}
	}
	// Mean gap 1/λ = 2 s: 199 gaps ≈ 398 s, Poisson sd ≈ 28 s.
	last := tasks[len(tasks)-1].ArrivalOffset.Seconds()
	if last < 300 || last > 500 {
		t.Fatalf("last arrival %.0f s is implausible for λ=0.5/s", last)
	}
	if short < 70 || short > 130 {
		t.Fatalf("short class drawn %d/200 times, want ≈100", short)
	}
}

func TestDeadlineFactorOverride(t *testing.T) {
	s := spec()
	s.DeadlineFactor = 1.1
	tasks, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		want := time.Duration(math.Ceil(task.Duration.Seconds()*1.1)) * time.Second
		if task.Deadline != want || task.DeadlineFactor != 1.1 {
			t.Fatalf("deadline %v factor %v, want %v / 1.1", task.Deadline, task.DeadlineFactor, want)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := []func(*Spec){
		func(s *Spec) { s.Tasks = 0 },
		func(s *Spec) { s.BurnFraction = 0 },
		func(s *Spec) { s.BurnFraction = 1.5 },
		func(s *Spec) { s.DeadlineFactor = 0.9 },
		func(s *Spec) { s.Classes = nil },
		func(s *Spec) {
			s.Classes = []Class{{Name: "x", Duration: 1500 * time.Millisecond, CPUMilli: 1, MemoryMiB: 1, WorkloadClass: "batch", Weight: 1}}
		},
		func(s *Spec) {
			s.Classes = []Class{{Name: "x", Duration: time.Second, CPUMilli: 1, MemoryMiB: 1, WorkloadClass: "nope", Weight: 1}}
		},
	}
	for i, mutate := range bad {
		s := spec()
		mutate(&s)
		if _, err := Generate(s); err == nil {
			t.Errorf("case %d: invalid spec accepted", i)
		}
	}
}

// The rendered Pod must parse as a carbon-aware WorkloadProfile exactly as
// the plugin reads it, with T̂, deadline and the optional weight.
func TestPodIsAValidWorkloadProfile(t *testing.T) {
	tasks, err := Generate(spec())
	if err != nil {
		t.Fatal(err)
	}
	w := 0.4
	pod := tasks[3].Pod(PodOptions{Namespace: "carbon-exp", RunID: "r1", SchedulerName: "carbon-aware-scheduler", CarbonWeight: &w})
	prof, err := v1alpha1.NewWorkloadProfile(pod.Namespace, pod.Name, "", pod.Labels, pod.Annotations)
	if err != nil {
		t.Fatal(err)
	}
	if !prof.Enabled || prof.ExpectedDurationSeconds == nil || *prof.ExpectedDurationSeconds != int64(tasks[3].Duration/time.Second) {
		t.Fatalf("profile %+v does not carry T̂", prof)
	}
	if prof.SLADeadlineSeconds == nil || *prof.SLADeadlineSeconds != int64(tasks[3].Deadline/time.Second) {
		t.Fatalf("deadline %v, want %v", prof.SLADeadlineSeconds, tasks[3].Deadline)
	}
	if prof.CarbonWeight == nil || *prof.CarbonWeight != 0.4 {
		t.Fatalf("carbon weight %v", prof.CarbonWeight)
	}
	if pod.Spec.SchedulerName != "carbon-aware-scheduler" || pod.Labels[LabelRunID] != "r1" {
		t.Fatal("scheduler name or run label missing")
	}
	req := pod.Spec.Containers[0].Resources.Requests
	if req.Cpu().MilliValue() != tasks[3].Class.CPUMilli || req.Memory().Value() != tasks[3].Class.MemoryMiB<<20 {
		t.Fatalf("requests %v", req)
	}
	job := tasks[3].Job(PodOptions{Namespace: "carbon-exp", SchedulerName: "default-scheduler"})
	if job.Spec.Template.Spec.SchedulerName != "default-scheduler" || *job.Spec.BackoffLimit != 0 {
		t.Fatal("job template not rendered")
	}
}
