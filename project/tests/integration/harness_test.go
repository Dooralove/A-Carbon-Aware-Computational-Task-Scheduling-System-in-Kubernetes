// Package integration_test wires the real Carbon Data Service (provider →
// cache → REST) to the real scheduler-side Cache Sync Worker and PreScore/Score
// over loopback HTTP (T8.1). No Kubernetes API server is involved.
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/metrics/legacyregistry"
	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/api"
	"carbon-aware-k8s/pkg/carbon/cache"
	"carbon-aware-k8s/pkg/scheduler"
)

// tunableProvider is an in-process carbon adapter whose intensities can be
// changed while the CDS refresh loop is running (the "provider" end of T8.1).
type tunableProvider struct {
	mu    sync.RWMutex
	zones map[v1alpha1.ZoneID]float64
	calls atomic.Int64
}

func newTunableProvider(ci map[string]float64) *tunableProvider {
	p := &tunableProvider{zones: make(map[v1alpha1.ZoneID]float64, len(ci))}
	for z, v := range ci {
		p.zones[v1alpha1.ZoneID(z)] = v
	}
	return p
}

func (p *tunableProvider) Set(zone string, ci float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.zones[v1alpha1.ZoneID(zone)] = ci
}

func (p *tunableProvider) Name() string                { return "integration-tunable" }
func (p *tunableProvider) Mode() v1alpha1.ProviderMode { return v1alpha1.ProviderModeMock }

func (p *tunableProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]v1alpha1.ZoneProfile, 0, len(p.zones))
	for z := range p.zones {
		out = append(out, v1alpha1.ZoneProfile{
			APIVersion:          v1alpha1.GroupVersion,
			Kind:                v1alpha1.KindZoneProfile,
			Zone:                z,
			Quality:             v1alpha1.QualityFresh,
			Source:              v1alpha1.SourceMock,
			TemporalGranularity: v1alpha1.Granularity5Minutes,
			IntervalSeconds:     300,
			HasCurrent:          true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Zone < out[j].Zone })
	return out, nil
}

func (p *tunableProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if err := ctx.Err(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	p.calls.Add(1)
	p.mu.RLock()
	ci, ok := p.zones[zone]
	p.mu.RUnlock()
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

func (p *tunableProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, _ time.Duration) ([]v1alpha1.ForecastInterval, error) {
	if _, err := p.Current(ctx, zone, at); err != nil {
		return nil, err
	}
	return []v1alpha1.ForecastInterval{}, nil
}

var _ v1alpha1.CarbonProvider = (*tunableProvider)(nil)

// cdsStack is a running Carbon Data Service on a loopback port.
type cdsStack struct {
	URL      string
	Cache    *cache.Cache
	server   *httptest.Server
	requests atomic.Int64
	closed   atomic.Bool
}

// Close stops the HTTP listener (simulates the CDS pod going away).
func (s *cdsStack) Close() {
	if s.closed.CompareAndSwap(false, true) {
		s.server.CloseClientConnections()
		s.server.Close()
	}
}

// startCDS runs the real cache refresh loop and REST handlers. middleware, if
// non-nil, wraps the REST handler (latency/jitter injection).
func startCDS(t *testing.T, prov v1alpha1.CarbonProvider, refresh time.Duration, middleware func(http.Handler) http.Handler) *cdsStack {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := cache.NewWithOptions(cache.Options{
		Provider:        prov,
		Logger:          logger,
		RefreshInterval: refresh,
		RefreshTimeout:  time.Second,
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("initial CDS refresh: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Loop(ctx)
	}()

	stack := &cdsStack{Cache: c}
	var h http.Handler = api.New("127.0.0.1:0", c, prov, logger).Handler()
	if middleware != nil {
		h = middleware(h)
	}
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stack.requests.Add(1)
		h.ServeHTTP(w, r)
	})
	stack.server = httptest.NewServer(counted)
	stack.URL = stack.server.URL
	t.Cleanup(func() {
		stack.Close()
		cancel()
		<-done
	})
	return stack
}

// jitter delays every request by a uniform random duration in [lo, hi].
func jitter(lo, hi time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d := lo
			if hi > lo {
				d += time.Duration(rand.Int64N(int64(hi - lo)))
			}
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// pluginArgs returns stand-canon args (scheduler-config.yaml) pointed at url.
func pluginArgs(url string) scheduler.PluginArgs {
	return scheduler.PluginArgs{
		ScoringMode:       v1alpha1.ScoringModeExpectedCF,
		CarbonWeight:      0.6,
		ResourceWeight:    0.2,
		PerformanceWeight: 0.2,
		Lambda:            1,
		PowerIdleWatts:    40,
		PowerMaxWatts:     100,
		PowerProfiles: map[string]v1alpha1.PowerProfileSpec{
			"standard": {PowerIdleWatts: 40, PowerMaxWatts: 100},
		},
		RuntimeClassSeconds: map[v1alpha1.WorkloadClass]int64{
			v1alpha1.WorkloadClassBatch: 1800,
		},
		MaxDataAgeSeconds:      300,
		StaleWarnSeconds:       90,
		ClockSkewSeconds:       60,
		RefreshIntervalSeconds: 30,
		HTTPTimeoutSeconds:     2,
		CarbonServiceURL:       url,
	}
}

// startPlugin builds the plugin through the production factory, which starts
// the Cache Sync Worker goroutine against the CDS URL.
func startPlugin(t *testing.T, args scheduler.PluginArgs) *scheduler.CarbonAwarePlugin {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pl, err := scheduler.New(ctx, &runtime.Unknown{Raw: raw, ContentType: runtime.ContentTypeJSON}, nil)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	p, ok := pl.(*scheduler.CarbonAwarePlugin)
	if !ok {
		t.Fatalf("unexpected plugin type %T", pl)
	}
	return p
}

func carbonPod(name string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID("uid-" + name),
			Labels: map[string]string{
				v1alpha1.LabelEnabled:       v1alpha1.EnabledTrue,
				v1alpha1.LabelWorkloadClass: string(v1alpha1.WorkloadClassBatch),
			},
			Annotations: map[string]string{
				v1alpha1.AnnotationExpectedDuration: "300s",
				v1alpha1.AnnotationSLADeadline:      "1800s",
			},
		},
		Spec: v1.PodSpec{Containers: []v1.Container{{
			Name: "c",
			Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("100m"),
				v1.ResourceMemory: resource.MustParse("64Mi"),
			}},
		}}},
	}
}

