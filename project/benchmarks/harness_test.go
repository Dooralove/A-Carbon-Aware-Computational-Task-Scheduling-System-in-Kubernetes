// Package benchmarks_test measures the scheduling overhead of the CarbonAware
// plugin (T9.1) and feeds the scalability audit (T9.2, Gate 9).
//
// The harness runs the real kube-scheduler (k8s.io/kubernetes/pkg/scheduler)
// in-process with the production profiles from
// deployments/kubernetes/scheduler-config.yaml, a fake clientset as the API
// server, and the real Carbon Data Service (cache + REST) on loopback. The
// plugin therefore gets its snapshot through the same Cache Sync Worker as in
// the cluster; only etcd/apiserver network latency is absent.
package benchmarks_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/events"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	configv1 "k8s.io/kube-scheduler/config/v1"
	fwk "k8s.io/kube-scheduler/framework"
	k8sscheduler "k8s.io/kubernetes/pkg/scheduler"
	schedconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/scheme"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/profile"
	"sigs.k8s.io/yaml"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/api"
	"carbon-aware-k8s/pkg/carbon/cache"
	carbonaware "carbon-aware-k8s/pkg/scheduler"
)

// Profile names as the cluster sees them. default-scheduler is the upstream
// default profile (strategy A); carbon-aware-scheduler and carbon-only-scheduler
// come verbatim from the production ConfigMap (strategies C and B).
const (
	profileDefault     = "default-scheduler"
	profileCarbonAware = "carbon-aware-scheduler"
	profileCarbonOnly  = "carbon-only-scheduler"
)

var allProfiles = []string{profileDefault, profileCarbonAware, profileCarbonOnly}

// nodeCounts is the T9.1 cluster-size grid.
var nodeCounts = []int{3, 5, 10, 20, 50}

// zones and their intensities match the Mock provider and the kind stand
// (north < central < west).
var zoneCI = map[string]float64{"north": 80, "central": 240, "west": 400}

var zoneOrder = []string{"north", "central", "west"}

const schedulerConfigMap = "../deployments/kubernetes/scheduler-config.yaml"

// ---------------------------------------------------------------------------
// Carbon Data Service on loopback.
// ---------------------------------------------------------------------------

// forecastProvider is a fixed-intensity provider that also returns a 12-step
// 5-minute forecast, so PreScore runs the full piecewise integral (2.10s)
// instead of the persistence shortcut. That is the expensive scoring path.
type forecastProvider struct {
	ci map[v1alpha1.ZoneID]float64
}

func newForecastProvider() *forecastProvider {
	p := &forecastProvider{ci: make(map[v1alpha1.ZoneID]float64, len(zoneCI))}
	for z, v := range zoneCI {
		p.ci[v1alpha1.ZoneID(z)] = v
	}
	return p
}

func (p *forecastProvider) Name() string                { return "bench-forecast" }
func (p *forecastProvider) Mode() v1alpha1.ProviderMode { return v1alpha1.ProviderModeMock }

func (p *forecastProvider) ListZones(context.Context) ([]v1alpha1.ZoneProfile, error) {
	out := make([]v1alpha1.ZoneProfile, 0, len(p.ci))
	for _, z := range zoneOrder {
		out = append(out, v1alpha1.ZoneProfile{
			APIVersion:          v1alpha1.GroupVersion,
			Kind:                v1alpha1.KindZoneProfile,
			Zone:                v1alpha1.ZoneID(z),
			Quality:             v1alpha1.QualityFresh,
			Source:              v1alpha1.SourceMock,
			TemporalGranularity: v1alpha1.Granularity5Minutes,
			IntervalSeconds:     300,
			HasCurrent:          true,
			HasForecast:         true,
		})
	}
	return out, nil
}

