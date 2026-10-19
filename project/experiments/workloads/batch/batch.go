// Package batch generates reproducible packs of synthetic batch tasks for the
// Stage 10 experiments (T10.1) and renders them as Pods or Jobs.
//
// A pack is a pure function of Spec: the same Spec (and seed) always yields the
// same tasks, durations, resource requests and arrival offsets, so every
// scheduling strategy in a matrix cell sees an identical workload.
package batch

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"carbon-aware-k8s/api/v1alpha1"
)

// Labels set on every generated task so the runner can select and clean them.
const (
	LabelApp   = "app.kubernetes.io/name"
	AppName    = "carbon-workload"
	LabelRunID = "experiments.carbon-aware.io/run-id"
	LabelTask  = "experiments.carbon-aware.io/task"
	LabelClass = "experiments.carbon-aware.io/class"

	ContainerName = "task"
	DefaultImage  = "carbon-workload-runner:0.1.0"
)

// Class is one SLA / resource class of the mix.
type Class struct {
	Name string `json:"name"`
	// Duration is the requested run time T of the task, which is also the
	// expected duration T̂ the scheduler sees (carbon-aware.io/expected-duration).
	Duration time.Duration `json:"duration"`
	// CPUMilli and MemoryMiB are the Pod requests the scheduler packs against.
	CPUMilli  int64 `json:"cpu_milli"`
	MemoryMiB int64 `json:"memory_mib"`
	// DeadlineFactor sets carbon-aware.io/sla-deadline = factor * T̂ (rounded
	// up to whole seconds). Zero means no deadline annotation.
	DeadlineFactor float64 `json:"deadline_factor"`
	// WorkloadClass is the carbon-aware.io/workload-class label.
	WorkloadClass v1alpha1.WorkloadClass `json:"workload_class"`
	// Weight is the relative frequency of the class in the mix.
	Weight float64 `json:"weight"`
}

// Spec describes one pack of tasks.
type Spec struct {
	Tasks int `json:"tasks"`
	// ArrivalRate is the mean Poisson arrival rate, tasks per second. Zero
	// submits the whole pack at once.
	ArrivalRate float64 `json:"arrival_rate"`
	Seed        uint64  `json:"seed"`
	Classes     []Class `json:"classes"`
	// BurnFraction is the share of the CPU request the task actually burns
	// (1 = the full request). See experiments/README.md for why the stand
	// runs below 1.
	BurnFraction float64 `json:"burn_fraction"`
	Kernel       string  `json:"kernel"`
	// DeadlineFactor, when > 0, overrides every class's DeadlineFactor
	// (T10.4 SLA series).
	DeadlineFactor float64 `json:"deadline_factor,omitempty"`
}

// Task is one generated batch task.
type Task struct {
	Index          int           `json:"index"`
	Name           string        `json:"name"`
	Class          Class         `json:"class"`
	ArrivalOffset  time.Duration `json:"arrival_offset"`
	Duration       time.Duration `json:"duration"`
	Deadline       time.Duration `json:"deadline"`
	DeadlineFactor float64       `json:"deadline_factor"`
	BurnCores      float64       `json:"burn_cores"`
	Kernel         string        `json:"kernel"`
}

// DefaultClasses is the Stage 10 mix: two SLA classes with different run time
// and CPU/RAM requests. A 300 s class exists for calibration and manual
// packs; the matrix leaves it out to keep a run within minutes (README).
func DefaultClasses() []Class {
	return []Class{
		{Name: "short", Duration: 30 * time.Second, CPUMilli: 500, MemoryMiB: 64, DeadlineFactor: 1.5, WorkloadClass: v1alpha1.WorkloadClassBatch, Weight: 0.5},
		{Name: "medium", Duration: 60 * time.Second, CPUMilli: 1000, MemoryMiB: 128, DeadlineFactor: 1.5, WorkloadClass: v1alpha1.WorkloadClassBatch, Weight: 0.5},
	}
}

