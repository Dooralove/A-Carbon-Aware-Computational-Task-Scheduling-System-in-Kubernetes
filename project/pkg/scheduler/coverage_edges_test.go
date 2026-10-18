package scheduler

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestNewForTestsAndSeedSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	args := validPluginArgs()
	p, err := NewForTests(args, v1alpha1.NewFakeClock(now), nil)
	if err != nil {
		t.Fatalf("NewForTests: %v", err)
	}
	if p.Snapshot() != nil {
		t.Fatal("empty cache must be Fallback-ready")
	}
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80})
	p.SeedSnapshot(snap)
	got := p.Snapshot()
	if got == nil || len(got.Zones) != 1 {
		t.Fatalf("SeedSnapshot: %+v", got)
	}

	bad := validPluginArgs()
	bad.CarbonWeight = 0.5
	if _, err := NewForTests(bad, nil, nil); err == nil {
		t.Fatal("NewForTests must Validate args")
	}
	withWall, err := NewForTests(args, nil, nil)
	if err != nil || withWall == nil {
		t.Fatalf("nil clock: %v", err)
	}
	var nilPlugin *CarbonAwarePlugin
	nilPlugin.SeedSnapshot(snap)
	(&CarbonAwarePlugin{}).SeedSnapshot(snap)
}

func TestScoreExtensionsReturnsPlugin(t *testing.T) {
	p := &CarbonAwarePlugin{}
	if p.ScoreExtensions() != p {
		t.Fatal("ScoreExtensions must return the receiver")
	}
}

func TestCycleStateCloneAndGuards(t *testing.T) {
	state := k8sframework.NewCycleState()
	writeCycleState(nil, &v1alpha1.CarbonCycleState{})
	writeCycleState(state, &v1alpha1.CarbonCycleState{
		ScorePerNode: map[string]int64{"n1": 8400},
		PodUID:       "pod-1",
	})
	cloned := state.Clone()
	got, err := readCycleState(cloned)
	if err != nil || got == nil || got.ScorePerNode["n1"] != 8400 {
		t.Fatalf("Clone must preserve payload: err=%v got=%+v", err, got)
	}

	state.Write(cycleStateKey, bogus{})
	if _, err := readCycleState(state); err == nil {
		t.Fatal("wrong StateData type must be ErrNotFound")
	}
	if name := nodeName(nil); name != "" {
		t.Fatalf("nil NodeInfo name=%q", name)
	}
	if name := nodeName(k8sframework.NewNodeInfo()); name != "" {
		t.Fatalf("unset Node name=%q", name)
	}
}

type bogus struct{}

func (bogus) Clone() fwk.StateData { return bogus{} }

func TestSnapshotAndNowNilGuards(t *testing.T) {
	var p *CarbonAwarePlugin
	if p.Snapshot() != nil {
		t.Fatal("nil plugin Snapshot")
	}
	if ts := (&CarbonAwarePlugin{}).now(); ts.Location() != time.UTC {
		t.Fatalf("nil clock must use UTC, got %s", ts.Location())
	}
	if (&CarbonAwarePlugin{}).Snapshot() != nil {
		t.Fatal("nil cache Snapshot")
	}
}

func TestDecodePluginArgsInvalidJSON(t *testing.T) {
	_, err := decodePluginArgs(&runtime.Unknown{Raw: []byte("{"), ContentType: runtime.ContentTypeJSON})
	if err == nil {
		t.Fatal("invalid JSON must fail decode")
	}
}

