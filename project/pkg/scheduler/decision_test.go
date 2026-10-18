package scheduler

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/metrics"
)

func decisions(zone, node, mode string) float64 {
	return testutil.ToFloat64(metrics.SchedulerDecisionsTotal.WithLabelValues(zone, node, mode))
}

func TestPostBindRecordsDecisionAndEstimates(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 500})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("dec-n1", "north", 1000, 0)
	n2 := testNode("dec-n2", "west", 1000, 0)
	mode := string(p.args.ScoringMode)

	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(500, "3600s", ""), []fwk.NodeInfo{n1, n2}))
	ds := readDecisionState(state)
	if ds == nil {
		t.Fatal("PreScore must write the decision state")
	}
	est := ds.nodes["dec-n1"]
	if !est.hasEstimate || est.zone != "north" || est.carbonGCO2e <= 0 || est.energyKWh <= 0 {
		t.Fatalf("estimate=%+v", est)
	}
	// CF of a constant-CI horizon is E * CI, so the two estimates must agree (2.10).
	if got, want := est.carbonGCO2e, est.energyKWh*80; math.Abs(got-want) > 1e-9*want {
		t.Fatalf("cf=%v, energy*ci=%v", got, want)
	}

	before := decisions("north", "dec-n1", mode)
	p.PostBind(context.Background(), state, nil, "dec-n1")
	if got := decisions("north", "dec-n1", mode); got != before+1 {
		t.Fatalf("decisions_total=%v, want %v", got, before+1)
	}
	if testutil.CollectAndCount(metrics.PodEstimatedCarbonGCO2) == 0 {
		t.Fatal("estimated carbon histogram must have a series")
	}
	if got := testutil.ToFloat64(metrics.PodEstimatedEnergyKWh.WithLabelValues("north", mode)); got != est.energyKWh {
		t.Fatalf("energy gauge=%v, want %v", got, est.energyKWh)
	}
}

func TestPostBindCountsFallbackDecisionsWithoutEstimate(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	p := testPlugin(v1alpha1.NewFakeClock(now), nil) // empty snapshot → G-08
	n1 := testNode("fb-n1", "west", 1000, 0)
	mode := string(p.args.ScoringMode)
	emptyBefore := testutil.ToFloat64(metrics.SchedulerFallbackTotal.WithLabelValues(metrics.FallbackReasonEmpty))

	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(100, "60s", ""), []fwk.NodeInfo{n1}))
	if got := testutil.ToFloat64(metrics.SchedulerFallbackTotal.WithLabelValues(metrics.FallbackReasonEmpty)); got != emptyBefore+1 {
		t.Fatalf("scheduler_fallback_total{empty}=%v, want %v", got, emptyBefore+1)
	}
	if ds := readDecisionState(state); ds == nil || ds.nodes["fb-n1"].hasEstimate {
		t.Fatalf("fallback must store zone without estimate: %+v", ds)
	}
	before := decisions("west", "fb-n1", mode)
	p.PostBind(context.Background(), state, nil, "fb-n1")
	if got := decisions("west", "fb-n1", mode); got != before+1 {
		t.Fatalf("decisions_total=%v, want %v", got, before+1)
	}
}

func TestPostBindWithoutPreScoreUsesUnknownZone(t *testing.T) {
	// Single feasible node: the framework skips PreScore/Score; nil handle → no informer.
	p := testPlugin(v1alpha1.NewFakeClock(time.Now()), nil)
	mode := string(p.args.ScoringMode)
	before := decisions(metrics.UnknownLabel, "solo", mode)
	p.PostBind(context.Background(), k8sframework.NewCycleState(), nil, "solo")
	if got := decisions(metrics.UnknownLabel, "solo", mode); got != before+1 {
		t.Fatalf("decisions_total=%v, want %v", got, before+1)
	}
}

func TestStaleInWindowCountsSchedulerFallback(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now.Add(-120*time.Second), v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	before := testutil.ToFloat64(metrics.SchedulerFallbackTotal.WithLabelValues(metrics.FallbackReasonStale))
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(100, "60s", ""), []fwk.NodeInfo{testNode("st-n1", "north", 1000, 0)}))
	if got := testutil.ToFloat64(metrics.SchedulerFallbackTotal.WithLabelValues(metrics.FallbackReasonStale)); got != before+1 {
		t.Fatalf("scheduler_fallback_total{stale}=%v, want %v", got, before+1)
	}
}

func TestPluginDurationObservedPerExtensionPoint(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	ni := testNode("dur-n1", "north", 1000, 0)
	count := func(point string) uint64 {
		return histogramCount(t, point)
	}
	before := map[string]uint64{}
	for _, pt := range []string{metrics.PointPreScore, metrics.PointScore, metrics.PointNormalizeScore, metrics.PointPostBind} {
		before[pt] = count(pt)
	}
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(100, "60s", ""), []fwk.NodeInfo{ni}))
	score := lookup(t, p, state, ni)
	requireSuccess(t, p.NormalizeScore(context.Background(), state, nil, fwk.NodeScoreList{{Name: "dur-n1", Score: score}}))
	p.PostBind(context.Background(), state, nil, "dur-n1")
	for pt, b := range before {
		if got := count(pt); got != b+1 {
			t.Errorf("%s: histogram count %d, want %d", pt, got, b+1)
		}
	}
}

func TestScoreHotPathStaysAllocationFree(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	ni := testNode("alloc-n1", "north", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(100, "60s", ""), []fwk.NodeInfo{ni}))
	ctx := context.Background()
	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = p.Score(ctx, state, nil, ni)
	})
	// fwk.NewStatus(Success) allocates one *Status; metrics must add nothing.
	if allocs > 1 {
		t.Fatalf("Score allocs/op=%v, want <= 1", allocs)
	}
}

func histogramCount(t *testing.T, point string) uint64 {
	t.Helper()
	h, ok := metrics.PluginDurationSeconds.WithLabelValues(point).(interface{ Write(*dto.Metric) error })
	if !ok {
		t.Fatal("histogram child does not implement Write")
	}
	m := &dto.Metric{}
	if err := h.Write(m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}