func (p *forecastProvider) Current(_ context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	ci, ok := p.ci[zone]
	if !ok {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("zone %s: %w", zone, v1alpha1.ErrUnknownZone)
	}
	at = at.UTC().Truncate(time.Second)
	flow := true
	rec := v1alpha1.CarbonRecord{
		APIVersion:                 v1alpha1.GroupVersion,
		Kind:                       v1alpha1.KindCarbonRecord,
		Zone:                       zone,
		Timestamp:                  v1alpha1.RFC3339Time{Time: at},
		CarbonIntensityGCO2ePerKWh: ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		TemporalGranularity:        v1alpha1.Granularity5Minutes,
		IntervalSeconds:            300,
		Quality:                    v1alpha1.QualityFresh,
		FlowTraced:                 &flow,
		FetchedAt:                  v1alpha1.RFC3339Time{Time: at},
		ExpiresAt:                  v1alpha1.RFC3339Time{Time: at.Add(5 * time.Minute)},
	}
	return rec, rec.Validate()
}

// Forecast returns 12 five-minute intervals with a ±10 % saw-tooth around the
// current value, so every interval is a separate breakpoint in the integral.
func (p *forecastProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, _ time.Duration) ([]v1alpha1.ForecastInterval, error) {
	rec, err := p.Current(ctx, zone, at)
	if err != nil {
		return nil, err
	}
	start := rec.Timestamp.Time
	out := make([]v1alpha1.ForecastInterval, 0, 12)
	for k := 0; k < 12; k++ {
		ts := start.Add(time.Duration(k) * 5 * time.Minute)
		factor := 0.9 + 0.2*float64(k%3)/2
		out = append(out, v1alpha1.ForecastInterval{
			Timestamp:                  v1alpha1.RFC3339Time{Time: ts},
			ExpiresAt:                  v1alpha1.RFC3339Time{Time: ts.Add(5 * time.Minute)},
			DurationSeconds:            300,
			HorizonSeconds:             int64(k) * 300,
			CarbonIntensityGCO2ePerKWh: rec.CarbonIntensityGCO2ePerKWh * factor,
			Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		})
	}
	return out, nil
}

var _ v1alpha1.CarbonProvider = (*forecastProvider)(nil)

// startCDS runs the real CDS cache refresh loop and REST handlers.
func startCDS(tb testing.TB) string {
	tb.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	prov := newForecastProvider()
	c := cache.NewWithOptions(cache.Options{
		Provider:        prov,
		Logger:          logger,
		RefreshInterval: 30 * time.Second,
		RefreshTimeout:  time.Second,
	})
	if err := c.Refresh(context.Background()); err != nil {
		tb.Fatalf("initial CDS refresh: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Loop(ctx)
	}()
	srv := httptest.NewServer(api.New("127.0.0.1:0", c, prov, logger).Handler())
	tb.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
	})
	return srv.URL
}

// ---------------------------------------------------------------------------
// Scheduler profiles from the production ConfigMap.
// ---------------------------------------------------------------------------

// loadProfiles decodes the KubeSchedulerConfiguration embedded in the
// production ConfigMap the same way kube-scheduler --config does, points the
// plugin at cdsURL, and prepends the upstream default profile.
func loadProfiles(tb testing.TB, cdsURL string) []schedconfig.KubeSchedulerProfile {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Clean(schedulerConfigMap))
	if err != nil {
		tb.Fatalf("read %s: %v", schedulerConfigMap, err)
	}
	var cm v1.ConfigMap
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		tb.Fatalf("parse ConfigMap: %v", err)
	}
	data, ok := cm.Data["scheduler-config.yaml"]
	if !ok {
		tb.Fatal("ConfigMap has no scheduler-config.yaml")
	}
	data = strings.ReplaceAll(data, "http://carbon-data-service.carbon-aware.svc:8080", cdsURL)

	obj, _, err := scheme.Codecs.UniversalDecoder().Decode([]byte(data), nil, nil)
	if err != nil {
		tb.Fatalf("decode KubeSchedulerConfiguration: %v", err)
	}
	cfg, ok := obj.(*schedconfig.KubeSchedulerConfiguration)
	if !ok {
		tb.Fatalf("decoded %T, want KubeSchedulerConfiguration", obj)
	}

	var versioned configv1.KubeSchedulerConfiguration
	scheme.Scheme.Default(&versioned)
	def := schedconfig.KubeSchedulerConfiguration{}
	if err := scheme.Scheme.Convert(&versioned, &def, nil); err != nil {
		tb.Fatalf("default config: %v", err)
	}
	if len(def.Profiles) != 1 || def.Profiles[0].SchedulerName != profileDefault {
		tb.Fatalf("unexpected default profiles: %+v", def.Profiles)
	}
	return append([]schedconfig.KubeSchedulerProfile{def.Profiles[0]}, cfg.Profiles...)
}