func TestLocalCarbonCacheNilAndAgeGuards(t *testing.T) {
	var cache *LocalCarbonCache
	if _, ok := cache.GetZoneState("north"); ok {
		t.Fatal("nil cache GetZoneState")
	}
	if cache.CarbonSnapshot() != nil {
		t.Fatal("nil cache CarbonSnapshot")
	}
	if _, ok := cache.SnapshotAge(time.Now()); ok {
		t.Fatal("nil cache SnapshotAge")
	}
	cache.replaceAPI(v1alpha1.CarbonSnapshot{})

	c := NewLocalCarbonCache(nil, 60, 300)
	if _, ok := c.GetZoneState("north"); ok {
		t.Fatal("empty")
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	c.snap.Store(&carbonSnapshot{
		fetchedAt: now.Add(time.Hour),
		quality:   v1alpha1.QualityFresh,
		zones: map[string]*ZoneCarbonState{
			"north": {
				Zone:     "north",
				Forecast: []v1alpha1.ForecastInterval{{Timestamp: v1alpha1.RFC3339Time{Time: now}}},
				IsValid:  true,
			},
			"ghost": nil,
		},
	})
	if _, ok := c.GetZoneState("ghost"); ok {
		t.Fatal("nil zone pointer")
	}
	if _, ok := c.GetZoneState("missing"); ok {
		t.Fatal("missing zone")
	}
	got, ok := c.GetZoneState("north")
	if !ok || len(got.Forecast) != 1 {
		t.Fatalf("forecast copy: ok=%v got=%+v", ok, got)
	}
	age, ok := c.SnapshotAge(now)
	if !ok || age != 0 {
		t.Fatalf("now before fetchedAt: age=%s ok=%v", age, ok)
	}
	c.publish(map[string]*ZoneCarbonState{"north": {Zone: "north"}}, time.Time{}, v1alpha1.QualityFresh)
	if _, ok := c.SnapshotAge(now); ok {
		t.Fatal("zero fetchedAt is unpublished age")
	}
}

func TestBuildZoneStateBranches(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	rec := validRecord("north", now)
	rec.Unit = ""
	rec.EmissionFactorType = ""
	st, ok := buildZoneState(rec, emptyForecast("north", now), now, 60)
	if !ok || st.Record.Unit != v1alpha1.IntensityUnitGCO2ePerKWh {
		t.Fatalf("defaults: ok=%v unit=%q", ok, st.Record.Unit)
	}

	badCI := rec
	badCI.CarbonIntensityGCO2ePerKWh = math.NaN()
	if _, ok := buildZoneState(badCI, emptyForecast("north", now), now, 60); ok {
		t.Fatal("NaN CI")
	}

	future := validRecord("north", now.Add(2*time.Hour))
	if _, ok := buildZoneState(future, emptyForecast("north", now), now, 60); ok {
		t.Fatal("FF-FUT")
	}

	badFC := v1alpha1.CarbonForecast{
		Zone: "north",
		Intervals: []v1alpha1.ForecastInterval{{
			CarbonIntensityGCO2ePerKWh: 100,
		}},
	}
	if _, ok := buildZoneState(validRecord("north", now), badFC, now, 60); ok {
		t.Fatal("invalid forecast intervals")
	}
}

func TestUpdateFromServiceNilGuardsAndSkipZone(t *testing.T) {
	var cache *LocalCarbonCache
	if err := cache.UpdateFromService(context.Background(), NewCarbonServiceClient("http://127.0.0.1", time.Second)); err == nil {
		t.Fatal("nil cache")
	}
	c := NewLocalCarbonCache(v1alpha1.NewFakeClock(time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)), 60, 300)
	if err := c.UpdateFromService(context.Background(), nil); err == nil {
		t.Fatal("nil client")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == v1alpha1.PathZones {
			_ = json.NewEncoder(w).Encode(v1alpha1.ZoneProfileList{
				Items: []v1alpha1.ZoneProfile{{Zone: ""}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	err := c.UpdateFromService(context.Background(), NewCarbonServiceClient(srv.URL, time.Second))
	if err == nil {
		t.Fatal("all skipped zones must not Store")
	}
}

func TestCarbonServiceClientGuards(t *testing.T) {
	c := NewCarbonServiceClient(" http://example.invalid/cds/ ", 0)
	if c.timeout != time.Duration(DefaultHTTPTimeoutSeconds)*time.Second {
		t.Fatalf("timeout=%s", c.timeout)
	}
	if c.baseURL != "http://example.invalid/cds" {
		t.Fatalf("baseURL=%q", c.baseURL)
	}
	var nilClient *CarbonServiceClient
	if _, err := nilClient.ListZones(context.Background()); err == nil {
		t.Fatal("nil client")
	}
	broken := &CarbonServiceClient{baseURL: "http://127.0.0.1", timeout: time.Second}
	if _, err := broken.ListZones(context.Background()); err == nil {
		t.Fatal("nil http")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	t.Cleanup(srv.Close)
	if _, err := NewCarbonServiceClient(srv.URL, time.Second).ListZones(context.Background()); err == nil {
		t.Fatal("bad JSON")
	}

	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{" + strings.Repeat("a", maxCDSResponseBytes) + "}"))
	}))
	t.Cleanup(big.Close)
	if _, err := NewCarbonServiceClient(big.URL, time.Second).ListZones(context.Background()); err == nil {
		t.Fatal("oversized body")
	}
}

func TestWorkerTickGuards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &CarbonAwarePlugin{
		args: PluginArgs{RefreshIntervalSeconds: 0, HTTPTimeoutSeconds: 1, CarbonServiceURL: "http://127.0.0.1:1"},
	}
	p.syncTick(ctx)
	done := make(chan struct{})
	live, stop := context.WithCancel(context.Background())
	go func() {
		p.startCacheSyncWorker(live)
		close(done)
	}()
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker must exit on ctx cancel")
	}
	recordSyncTick(0.01, true, 2)
	recordEstimatedCF("", 1.25)
}

func TestPreScoreWeightOverrideAndPowerProfile(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	p.args.AllowPodCarbonWeightOverride = true
	p.args.PowerProfiles = map[string]v1alpha1.PowerProfileSpec{
		"gpu": {PowerIdleWatts: 50, PowerMaxWatts: 200},
	}

	north := testNode("n-north", "north", 1000, 0)
	north.Node().Labels[v1alpha1.LabelPowerProfile] = "gpu"
	west := testNode("n-west", "west", 1000, 0)
	west.Node().Labels[v1alpha1.LabelPowerProfile] = "missing-profile"

	pod := scoringPod(300, "7200s", "14400s")
	pod.Annotations[v1alpha1.AnnotationCarbonWeight] = "1"
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, pod, []fwk.NodeInfo{north, west}))
	sN, sW := lookup(t, p, state, north), lookup(t, p, state, west)
	if sN != 10000 || sW != 0 {
		t.Fatalf("w_C=1 override: north=%d west=%d, want 10000 and 0", sN, sW)
	}

	p.args.CarbonWeight = 1
	p.args.ResourceWeight = 0
	p.args.PerformanceWeight = 0
	pod.Annotations[v1alpha1.AnnotationCarbonWeight] = "0.5"
	st := p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, []fwk.NodeInfo{north})
	requireSLAError(t, st)

	pod.Annotations[v1alpha1.AnnotationCarbonWeight] = "1"
	requireSuccess(t, p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, []fwk.NodeInfo{north}))
}

