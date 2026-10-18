//go:build e2e

// Package e2e_test runs scheduling scenarios against a live kind cluster
// through client-go (T8.2, T8.3). Prerequisites:
//
//	make kind-up kind-load
//	kubectl apply -k tests/e2e/manifests
//	go test -v -tags=e2e -timeout 40m ./tests/e2e/...
//
// The kubeconfig comes from $KUBECONFIG or ~/.kube/config.
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	stackNamespace   = "carbon-aware"
	carbonScheduler  = "carbon-aware-scheduler"
	defaultScheduler = "default-scheduler"
	cdsName          = "carbon-data-service"
	cdsConfigMap     = "carbon-data-service"
	schedConfigMap   = "carbon-aware-scheduler-config"

	baselineDataset = "/data/e2e/e2e_baseline.json"
	spikeDataset    = "/data/e2e/e2e_spike.json"

	e2eLabel = "carbon-aware.io/e2e"
)

var (
	cs   kubernetes.Interface
	args v1alpha1.CarbonAwarePluginArgs
	// zoneOf maps worker node name → carbon-aware.io/zone.
	zoneOf map[string]string
)

func TestMain(m *testing.M) {
	if err := setup(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func setup() error {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("kubeconfig %s: %w", kubeconfig, err)
	}
	cfg.QPS, cfg.Burst = 50, 100
	cs, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, d := range []string{carbonScheduler, cdsName} {
		if err := waitDeploymentReady(ctx, d, 1); err != nil {
			return fmt.Errorf("stack not deployed (kubectl apply -k tests/e2e/manifests): %w", err)
		}
	}
	args, err = readPluginArgs(ctx, carbonScheduler)
	if err != nil {
		return err
	}
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: v1alpha1.LabelZone})
	if err != nil {
		return err
	}
	zoneOf = make(map[string]string, len(nodes.Items))
	for _, n := range nodes.Items {
		zoneOf[n.Name] = n.Labels[v1alpha1.LabelZone]
	}
	if len(zoneOf) < 2 {
		return fmt.Errorf("want ≥2 zone-labelled workers, got %v (run make kind-up)", zoneOf)
	}
	fmt.Printf("e2e: zones=%v refresh=%ds staleWarn=%ds maxDataAge=%ds httpTimeout=%ds\n",
		zoneOf, args.RefreshIntervalSeconds, args.StaleWarnSeconds, args.MaxDataAgeSeconds, args.HTTPTimeoutSeconds)
	return nil
}