// ---------------------------------------------------------------------------
// Cluster objects.
// ---------------------------------------------------------------------------

const (
	nodeCPU    = "16"
	nodeMemory = "64Gi"
	nodePods   = "250"
)

func benchNode(i int) *v1.Node {
	zone := zoneOrder[i%len(zoneOrder)]
	name := fmt.Sprintf("worker-%03d", i)
	alloc := v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse(nodeCPU),
		v1.ResourceMemory: resource.MustParse(nodeMemory),
		v1.ResourcePods:   resource.MustParse(nodePods),
	}
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID("node-" + name),
			Labels: map[string]string{
				"kubernetes.io/hostname":      name,
				"kubernetes.io/os":            "linux",
				"topology.kubernetes.io/zone": zone,
				v1alpha1.LabelZone:            zone,
				v1alpha1.LabelPowerProfile:    "standard",
			},
		},
		Status: v1.NodeStatus{
			Capacity:    alloc,
			Allocatable: alloc,
			Conditions:  []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionTrue}},
		},
	}
}

// residentPod is already bound, so resource plugins (and the plugin's u⁻, u⁺)
// see a non-empty node. Requests vary per node to break ties.
func residentPod(node *v1.Node, i int) *v1.Pod {
	cpu := int64(500 + 250*(i%5))
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("resident-%s", node.Name),
			Namespace: "default",
			UID:       types.UID("resident-" + node.Name),
		},
		Spec: v1.PodSpec{
			NodeName:      node.Name,
			SchedulerName: profileDefault,
			Containers: []v1.Container{{
				Name:  "c",
				Image: "registry.k8s.io/pause:3.10",
				Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
					v1.ResourceCPU:    *resource.NewMilliQuantity(cpu, resource.DecimalSI),
					v1.ResourceMemory: resource.MustParse("1Gi"),
				}},
			}},
		},
		Status: v1.PodStatus{Phase: v1.PodRunning},
	}
}

// workloadPod is the pod under test. The carbon labels and annotations are the
// same for every profile; default-scheduler ignores them.
func workloadPod(name, schedulerName string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID("uid-" + name),
			Labels: map[string]string{
				v1alpha1.LabelEnabled: v1alpha1.EnabledTrue,
				"app":                 "bench",
			},
			Annotations: map[string]string{
				v1alpha1.AnnotationExpectedDuration: "3600s",
				v1alpha1.AnnotationSLADeadline:      "14400s",
			},
		},
		Spec: v1.PodSpec{
			SchedulerName: schedulerName,
			Containers: []v1.Container{{
				Name:  "c",
				Image: "registry.k8s.io/pause:3.10",
				Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("100m"),
					v1.ResourceMemory: resource.MustParse("128Mi"),
				}},
			}},
		},
	}
}

// ---------------------------------------------------------------------------
// In-process scheduler.
// ---------------------------------------------------------------------------

type schedEnv struct {
	sched    *k8sscheduler.Scheduler
	client   *fake.Clientset
	snapshot *internalcache.Snapshot
	nodes    []*v1.Node
	ctx      context.Context
	cancel   context.CancelFunc

	bindMu   sync.Mutex
	boundAt  map[string]time.Time
	bindNode map[string]string
	bindCh   chan string

	// lastSamples holds the raw cycle durations of the latest report run,
	// per profile, for the Welch interval on the mean overhead.
	lastSamples map[string][]time.Duration
}

var klogOnce sync.Once