func TestPreScoreNilPodOptOut(t *testing.T) {
	p := testPlugin(v1alpha1.NewFakeClock(time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)), nil)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, nil, []fwk.NodeInfo{testNode("n1", "north", 1000, 0)}))
	if lookup(t, p, state, testNode("n1", "north", 1000, 0)) != v1alpha1.NeutralFixedPointScore() {
		t.Fatal("nil pod is opt-out")
	}
}

func TestEvaluateOneGuards(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	p := testPlugin(v1alpha1.NewFakeClock(now), nil)
	profile := v1alpha1.WorkloadProfile{Enabled: true, CPURequestMillicores: 300}
	if w := p.evaluateOne(now, nil, nil, nil, 7200, profile, 0.5, false); w.name != "" {
		t.Fatalf("nil node: %+v", w)
	}
	if c, m := allocatable(nil); c != 0 || m != 0 {
		t.Fatal("allocatable(nil)")
	}
	if c, m := allocatable(k8sframework.NewNodeInfo()); c != 0 || m != 0 {
		t.Fatal("allocatable(empty NodeInfo)")
	}
	if c, m := requested(nil); c != 0 || m != 0 {
		t.Fatal("requested(nil)")
	}
	if c, m := requested(k8sframework.NewNodeInfo()); c != 0 || m != 0 {
		t.Fatal("requested(empty NodeInfo)")
	}
	if c, m := podResourceRequests(nil); c != 0 || m != 0 {
		t.Fatal("podResourceRequests(nil)")
	}
	empty := k8sframework.NewNodeInfo()
	empty.SetNode(&v1.Node{ObjectMeta: metav1.ObjectMeta{Name: ""}})
	if w := p.evaluateOne(now, nil, nil, empty, 7200, profile, 0.5, false); w.name != "" {
		t.Fatalf("empty name: %+v", w)
	}
	w := p.evaluateOne(now, nil, nil, testNode("n1", "north", 1000, 0), 7200, profile, 0.5, false)
	if w.mark != markUnknown {
		t.Fatalf("nil snap with zone: mark=%v", w.mark)
	}
	payload := assembleCycleState([]nodeWork{{name: "x", hasScore: false}, {name: "y", hasScore: true, pluginScore: 80}}, nil, v1alpha1.ScoringModeExpectedCF, nil, 0, 0)
	if payload.ScorePerNode["x"] != v1alpha1.NeutralFixedPointScore() {
		t.Fatalf("missing score: %+v", payload.ScorePerNode)
	}

	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400, "east": 200})
	p.cache.replaceAPI(snap)
	p.args.ScoringMode = v1alpha1.ScoringModeCurrentCI
	state := k8sframework.NewCycleState()
	nodes := []fwk.NodeInfo{
		testNode("n1", "north", 1000, 0),
		testNode("n2", "west", 1000, 0),
		testNode("n3", "east", 1000, 0),
	}
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), nodes))
	if lookup(t, p, state, nodes[0]) <= lookup(t, p, state, nodes[1]) {
		t.Fatal("current-ci: north must beat west")
	}
}