// readPluginArgs extracts CarbonAware args of one profile from the ConfigMap
// the running scheduler was started with, so waits follow the deployed values.
func readPluginArgs(ctx context.Context, profile string) (v1alpha1.CarbonAwarePluginArgs, error) {
	cm, err := cs.CoreV1().ConfigMaps(stackNamespace).Get(ctx, schedConfigMap, metav1.GetOptions{})
	if err != nil {
		return v1alpha1.CarbonAwarePluginArgs{}, err
	}
	var cfg struct {
		Profiles []struct {
			SchedulerName string `json:"schedulerName"`
			PluginConfig  []struct {
				Name string                         `json:"name"`
				Args v1alpha1.CarbonAwarePluginArgs `json:"args"`
			} `json:"pluginConfig"`
		} `json:"profiles"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data["scheduler-config.yaml"]), &cfg); err != nil {
		return v1alpha1.CarbonAwarePluginArgs{}, err
	}
	for _, p := range cfg.Profiles {
		if p.SchedulerName != profile {
			continue
		}
		for _, pc := range p.PluginConfig {
			if pc.Name == v1alpha1.CarbonAwarePluginName {
				return pc.Args, pc.Args.Validate()
			}
		}
	}
	return v1alpha1.CarbonAwarePluginArgs{}, fmt.Errorf("profile %s has no CarbonAware args", profile)
}

func seconds(n int64) time.Duration { return time.Duration(n) * time.Second }

// ---------- namespaces and pods ----------

// testNamespace creates an isolated namespace that is deleted (and waited for)
// when the test ends, so every scenario starts with an empty cluster.
func testNamespace(t *testing.T, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("e2e-%s-%s", prefix, rand.String(5))
	_, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			e2eLabel:                             "true",
			"pod-security.kubernetes.io/enforce": "restricted",
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if t.Failed() {
			dumpNamespace(ctx, t, name)
		}
		bg := metav1.DeletePropagationForeground
		_ = cs.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{
			GracePeriodSeconds: ptr.To[int64](0), PropagationPolicy: &bg,
		})
		err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := cs.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
			return apierrors.IsNotFound(err), nil
		})
		if err != nil {
			t.Logf("namespace %s not gone: %v", name, err)
		}
	})
	return name
}

type podOpts struct {
	scheduler string
	enabled   *bool // nil: no carbon-aware.io/enabled label
	cpu       string
	nodeName  string
}

func carbonOpts() podOpts {
	return podOpts{scheduler: carbonScheduler, enabled: ptr.To(true), cpu: "100m"}
}

func newPod(ns, name string, o podOpts) *corev1.Pod {
	if o.cpu == "" {
		o.cpu = "100m"
	}
	labels := map[string]string{e2eLabel: "true", "app.kubernetes.io/part-of": "carbon-aware-k8s-e2e"}
	annotations := map[string]string{}
	if o.enabled != nil {
		labels[v1alpha1.LabelEnabled] = map[bool]string{true: v1alpha1.EnabledTrue, false: v1alpha1.EnabledFalse}[*o.enabled]
		labels[v1alpha1.LabelWorkloadClass] = string(v1alpha1.WorkloadClassBatch)
		annotations[v1alpha1.AnnotationExpectedDuration] = "300s"
		annotations[v1alpha1.AnnotationSLADeadline] = "1800s"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: annotations},
		Spec: corev1.PodSpec{
			SchedulerName:                 o.scheduler,
			NodeName:                      o.nodeName,
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: ptr.To[int64](0),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To[int64](65532),
				RunAsGroup:     ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "pause",
				Image:           "registry.k8s.io/pause:3.10",
				ImagePullPolicy: corev1.PullIfNotPresent,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(o.cpu),
						corev1.ResourceMemory: resource.MustParse("16Mi"),
					},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					ReadOnlyRootFilesystem:   ptr.To(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
}

func createPod(t *testing.T, p *corev1.Pod) *corev1.Pod {
	t.Helper()
	out, err := cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod %s: %v", p.Name, err)
	}
	return out
}

func createPods(t *testing.T, ns, prefix string, n int, o podOpts) []string {
	t.Helper()
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%02d", prefix, i)
		createPod(t, newPod(ns, names[i], o))
	}
	return names
}

// waitRunning waits until every named pod is bound and Running and returns them.
// A pod still Pending at the deadline fails the test (FF-INV-01: no carbon
// failure may leave a resource-feasible pod Pending).
func waitRunning(t *testing.T, ns string, names []string, timeout time.Duration) map[string]*corev1.Pod {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out := make(map[string]*corev1.Pod, len(names))
	err := wait.PollUntilContextCancel(ctx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		list, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, nil
		}
		byName := make(map[string]*corev1.Pod, len(list.Items))
		for i := range list.Items {
			byName[list.Items[i].Name] = &list.Items[i]
		}
		for _, n := range names {
			p, ok := byName[n]
			if !ok || p.Spec.NodeName == "" || p.Status.Phase != corev1.PodRunning {
				return false, nil
			}
			out[n] = p
		}
		return true, nil
	})
	if err != nil {
		list, _ := cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{})
		var stuck []string
		for _, p := range list.Items {
			if p.Status.Phase != corev1.PodRunning {
				stuck = append(stuck, fmt.Sprintf("%s(%s node=%q)", p.Name, p.Status.Phase, p.Spec.NodeName))
			}
		}
		t.Fatalf("pods not Running within %v: %v", timeout, stuck)
	}
	return out
}

func placement(pods map[string]*corev1.Pod) map[string]int {
	byZone := map[string]int{}
	for _, p := range pods {
		byZone[zoneOf[p.Spec.NodeName]]++
	}
	return byZone
}

func scheduledEvent(ctx context.Context, ns, pod string) (*corev1.Event, error) {
	list, err := cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: fields.AndSelectors(
		fields.OneTermEqualSelector("involvedObject.name", pod),
		fields.OneTermEqualSelector("reason", "Scheduled"),
	).String()})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no Scheduled event for %s/%s", ns, pod)
	}
	return &list.Items[0], nil
}

func eventReporter(e *corev1.Event) string {
	if e.ReportingController != "" {
		return e.ReportingController
	}
	return e.Source.Component
}

func dumpNamespace(ctx context.Context, t *testing.T, ns string) {
	pods, _ := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	for _, p := range pods.Items {
		t.Logf("dump %s phase=%s node=%s", p.Name, p.Status.Phase, p.Spec.NodeName)
	}
	evs, _ := cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	for _, e := range evs.Items {
		t.Logf("event %s %s %s: %s", e.InvolvedObject.Name, e.Reason, eventReporter(&e), e.Message)
	}
}

// ---------- stack control ----------

func waitDeploymentReady(ctx context.Context, name string, replicas int32) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		d, err := cs.AppsV1().Deployments(stackNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return deploymentSettled(d, replicas), nil
	})
}

func deploymentSettled(d *appsv1.Deployment, replicas int32) bool {
	s := d.Status
	return d.Generation == s.ObservedGeneration && s.Replicas == replicas &&
		s.UpdatedReplicas == replicas && s.ReadyReplicas == replicas && s.AvailableReplicas == replicas
}

func patchCDSReplicas(t *testing.T, replicas int32) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	if _, err := cs.AppsV1().Deployments(stackNamespace).Patch(context.Background(), cdsName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("scale CDS: %v", err)
	}
}

// scaleCDS sets CDS replicas and waits until the change has fully settled.
func scaleCDS(t *testing.T, replicas int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	patchCDSReplicas(t, replicas)
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		d, err := cs.AppsV1().Deployments(stackNamespace).Get(ctx, cdsName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if replicas == 0 {
			pods, err := cs.CoreV1().Pods(stackNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + cdsName})
			return err == nil && len(pods.Items) == 0 && d.Status.Replicas == 0, nil
		}
		return deploymentSettled(d, replicas), nil
	})
	if err != nil {
		t.Fatalf("CDS did not reach %d replicas: %v", replicas, err)
	}
}

// restartDeployment triggers a rollout like `kubectl rollout restart` and waits for it.
func restartDeployment(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"carbon-aware.io/e2e-restarted-at":%q}}}}}`, time.Now().Format(time.RFC3339Nano))
	if _, err := cs.AppsV1().Deployments(stackNamespace).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("restart %s: %v", name, err)
	}
	if err := waitDeploymentReady(ctx, name, 1); err != nil {
		t.Fatalf("rollout %s: %v", name, err)
	}
	// Recreate strategy: wait until exactly one non-terminating pod is Ready.
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		pods, err := cs.CoreV1().Pods(stackNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + name})
		if err != nil {
			return false, nil
		}
		ready := 0
		for _, p := range pods.Items {
			if p.DeletionTimestamp != nil {
				return false, nil
			}
			if podReady(&p) {
				ready++
			}
		}
		return ready == 1 && len(pods.Items) == 1, nil
	})
	if err != nil {
		t.Fatalf("%s pod not ready after restart: %v", name, err)
	}
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// setCDSDataset points the CDS replay provider at path and restarts it.
func setCDSDataset(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	cm, err := cs.CoreV1().ConfigMaps(stackNamespace).Get(ctx, cdsConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	changed := false
	for _, line := range strings.Split(cm.Data["config.yaml"], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "replayDatasetPath:") {
			nl := "replayDatasetPath: " + path
			changed = changed || strings.TrimSpace(line) != nl
			line = nl
		}
		out = append(out, line)
	}
	if !changed {
		return
	}
	cm.Data["config.yaml"] = strings.Join(out, "\n")
	if _, err := cs.CoreV1().ConfigMaps(stackNamespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	restartDeployment(t, cdsName)
}

// freezeCDS sends SIGSTOP to the carbon-service process on its kind node
// (docker exec): the pod and its connections stay, but nothing is answered.
func freezeCDS(t *testing.T) *corev1.Pod {
	t.Helper()
	pods, err := cs.CoreV1().Pods(stackNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + cdsName})
	if err != nil || len(pods.Items) != 1 {
		t.Fatalf("want exactly one CDS pod: n=%d err=%v", len(pods.Items), err)
	}
	p := pods.Items[0]
	signalCDS(t, p.Spec.NodeName, "STOP")
	return &p
}

