package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	cdsNamespace  = "carbon-aware"
	cdsName       = "carbon-data-service"
	cdsConfigKey  = "config.yaml"
	restartedAtAn = "experiments.carbon-aware.io/restarted-at"
)

type cluster struct {
	cs        kubernetes.Interface
	namespace string
	cdsURL    string
	http      *http.Client
}

// cleanNamespace deletes every Pod in the task namespace and waits until the
// namespace is empty (T10.2 step 1).
func (c *cluster) cleanNamespace(ctx context.Context) error {
	zero := int64(0)
	err := c.cs.CoreV1().Pods(c.namespace).DeleteCollection(ctx,
		metav1.DeleteOptions{GracePeriodSeconds: &zero}, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pods: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		l, err := c.cs.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		return len(l.Items) == 0, nil
	})
}

var (
	reDataset    = regexp.MustCompile(`(?m)^(\s*replayDatasetPath:).*$`)
	reMultiplier = regexp.MustCompile(`(?m)^(\s*replayTimeMultiplier:).*$`)
)

// setReplay rewrites the CDS ConfigMap for the scenario and restarts the CDS
// Deployment, so the replay clock starts at the dataset's modelStart (T10.2
// step 2, REPRO-03). It returns the new Pod's name and the time it became
// ready.
func (c *cluster) setReplay(ctx context.Context, sc Scenario) (string, time.Time, error) {
	cms := c.cs.CoreV1().ConfigMaps(cdsNamespace)
	cm, err := cms.Get(ctx, cdsName, metav1.GetOptions{})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("get cds configmap: %w", err)
	}
	cfg := cm.Data[cdsConfigKey]
	if !reDataset.MatchString(cfg) {
		return "", time.Time{}, fmt.Errorf("cds config has no replayDatasetPath")
	}
	cfg = reDataset.ReplaceAllString(cfg, "${1} "+sc.DatasetPath)
	mult := strconv.FormatFloat(sc.Multiplier, 'f', -1, 64)
	if reMultiplier.MatchString(cfg) {
		cfg = reMultiplier.ReplaceAllString(cfg, "${1} "+mult)
	} else {
		cfg = strings.TrimRight(cfg, "\n") + "\nreplayTimeMultiplier: " + mult + "\n"
	}
	cm.Data[cdsConfigKey] = cfg
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return "", time.Time{}, fmt.Errorf("update cds configmap: %w", err)
	}

	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, restartedAtAn, stamp)
	deps := c.cs.AppsV1().Deployments(cdsNamespace)
	if _, err := deps.Patch(ctx, cdsName, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return "", time.Time{}, fmt.Errorf("restart cds: %w", err)
	}
	var podName string
	// Recreate strategy: wait for exactly one ready Pod carrying the new stamp.
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := c.cs.CoreV1().Pods(cdsNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=carbon-data-service",
		})
		if err != nil {
			return false, nil
		}
		ready := 0
		podName = ""
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.DeletionTimestamp != nil {
				return false, nil
			}
			if p.Annotations[restartedAtAn] != stamp {
				return false, nil
			}
			if podReady(p) {
				ready++
				podName = p.Name
			}
		}
		return ready == 1, nil
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("wait for cds restart: %w", err)
	}
	return podName, time.Now().UTC(), nil
}

func podReady(p *v1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == v1.PodReady {
			return c.Status == v1.ConditionTrue
		}
	}
	return false
}

// cdsModelTime reads carbon_aware_cds_replay_model_time_seconds and the
// current CI per zone straight from the CDS /metrics (NodePort on the host).
func (c *cluster) cdsModelTime(ctx context.Context) (time.Time, map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cdsURL+v1alpha1.PathMetrics, nil)
	if err != nil {
		return time.Time{}, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return time.Time{}, nil, err
	}
	defer resp.Body.Close()
	var model time.Time
	ci := map[string]float64{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "carbon_aware_cds_replay_model_time_seconds "):
			v, err := strconv.ParseFloat(strings.TrimPrefix(line, "carbon_aware_cds_replay_model_time_seconds "), 64)
			if err == nil {
				model = time.Unix(0, int64(v*1e9)).UTC()
			}
		case strings.HasPrefix(line, `carbon_aware_zone_intensity_gco2_kwh{zone="`):
			rest := strings.TrimPrefix(line, `carbon_aware_zone_intensity_gco2_kwh{zone="`)
			zone, val, ok := strings.Cut(rest, `"} `)
			if ok {
				if v, err := strconv.ParseFloat(val, 64); err == nil {
					ci[zone] = v
				}
			}
		}
	}
	if model.IsZero() {
		return time.Time{}, nil, fmt.Errorf("cds /metrics has no replay model time")
	}
	return model, ci, sc.Err()
}