func TestScoreMissingNodeKeyIsNeutral(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1}))
	extra := testNode("n-extra", "north", 1000, 0)
	if lookup(t, p, state, extra) != v1alpha1.NeutralFixedPointScore() {
		t.Fatal("unknown CycleState key → 50")
	}
}

func TestWarnStaleDedup(t *testing.T) {
	fetched := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(fetched.Add(120 * time.Second))
	snap := snapWithCI(fetched, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
	p := testPlugin(clock, &snap)
	n1 := testNode("n1", "north", 1000, 0)
	n2 := testNode("n2", "west", 1000, 0)
	pod := scoringPod(300, "7200s", "14400s")
	requireSuccess(t, p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, []fwk.NodeInfo{n1, n2}))
	requireSuccess(t, p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, []fwk.NodeInfo{n1, n2}))
}

func TestPreScoreRuntimeClassHorizon(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	p.args.RuntimeClassSeconds = map[v1alpha1.WorkloadClass]int64{v1alpha1.WorkloadClassBatch: 7200}
	p.args.DefaultWorkloadClass = v1alpha1.WorkloadClassBatch
	pod := scoringPod(300, "", "14400s")
	pod.Labels[v1alpha1.LabelWorkloadClass] = string(v1alpha1.WorkloadClassBatch)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, pod, []fwk.NodeInfo{testNode("n1", "north", 1000, 0)}))
}

func TestEvaluateNodesUnsafePartialNeutralizes(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	p := testPlugin(v1alpha1.NewFakeClock(now), nil)
	works := p.evaluateNodes(now, nil, nil, nil, 7200, v1alpha1.WorkloadProfile{}, 0.6, 0.2, 0.2)
	if len(works) != 0 {
		t.Fatalf("empty nodes: %d", len(works))
	}
	score := p.evaluateNodes(now, nil, nil, []fwk.NodeInfo{testNode("n1", "north", 1000, 0)}, 7200, v1alpha1.WorkloadProfile{CPURequestMillicores: 300}, math.NaN(), 0.2, 0.2)
	if len(score) != 1 || score[0].pluginScore != 50 {
		t.Fatalf("NaN weight → G-21 score=50, got %+v", score)
	}
}