func thawCDS(t *testing.T, p *corev1.Pod) {
	t.Helper()
	signalCDS(t, p.Spec.NodeName, "CONT")
}

func signalCDS(t *testing.T, node, sig string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", node, "pkill", "-"+sig, "-f", "/usr/local/bin/carbon-service").CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s pkill -%s carbon-service: %v %s (chaos 2 needs the docker CLI of the kind host)", node, sig, err, out)
	}
}

func proxyGet(ctx context.Context, path string) ([]byte, error) {
	return cs.CoreV1().RESTClient().Get().AbsPath(path).DoRaw(ctx)
}

// cdsCurrentCI reads GET /api/v1/zones/{zone}/current through the API server service proxy.
func cdsCurrentCI(ctx context.Context, zone string) (float64, error) {
	raw, err := proxyGet(ctx, fmt.Sprintf("/api/v1/namespaces/%s/services/%s:8080/proxy/api/v1/zones/%s/current", stackNamespace, cdsName, zone))
	if err != nil {
		return 0, err
	}
	var rec v1alpha1.CarbonRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return 0, err
	}
	return rec.CarbonIntensityGCO2ePerKWh, nil
}

// greenestZone returns the zone with the lowest current CI served by CDS.
func greenestZone(t *testing.T) (string, map[string]float64) {
	t.Helper()
	ci := map[string]float64{}
	best, bestCI := "", 0.0
	zones := map[string]bool{}
	for _, z := range zoneOf {
		zones[z] = true
	}
	for z := range zones {
		v, err := cdsCurrentCI(context.Background(), z)
		if err != nil {
			t.Fatalf("CDS current %s: %v", z, err)
		}
		ci[z] = v
		if best == "" || v < bestCI {
			best, bestCI = z, v
		}
	}
	return best, ci
}