// LongClass is the 300 s class (T10.1 T ∈ {30, 60, 300} s).
func LongClass() Class {
	return Class{Name: "long", Duration: 300 * time.Second, CPUMilli: 1000, MemoryMiB: 128, DeadlineFactor: 1.5, WorkloadClass: v1alpha1.WorkloadClassFlexible, Weight: 1}
}

// Validate checks that the spec can produce a pack.
func (s Spec) Validate() error {
	if s.Tasks <= 0 {
		return errors.New("tasks must be > 0")
	}
	if s.ArrivalRate < 0 || math.IsNaN(s.ArrivalRate) || math.IsInf(s.ArrivalRate, 0) {
		return errors.New("arrival rate must be >= 0")
	}
	if len(s.Classes) == 0 {
		return errors.New("at least one class is required")
	}
	if s.BurnFraction <= 0 || s.BurnFraction > 1 {
		return errors.New("burn fraction must be in (0, 1]")
	}
	if s.DeadlineFactor != 0 && s.DeadlineFactor < 1 {
		return errors.New("deadline factor must be >= 1 (the plugin rejects T̂ > deadline)")
	}
	total := 0.0
	for _, c := range s.Classes {
		if c.Name == "" || c.Duration < time.Second || c.Duration%time.Second != 0 {
			return fmt.Errorf("class %q: duration must be a whole number of seconds >= 1s", c.Name)
		}
		if c.CPUMilli <= 0 || c.MemoryMiB <= 0 {
			return fmt.Errorf("class %q: cpu and memory requests must be > 0", c.Name)
		}
		if c.DeadlineFactor != 0 && c.DeadlineFactor < 1 {
			return fmt.Errorf("class %q: deadline factor must be >= 1", c.Name)
		}
		if !c.WorkloadClass.IsKnown() {
			return fmt.Errorf("class %q: unknown workload class %q", c.Name, c.WorkloadClass)
		}
		if c.Weight <= 0 {
			return fmt.Errorf("class %q: weight must be > 0", c.Name)
		}
		total += c.Weight
	}
	if total <= 0 {
		return errors.New("class weights sum to zero")
	}
	return nil
}

// Generate returns the pack for s. Arrival offsets are cumulative exponential
// gaps (Poisson process) starting at 0; classes are drawn by weight. Both use
// one PCG stream seeded from s.Seed, so the result is deterministic.
func Generate(s Spec) ([]Task, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewPCG(s.Seed, 0x5eed10))
	total := 0.0
	for _, c := range s.Classes {
		total += c.Weight
	}
	kernel := s.Kernel
	if kernel == "" {
		kernel = "sha256"
	}
	tasks := make([]Task, 0, s.Tasks)
	var at time.Duration
	for i := 0; i < s.Tasks; i++ {
		if i > 0 && s.ArrivalRate > 0 {
			gap := rng.ExpFloat64() / s.ArrivalRate
			at += time.Duration(gap * float64(time.Second)).Round(time.Millisecond)
		}
		c := pick(s.Classes, total, rng.Float64())
		factor := c.DeadlineFactor
		if s.DeadlineFactor > 0 {
			factor = s.DeadlineFactor
		}
		var deadline time.Duration
		if factor > 0 {
			sec := math.Ceil(c.Duration.Seconds()*factor - 1e-9)
			deadline = time.Duration(sec) * time.Second
		}
		tasks = append(tasks, Task{
			Index:          i,
			Name:           fmt.Sprintf("task-%03d", i),
			Class:          c,
			ArrivalOffset:  at,
			Duration:       c.Duration,
			Deadline:       deadline,
			DeadlineFactor: factor,
			BurnCores:      float64(c.CPUMilli) / 1000 * s.BurnFraction,
			Kernel:         kernel,
		})
	}
	return tasks, nil
}

func pick(classes []Class, total, u float64) Class {
	x := u * total
	for _, c := range classes {
		if x < c.Weight {
			return c
		}
		x -= c.Weight
	}
	return classes[len(classes)-1]
}

// PodOptions are the per-run settings applied when rendering a task.
type PodOptions struct {
	Namespace     string
	RunID         string
	SchedulerName string
	Image         string
	// CarbonWeight, when set, adds carbon-aware.io/carbon-weight (T10.4).
	CarbonWeight *float64
	// NamePrefix is prepended to the task name (Pods of one run share a namespace).
	NamePrefix string
}