// waitModelTime blocks until the replay model time reaches target. It fails
// if model time is already past target+slack: the CDS did not restart.
func (c *cluster) waitModelTime(ctx context.Context, target time.Time, slack time.Duration) (time.Time, error) {
	var last time.Time
	err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		m, _, err := c.cdsModelTime(ctx)
		if err != nil {
			return false, nil
		}
		last = m
		return !m.Before(target), nil
	})
	if err != nil {
		return last, fmt.Errorf("replay model time %s did not reach %s: %w", last, target, err)
	}
	if last.After(target.Add(slack)) {
		return last, fmt.Errorf("replay model time %s is past the anchor %s (+%s): CDS did not restart", last, target, slack)
	}
	return last, nil
}

// nodes returns allocatable CPU (millicores) and zone per node.
func (c *cluster) nodes(ctx context.Context) (map[string]nodeInfo, error) {
	l, err := c.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]nodeInfo, len(l.Items))
	for _, n := range l.Items {
		out[n.Name] = nodeInfo{
			allocCPUMilli: n.Status.Allocatable.Cpu().MilliValue(),
			zone:          n.Labels[v1alpha1.LabelZone],
		}
	}
	return out, nil
}

type nodeInfo struct {
	allocCPUMilli int64
	zone          string
}

// podTrace is what the runner observed about one task Pod. Times are the
// runner's clock when the watch event arrived (ms resolution), which is
// finer than the 1 s resolution of Pod status timestamps.
type podTrace struct {
	name        string
	submittedAt time.Time
	createErr   error
	boundAt     time.Time
	runningAt   time.Time
	doneAt      time.Time
	node        string
	phase       v1.PodPhase
	final       *v1.Pod
}

type tracker struct {
	mu   sync.Mutex
	pods map[string]*podTrace
}

func newTracker() *tracker { return &tracker{pods: map[string]*podTrace{}} }

func (t *tracker) submitted(name string, at time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.get(name)
	p.submittedAt = at
	p.createErr = err
}

func (t *tracker) get(name string) *podTrace {
	p, ok := t.pods[name]
	if !ok {
		p = &podTrace{name: name}
		t.pods[name] = p
	}
	return p
}

func (t *tracker) observe(pod *v1.Pod) {
	now := time.Now().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.get(pod.Name)
	if pod.Spec.NodeName != "" && p.boundAt.IsZero() {
		p.boundAt = now
		p.node = pod.Spec.NodeName
	}
	if pod.Status.Phase == v1.PodRunning && p.runningAt.IsZero() {
		p.runningAt = now
	}
	if (pod.Status.Phase == v1.PodSucceeded || pod.Status.Phase == v1.PodFailed) && p.doneAt.IsZero() {
		p.doneAt = now
		p.final = pod.DeepCopy()
	}
	p.phase = pod.Status.Phase
}

// finished reports how many of the expected Pods reached a terminal phase.
func (t *tracker) finished() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, p := range t.pods {
		if !p.doneAt.IsZero() || p.createErr != nil {
			n++
		}
	}
	return n
}

func (t *tracker) snapshot() map[string]podTrace {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]podTrace, len(t.pods))
	for k, v := range t.pods {
		out[k] = *v
	}
	return out
}

// watchRun starts an informer on the run's Pods; call stop to end it.
func (c *cluster) watchRun(runLabel string, tr *tracker) (stop func(), err error) {
	factory := informers.NewSharedInformerFactoryWithOptions(c.cs, 0,
		informers.WithNamespace(c.namespace),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = runLabel }))
	inf := factory.Core().V1().Pods().Informer()
	_, err = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { observePod(tr, obj) },
		UpdateFunc: func(_, obj any) { observePod(tr, obj) },
	})
	if err != nil {
		return nil, err
	}
	ch := make(chan struct{})
	factory.Start(ch)
	if !cache.WaitForCacheSync(ch, inf.HasSynced) {
		close(ch)
		return nil, fmt.Errorf("pod informer did not sync")
	}
	return func() { close(ch); factory.Shutdown() }, nil
}

func observePod(tr *tracker, obj any) {
	if pod, ok := obj.(*v1.Pod); ok {
		tr.observe(pod)
	}
}

// taskResult is the JSON line printed by experiments/workloads/synthetic_task.go.
type taskResult struct {
	TaskID          string  `json:"task_id"`
	StartedAt       string  `json:"started_at"`
	FinishedAt      string  `json:"finished_at"`
	ActualDurationS float64 `json:"actual_duration_s"`
	CPUUserS        float64 `json:"cpu_user_s"`
	CPUSysS         float64 `json:"cpu_sys_s"`
	CPUUtilization  float64 `json:"cpu_utilization"`
	WorkUnits       int64   `json:"work_units"`
	Status          string  `json:"status"`
}

func (c *cluster) taskLog(ctx context.Context, pod string) (*taskResult, error) {
	req := c.cs.CoreV1().Pods(c.namespace).GetLogs(pod, &v1.PodLogOptions{Container: "task"})
	rc, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 1<<16))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var res taskResult
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		return nil, fmt.Errorf("parse task output %q: %w", lines[len(lines)-1], err)
	}
	return &res, nil
}