// ---------- scheduler metrics ----------

func schedulerPod(ctx context.Context) (string, error) {
	pods, err := cs.CoreV1().Pods(stackNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + carbonScheduler})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && podReady(&p) {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no ready %s pod", carbonScheduler)
}

type metricSet map[string]*dto.MetricFamily

// scrape reads kube-scheduler /metrics through the API server pod proxy.
func scrape(t *testing.T) metricSet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out metricSet
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		pod, err := schedulerPod(ctx)
		if err != nil {
			return false, nil
		}
		raw, err := proxyGet(ctx, fmt.Sprintf("/api/v1/namespaces/%s/pods/https:%s:10259/proxy/metrics", stackNamespace, pod))
		if err != nil {
			return false, nil
		}
		parser := expfmt.NewTextParser(model.UTF8Validation)
		mfs, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
		if err != nil {
			return false, nil
		}
		out = mfs
		return true, nil
	})
	if err != nil {
		t.Fatalf("scrape scheduler metrics: %v", err)
	}
	return out
}

// value sums samples of the first family present among names whose labels
// include every pair in match. Several names follow the Stage 7 renames.
func (m metricSet) value(names []string, match map[string]string) float64 {
	for _, n := range names {
		mf, ok := m[n]
		if !ok {
			continue
		}
		var sum float64
		for _, s := range mf.GetMetric() {
			if !labelsMatch(s, match) {
				continue
			}
			switch {
			case s.GetCounter() != nil:
				sum += s.GetCounter().GetValue()
			case s.GetGauge() != nil:
				sum += s.GetGauge().GetValue()
			case s.GetHistogram() != nil:
				sum += float64(s.GetHistogram().GetSampleCount())
			}
		}
		return sum
	}
	return 0
}