func quietKlog() {
	klogOnce.Do(func() {
		klog.SetOutput(io.Discard)
		klog.LogToStderr(false)
	})
}

// newSchedEnv starts the scheduler with n nodes and one resident pod per
// node. It returns once informers are synced and every CarbonAware instance
// has stored a CDS snapshot, so measurements never hit the empty-cache fallback.
func newSchedEnv(tb testing.TB, n int) *schedEnv {
	tb.Helper()
	quietKlog()
	cdsURL := startCDS(tb)
	profiles := loadProfiles(tb, cdsURL)

	objs := make([]runtime.Object, 0, 2*n)
	nodes := make([]*v1.Node, 0, n)
	for i := 0; i < n; i++ {
		node := benchNode(i)
		nodes = append(nodes, node)
		objs = append(objs, node, residentPod(node, i))
	}
	client := fake.NewClientset(objs...)

	env := &schedEnv{
		client:   client,
		snapshot: internalcache.NewEmptySnapshot(),
		nodes:    nodes,
		boundAt:  map[string]time.Time{},
		bindNode: map[string]string{},
		bindCh:   make(chan string, 16384),
	}
	// The fake tracker has no pods/binding subresource; emulate the apiserver.
	client.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok || ca.GetSubresource() != "binding" {
			return false, nil, nil
		}
		b, ok := ca.GetObject().(*v1.Binding)
		if !ok {
			return false, nil, nil
		}
		now := time.Now()
		env.bindMu.Lock()
		env.boundAt[b.Name] = now
		env.bindNode[b.Name] = b.Target.Name
		env.bindMu.Unlock()
		select {
		case env.bindCh <- b.Name:
		default:
		}
		return true, b, nil
	})

	syncBase := counterValue(tb, "carbon_aware_sync_duration_seconds", map[string]string{"status": "success"})
	ctx, cancel := context.WithCancel(context.Background())
	env.ctx, env.cancel = ctx, cancel
	tb.Cleanup(cancel)

	broadcaster := events.NewBroadcaster(&events.EventSinkImpl{Interface: client.EventsV1()})
	informerFactory := k8sscheduler.NewInformerFactory(client, 0, nil)
	registry := frameworkruntime.Registry{carbonaware.Name: carbonaware.New}

	sched, err := k8sscheduler.New(ctx, client, informerFactory, nil,
		profile.NewRecorderFactory(broadcaster),
		k8sscheduler.WithProfiles(profiles...),
		k8sscheduler.WithFrameworkOutOfTreeRegistry(registry),
		k8sscheduler.WithNodeInfoSnapshot(env.snapshot),
	)
	if err != nil {
		tb.Fatalf("scheduler.New: %v", err)
	}
	env.sched = sched
	broadcaster.StartRecordingToSink(ctx.Done())
	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())
	if err := sched.WaitForHandlersSync(ctx); err != nil {
		tb.Fatalf("WaitForHandlersSync: %v", err)
	}
	env.waitNodesInCache(tb)
	// Two CarbonAware instances: carbon-aware-scheduler and carbon-only-scheduler.
	waitCarbonSnapshots(tb, syncBase+2)
	return env
}

func (e *schedEnv) waitNodesInCache(tb testing.TB) {
	tb.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.sched.Cache.UpdateSnapshot(klog.Background(), e.snapshot); err == nil &&
			e.snapshot.NumNodesInPlacement() == len(e.nodes) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	tb.Fatalf("scheduler cache did not see %d nodes", len(e.nodes))
}

// waitCarbonSnapshots blocks until the Cache Sync Workers have completed at
// least want successful ticks in total since process start.
func waitCarbonSnapshots(tb testing.TB, want float64) {
	tb.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if counterValue(tb, "carbon_aware_sync_duration_seconds", map[string]string{"status": "success"}) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tb.Fatal("CarbonAware Cache Sync Worker never stored a snapshot")
}