func zoneNode(name, zone string) fwk.NodeInfo {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			v1alpha1.LabelZone:         zone,
			v1alpha1.LabelPowerProfile: "standard",
		}},
		Status: v1.NodeStatus{Allocatable: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("8Gi"),
		}},
	}
	ni := k8sframework.NewNodeInfo()
	ni.SetNode(node)
	return ni
}

// standNodes mirrors the kind topology: worker-01/02 north, worker-03/04 west.
func standNodes() []fwk.NodeInfo {
	return []fwk.NodeInfo{
		zoneNode("worker-01", "north"),
		zoneNode("worker-02", "north"),
		zoneNode("worker-03", "west"),
		zoneNode("worker-04", "west"),
	}
}

// decision is the outcome of one PreScore → Score → NormalizeScore cycle.
type decision struct {
	scores map[string]int64
	best   string
}

// neutral reports whether every node got the same framework score (G-08).
func (d decision) neutral() bool {
	var first int64 = -1
	for _, s := range d.scores {
		if first < 0 {
			first = s
		}
		if s != first {
			return false
		}
	}
	return true
}

func (d decision) bestZone(nodes []fwk.NodeInfo) string {
	for _, ni := range nodes {
		if ni.Node().Name == d.best {
			return ni.Node().Labels[v1alpha1.LabelZone]
		}
	}
	return ""
}

// decide runs the plugin's extension points exactly as the framework would.
func decide(p *scheduler.CarbonAwarePlugin, pod *v1.Pod, nodes []fwk.NodeInfo) (decision, error) {
	ctx := context.Background()
	state := k8sframework.NewCycleState()
	if st := p.PreScore(ctx, state, pod, nodes); !st.IsSuccess() {
		return decision{}, fmt.Errorf("PreScore: %v", st)
	}
	list := make(fwk.NodeScoreList, 0, len(nodes))
	for _, ni := range nodes {
		s, st := p.Score(ctx, state, pod, ni)
		if !st.IsSuccess() {
			return decision{}, fmt.Errorf("Score(%s): %v", ni.Node().Name, st)
		}
		list = append(list, fwk.NodeScore{Name: ni.Node().Name, Score: s})
	}
	if st := p.ScoreExtensions().NormalizeScore(ctx, state, pod, list); !st.IsSuccess() {
		return decision{}, fmt.Errorf("NormalizeScore: %v", st)
	}
	d := decision{scores: make(map[string]int64, len(list))}
	var bestScore int64 = -1
	for _, ns := range list {
		d.scores[ns.Name] = ns.Score
		if ns.Score > bestScore {
			bestScore, d.best = ns.Score, ns.Name
		}
	}
	return d, nil
}

func snapshotCI(p *scheduler.CarbonAwarePlugin, zone string) (float64, bool) {
	snap := p.Snapshot()
	if snap == nil {
		return 0, false
	}
	z, ok := snap.Zones[v1alpha1.ZoneID(zone)]
	if !ok {
		return 0, false
	}
	return z.Current.CarbonIntensityGCO2ePerKWh, true
}

// waitFor polls cond every 10ms until it is true or timeout elapses.
func waitFor(timeout time.Duration, cond func() bool) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		if cond() {
			return time.Since(start), nil
		}
		if time.Now().After(deadline) {
			return time.Since(start), errors.New("condition not met before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// counterValue sums a counter family in the scheduler's legacy registry,
// optionally filtered by one label. names are tried in order and the first
// family present is used: Stage 7 added carbon_aware_scheduler_fallback_total
// next to carbon_aware_fallback_total (metrics.md v1.1.0, ADR-003), and both
// count the same events, so summing them would double-count.
func counterValue(t *testing.T, names []string, label, value string) float64 {
	t.Helper()
	mfs, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	byName := make(map[string]*dto.MetricFamily, len(mfs))
	for _, mf := range mfs {
		byName[mf.GetName()] = mf
	}
	var sum float64
	for _, n := range names {
		mf, ok := byName[n]
		if !ok {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label != "" && !hasLabel(m, label, value) {
				continue
			}
			if c := m.GetCounter(); c != nil {
				sum += c.GetValue()
			}
		}
		break
	}
	return sum
}

func hasLabel(m *dto.Metric, name, value string) bool {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name && lp.GetValue() == value {
			return true
		}
	}
	return false
}

var fallbackMetricNames = []string{"carbon_aware_scheduler_fallback_total", "carbon_aware_fallback_total"}

func percentile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(q*float64(len(s)-1) + 0.5)
	return s[idx]
}