func (m metricSet) has(names []string) bool {
	for _, n := range names {
		if _, ok := m[n]; ok {
			return true
		}
	}
	return false
}

func labelsMatch(s *dto.Metric, match map[string]string) bool {
	for k, v := range match {
		found := false
		for _, lp := range s.GetLabel() {
			if lp.GetName() == k && lp.GetValue() == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

var (
	mFallback      = []string{"carbon_aware_scheduler_fallback_total", "carbon_aware_fallback_total"}
	mAttempts      = []string{"carbon_aware_scoring_attempts_total"}
	mOptOut        = []string{"carbon_aware_opt_out_total"}
	mStaleInWindow = []string{"carbon_aware_stale_in_window_total"}
	mSnapshotAge   = []string{"carbon_aware_snapshot_age_seconds"}
	mSyncSuccess   = []string{"carbon_aware_sync_duration_seconds"}
)

func snapshotAge(t *testing.T) (float64, bool) {
	m := scrape(t)
	if !m.has(mSnapshotAge) {
		return 0, false
	}
	return m.value(mSnapshotAge, nil), true
}

// waitSnapshotAge waits until the plugin snapshot age satisfies cond.
func waitSnapshotAge(t *testing.T, timeout time.Duration, what string, cond func(age float64) bool) float64 {
	t.Helper()
	var age float64
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(context.Context) (bool, error) {
		a, ok := snapshotAge(t)
		age = a
		return ok && cond(a), nil
	})
	if err != nil {
		t.Fatalf("snapshot age never became %s (last %.1fs): %v", what, age, err)
	}
	return age
}

// ensureBaseline restores the healthy stand: CDS up and reachable, baseline
// dataset, and a fresh plugin snapshot that already reflects it.
func ensureBaseline(t *testing.T) {
	t.Helper()
	scaleCDS(t, 1)
	setCDSDataset(t, baselineDataset)
	waitSchedulerSees(t, "north", 100)
}

// waitSchedulerSees waits until CDS serves zone=ci and the scheduler Worker
// has completed fresh successful ticks after that.
func waitSchedulerSees(t *testing.T, zone string, ci float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		v, err := cdsCurrentCI(ctx, zone)
		return err == nil && v == ci, nil
	})
	if err != nil {
		t.Fatalf("CDS never served %s=%v: %v", zone, ci, err)
	}
	// Two successful ticks per profile after CDS serves the value.
	base := scrape(t).value(mSyncSuccess, map[string]string{"status": "success"})
	err = wait.PollUntilContextCancel(ctx, time.Second, false, func(context.Context) (bool, error) {
		return scrape(t).value(mSyncSuccess, map[string]string{"status": "success"}) >= base+4, nil
	})
	if err != nil {
		t.Fatalf("scheduler Worker did not tick successfully: %v", err)
	}
	waitSnapshotAge(t, seconds(args.MaxDataAgeSeconds), "fresh", func(a float64) bool { return a < float64(args.StaleWarnSeconds) })
}

func schedulerLogs(t *testing.T, since time.Duration) string {
	t.Helper()
	ctx := context.Background()
	pod, err := schedulerPod(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sec := int64(since / time.Second)
	raw, err := cs.CoreV1().Pods(stackNamespace).GetLogs(pod, &corev1.PodLogOptions{SinceSeconds: &sec}).DoRaw(ctx)
	if err != nil {
		t.Fatalf("scheduler logs: %v", err)
	}
	return string(raw)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