// counterValue sums a counter, or a histogram's sample count, over the series
// that match labels.
func counterValue(tb testing.TB, name string, labels map[string]string) float64 {
	tb.Helper()
	mfs, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		tb.Fatalf("gather: %v", err)
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metric:
		for _, m := range mf.GetMetric() {
			for k, v := range labels {
				found := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == k && lp.GetValue() == v {
						found = true
					}
				}
				if !found {
					continue metric
				}
			}
			switch {
			case m.GetCounter() != nil:
				sum += m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				sum += float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return sum
}

// queued wraps a pod the way the scheduling queue hands it to a cycle. No
// PodSignature is set, so opportunistic batching never short-circuits the
// algorithm: every call runs Filter and Score in full for both profiles.
func queued(tb testing.TB, pod *v1.Pod) *framework.QueuedPodInfo {
	tb.Helper()
	pi, err := framework.NewPodInfo(pod)
	if err != nil {
		tb.Fatal(err)
	}
	return &framework.QueuedPodInfo{PodInfo: pi}
}

// cycle runs one scheduling algorithm pass exactly as schedulingCycle does
// before assume/reserve: refresh the NodeInfo snapshot, then SchedulePod
// (PreFilter, Filter, PostFilter if needed, PreScore, Score, NormalizeScore,
// select host). The pod is not bound, so cluster state is identical across
// iterations.
func (e *schedEnv) cycle(profileName string, qpi *framework.QueuedPodInfo) (string, time.Duration, error) {
	f, ok := e.sched.Profiles[profileName]
	if !ok {
		return "", 0, fmt.Errorf("no profile %q", profileName)
	}
	start := time.Now()
	if err := e.sched.Cache.UpdateSnapshot(klog.Background(), e.snapshot); err != nil {
		return "", 0, err
	}
	state := framework.NewCycleState()
	res, err := e.sched.SchedulePod(e.ctx, f, state, qpi)
	d := time.Since(start)
	if err != nil {
		return "", d, err
	}
	return res.SuggestedHost, d, nil
}

func (e *schedEnv) nodeInfos(tb testing.TB) []fwk.NodeInfo {
	tb.Helper()
	list, err := e.snapshot.NodeInfos().List()
	if err != nil {
		tb.Fatal(err)
	}
	return list
}

func zoneOfNode(name string) string {
	var i int
	if _, err := fmt.Sscanf(name, "worker-%03d", &i); err != nil {
		return ""
	}
	return zoneOrder[i%len(zoneOrder)]
}

// ---------------------------------------------------------------------------
// Statistics.
// ---------------------------------------------------------------------------

type latencyStats struct {
	Samples int     `json:"samples"`
	MeanUs  float64 `json:"mean_us"`
	StdUs   float64 `json:"std_us"`
	MinUs   float64 `json:"min_us"`
	P50Us   float64 `json:"p50_us"`
	P90Us   float64 `json:"p90_us"`
	P95Us   float64 `json:"p95_us"`
	P99Us   float64 `json:"p99_us"`
	MaxUs   float64 `json:"max_us"`
}

func summarize(ds []time.Duration) latencyStats {
	if len(ds) == 0 {
		return latencyStats{}
	}
	us := make([]float64, len(ds))
	var sum float64
	for i, d := range ds {
		us[i] = float64(d.Nanoseconds()) / 1e3
		sum += us[i]
	}
	sort.Float64s(us)
	mean := sum / float64(len(us))
	var ss float64
	for _, v := range us {
		ss += (v - mean) * (v - mean)
	}
	std := 0.0
	if len(us) > 1 {
		std = sqrt(ss / float64(len(us)-1))
	}
	return latencyStats{
		Samples: len(us),
		MeanUs:  round2(mean),
		StdUs:   round2(std),
		MinUs:   round2(us[0]),
		P50Us:   round2(quantile(us, 0.50)),
		P90Us:   round2(quantile(us, 0.90)),
		P95Us:   round2(quantile(us, 0.95)),
		P99Us:   round2(quantile(us, 0.99)),
		MaxUs:   round2(us[len(us)-1]),
	}
}

// quantile uses linear interpolation between closest ranks (type 7).
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return sorted[lo] + frac*(sorted[lo+1]-sorted[lo])
}