// Pod renders t as a bare Pod with restartPolicy Never.
func (t Task) Pod(o PodOptions) *v1.Pod {
	meta, spec := t.template(o)
	return &v1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: meta,
		Spec:       spec,
	}
}

// Job renders t as a batch/v1 Job (backoffLimit 0) for kubectl-driven packs.
func (t Task) Job(o PodOptions) *batchv1.Job {
	meta, spec := t.template(o)
	backoff := int32(0)
	ttl := int32(600)
	podMeta := metav1.ObjectMeta{Labels: meta.Labels, Annotations: meta.Annotations}
	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      meta.Name,
			Namespace: meta.Namespace,
			Labels:    meta.Labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template:                v1.PodTemplateSpec{ObjectMeta: podMeta, Spec: spec},
		},
	}
}

func (t Task) template(o PodOptions) (metav1.ObjectMeta, v1.PodSpec) {
	image := o.Image
	if image == "" {
		image = DefaultImage
	}
	labels := map[string]string{
		LabelApp:                    AppName,
		LabelTask:                   t.Name,
		LabelClass:                  t.Class.Name,
		v1alpha1.LabelEnabled:       v1alpha1.EnabledTrue,
		v1alpha1.LabelWorkloadClass: string(t.Class.WorkloadClass),
	}
	if o.RunID != "" {
		labels[LabelRunID] = o.RunID
	}
	ann := map[string]string{
		v1alpha1.AnnotationExpectedDuration: v1alpha1.FormatDurationSeconds(int64(t.Duration / time.Second)),
	}
	if t.Deadline > 0 {
		ann[v1alpha1.AnnotationSLADeadline] = v1alpha1.FormatDurationSeconds(int64(t.Deadline / time.Second))
	}
	if o.CarbonWeight != nil {
		ann[v1alpha1.AnnotationCarbonWeight] = strconv.FormatFloat(*o.CarbonWeight, 'f', -1, 64)
	}
	cpu := resource.NewMilliQuantity(t.Class.CPUMilli, resource.DecimalSI)
	mem := resource.NewQuantity(t.Class.MemoryMiB<<20, resource.BinarySI)
	// The task touches half its memory request; the limit is the request.
	touch := t.Class.MemoryMiB / 2
	noEscalation := false
	nonRoot := true
	uid := int64(65532)
	grace := int64(5)
	spec := v1.PodSpec{
		SchedulerName:                 o.SchedulerName,
		RestartPolicy:                 v1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &grace,
		AutomountServiceAccountToken:  &noEscalation,
		EnableServiceLinks:            &noEscalation,
		SecurityContext: &v1.PodSecurityContext{
			RunAsNonRoot:   &nonRoot,
			RunAsUser:      &uid,
			SeccompProfile: &v1.SeccompProfile{Type: v1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []v1.Container{{
			Name:            ContainerName,
			Image:           image,
			ImagePullPolicy: v1.PullIfNotPresent,
			Args: []string{
				"--task-id=" + strings.TrimPrefix(o.NamePrefix+t.Name, "-"),
				"--duration=" + t.Duration.String(),
				"--cpu=" + strconv.FormatFloat(t.BurnCores, 'f', -1, 64),
				"--memory-mib=" + strconv.FormatInt(touch, 10),
				"--kernel=" + t.Kernel,
			},
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{v1.ResourceCPU: *cpu, v1.ResourceMemory: *mem},
				Limits:   v1.ResourceList{v1.ResourceMemory: *mem},
			},
			SecurityContext: &v1.SecurityContext{
				AllowPrivilegeEscalation: &noEscalation,
				ReadOnlyRootFilesystem:   &nonRoot,
				Capabilities:             &v1.Capabilities{Drop: []v1.Capability{"ALL"}},
			},
		}},
	}
	meta := metav1.ObjectMeta{
		Name:        o.NamePrefix + t.Name,
		Namespace:   o.Namespace,
		Labels:      labels,
		Annotations: ann,
	}
	return meta, spec
}
