package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

func requireSuccess(t *testing.T, st *fwk.Status) {
	t.Helper()
	if !st.IsSuccess() {
		t.Fatalf("want Success, got %v", st)
	}
}

func requireSLAError(t *testing.T, st *fwk.Status) {
	t.Helper()
	if st == nil || !st.IsError() {
		t.Fatalf("want Error, got %v", st)
	}
	if st.Message() != v1alpha1.SLAPreconditionMessage {
		t.Fatalf("message=%q", st.Message())
	}
}

func testPlugin(clock v1alpha1.Clock, snap *v1alpha1.CarbonSnapshot) *CarbonAwarePlugin {
	args := validPluginArgs()
	cache := NewLocalCarbonCache(clock, args.ClockSkewSeconds, args.MaxDataAgeSeconds)
	if snap != nil {
		cache.replaceAPI(*snap)
	}
	return &CarbonAwarePlugin{args: args, cache: cache, clock: clock}
}

func scoringPod(cpuMilli int64, duration, deadline string) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job",
			Namespace: "default",
			UID:       types.UID("pod-1"),
			Labels: map[string]string{
				v1alpha1.LabelEnabled: v1alpha1.EnabledTrue,
			},
			Annotations: map[string]string{},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{
				Name: "c",
				Resources: v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceCPU: *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
					},
				},
			}},
		},
	}
	if duration != "" {
		pod.Annotations[v1alpha1.AnnotationExpectedDuration] = duration
	}
	if deadline != "" {
		pod.Annotations[v1alpha1.AnnotationSLADeadline] = deadline
	}
	return pod
}

func residentPod(id string, cpuMilli int64) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      id,
			Namespace: "default",
			UID:       types.UID(id),
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{
				Name: "c",
				Resources: v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceCPU: *resource.NewMilliQuantity(cpuMilli, resource.DecimalSI),
					},
				},
			}},
		},
	}
}

func testNode(name, zone string, allocMilli, reqMilli int64) fwk.NodeInfo {
	labels := map[string]string{}
	if zone != "" {
		labels[v1alpha1.LabelZone] = zone
	}
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse("1"),
				v1.ResourceMemory: resource.MustParse("8Gi"),
			},
		},
	}
	if allocMilli != 1000 {
		node.Status.Allocatable[v1.ResourceCPU] = *resource.NewMilliQuantity(allocMilli, resource.DecimalSI)
	}
	ni := k8sframework.NewNodeInfo()
	ni.SetNode(node)
	if reqMilli > 0 {
		ni.AddPod(residentPod(name+"-res", reqMilli))
	}
	return ni
}

func snapWithCI(fetched time.Time, quality v1alpha1.DataQuality, ci map[string]float64) v1alpha1.CarbonSnapshot {
	zones := make(map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState, len(ci))
	for z, v := range ci {
		zones[v1alpha1.ZoneID(z)] = v1alpha1.ZoneCarbonState{
			Current: v1alpha1.CarbonRecord{
				Zone:                       v1alpha1.ZoneID(z),
				Timestamp:                  v1alpha1.RFC3339Time{Time: fetched},
				CarbonIntensityGCO2ePerKWh: v,
				Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
				Source:                     v1alpha1.SourceMock,
				EmissionFactorType:         v1alpha1.EmissionLifecycle,
				Quality:                    v1alpha1.QualityFresh,
			},
		}
	}
	return v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: fetched},
		Quality:   quality,
		Zones:     zones,
	}
}

func lookup(t *testing.T, p *CarbonAwarePlugin, state fwk.CycleState, ni fwk.NodeInfo) int64 {
	t.Helper()
	score, st := p.Score(context.Background(), state, nil, ni)
	requireSuccess(t, st)
	return score
}

func TestPreScoreOptOutDoesNotUseSnapshotOrFallback(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 200)
	fallbackBefore := testutil.ToFloat64(carbonAwareFallbackTotal.WithLabelValues("empty"))
	optBefore := testutil.ToFloat64(carbonAwareOptOutTotal)

	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, pod, []fwk.NodeInfo{n1}))

	cs, err := readCycleState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.OptInDisabled || cs.FallbackMode != v1alpha1.FallbackOptOut {
		t.Fatalf("payload=%+v", cs)
	}
	if lookup(t, p, state, n1) != v1alpha1.NeutralFixedPointScore() {
		t.Fatal("opt-out must write 50")
	}
	if testutil.ToFloat64(carbonAwareFallbackTotal.WithLabelValues("empty")) != fallbackBefore {
		t.Fatal("opt-out must not increment fallback_total")
	}
	if testutil.ToFloat64(carbonAwareOptOutTotal) <= optBefore {
		t.Fatal("opt_out_total must increase")
	}
}

func TestPreScoreInvalidEnabledIsSLAError(t *testing.T) {
	p := testPlugin(v1alpha1.NewFakeClock(time.Now()), nil)
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{v1alpha1.LabelEnabled: "yes"},
	}}
	st := p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, nil)
	requireSLAError(t, st)
}

func TestPreScoreHardSLA(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	p := testPlugin(v1alpha1.NewFakeClock(now), nil)
	slaBefore := testutil.ToFloat64(carbonAwareSLARejectionTotal)
	fbBefore := testutil.ToFloat64(carbonAwareFallbackTotal.WithLabelValues("empty"))
	pod := scoringPod(300, "1800s", "1200s")
	requireSLAError(t, p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, nil))
	if testutil.ToFloat64(carbonAwareSLARejectionTotal) <= slaBefore {
		t.Fatal("sla_rejection_total")
	}
	if testutil.ToFloat64(carbonAwareFallbackTotal.WithLabelValues("empty")) != fbBefore {
		t.Fatal("SLA must not increment fallback_total")
	}
}

func TestPreScoreG20MissingHorizon(t *testing.T) {
	p := testPlugin(v1alpha1.NewFakeClock(time.Now()), nil)
	pod := scoringPod(300, "", "")
	requireSLAError(t, p.PreScore(context.Background(), k8sframework.NewCycleState(), pod, nil))
}

func TestPreScoreEmptySnapshotIsG08(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	p := testPlugin(v1alpha1.NewFakeClock(now), nil)
	n1 := testNode("n1", "north", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{n1}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackEmpty || !cs.DataStale {
		t.Fatalf("%+v", cs)
	}
	if lookup(t, p, state, n1) != v1alpha1.NeutralFixedPointScore() {
		t.Fatal("G-08 empty")
	}
}

func TestPreScoreDeadSnapshotIsG08(t *testing.T) {
	fetched := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(fetched.Add(301 * time.Second))
	snap := snapWithCI(fetched, v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(clock, &snap)
	n1 := testNode("n1", "north", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackDead {
		t.Fatalf("mode=%s", cs.FallbackMode)
	}
	if lookup(t, p, state, n1) != v1alpha1.NeutralFixedPointScore() {
		t.Fatal("G-08 dead")
	}
}

func TestPreScoreOracle14_3(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "west", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{n1, n2}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackFresh {
		t.Fatalf("mode=%s", cs.FallbackMode)
	}
	if s1, s2 := lookup(t, p, state, n1), lookup(t, p, state, n2); s1 != 8000 || s2 != 1600 {
		t.Fatalf("fixed-point n1=%d n2=%d, want 8000 and 1600 (Score 80 and 16)", s1, s2)
	}
	scores := fwk.NodeScoreList{{Name: "n1", Score: 8000}, {Name: "n2", Score: 1600}}
	requireSuccess(t, p.NormalizeScore(context.Background(), state, nil, scores))
	if scores[0].Score != 80 || scores[1].Score != 16 {
		t.Fatalf("framework scores=%v", scores)
	}
}

func TestPreScoreStaleInWindowStillRanks(t *testing.T) {
	fetched := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(fetched.Add(120 * time.Second))
	snap := snapWithCI(fetched, v1alpha1.QualityFresh, map[string]float64{"north": 100, "west": 400})
	p := testPlugin(clock, &snap)
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "west", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{n1, n2}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackStale || !cs.DataStale {
		t.Fatalf("%+v", cs)
	}
	if lookup(t, p, state, n1) == v1alpha1.NeutralFixedPointScore() && lookup(t, p, state, n2) == v1alpha1.NeutralFixedPointScore() {
		t.Fatal("SM-STALE must not replace Score with 50")
	}
	if lookup(t, p, state, n1) <= lookup(t, p, state, n2) {
		t.Fatal("north must still rank above west")
	}
}

func TestPreScoreStaleQualityFlag(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityStale, map[string]float64{"north": 100, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "west", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1, n2}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackStale {
		t.Fatalf("quality=stale → SM-STALE, got %s", cs.FallbackMode)
	}
}

func TestPreScoreMissingZoneIsG09NotUnschedulable(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{n1, n2}))
	cs, _ := readCycleState(state)
	if cs.ZonesMissing != 1 {
		t.Fatalf("zonesMissing=%d", cs.ZonesMissing)
	}
	if lookup(t, p, state, n2) == 0 {
		t.Fatal("G-09 must not use Score=0 as a filter")
	}
	_, st := p.Score(context.Background(), state, nil, n2)
	requireSuccess(t, st)
}

func TestPreScoreUnknownZoneInSnapshotIsG09(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 0)
	n2 := testNode("n2", "west", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1, n2}))
	cs, _ := readCycleState(state)
	if cs.ZonesMissing < 1 {
		t.Fatal("west missing from snapshot is NM-UNKNOWN")
	}
}

func TestPreScoreAllUnknownIsG08(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "", 1000, 0)
	n2 := testNode("n2", "", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1, n2}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackAllInvalid {
		t.Fatalf("mode=%s", cs.FallbackMode)
	}
	if lookup(t, p, state, n1) != v1alpha1.NeutralFixedPointScore() {
		t.Fatal("all_invalid → 50")
	}
}

func TestPreScoreInvalidCIIsG09ThenAllInvalid(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 2500})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackAllInvalid {
		t.Fatalf("mode=%s invalidZones=%d", cs.FallbackMode, cs.InvalidZones)
	}
}

func TestPreScoreFutureTimestampInvalid(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now.Add(2*time.Hour), v1alpha1.QualityFresh, map[string]float64{"north": 100})
	snap.FetchedAt = v1alpha1.RFC3339Time{Time: now}
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 0)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1}))
	cs, _ := readCycleState(state)
	if cs.FallbackMode != v1alpha1.FallbackAllInvalid {
		t.Fatalf("FF-FUT should invalidate the only zone, got %s", cs.FallbackMode)
	}
}

func TestPreScoreCurrentCIMode(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	p.args.ScoringMode = v1alpha1.ScoringModeCurrentCI
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "west", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", ""), []fwk.NodeInfo{n1, n2}))
	if s1, s2 := lookup(t, p, state, n1), lookup(t, p, state, n2); s1 != 10000 || s2 != 0 {
		t.Fatalf("(2.23) n1=%d n2=%d, want 10000 and 0", s1, s2)
	}
}

func TestPreScoreEqualCFUsesNeutralSC(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100, "west": 100})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "west", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{n1, n2}))
	s1, s2 := lookup(t, p, state, n1), lookup(t, p, state, n2)
	if s1 != 5000 {
		t.Fatalf("14.4 n1=%d want 5000", s1)
	}
	if s2 != 4600 {
		t.Fatalf("14.4 n2=%d want 4600", s2)
	}
}

func TestScoreMissingCycleStateIsSuccessNeutral(t *testing.T) {
	p := &CarbonAwarePlugin{}
	score, st := p.Score(context.Background(), k8sframework.NewCycleState(), nil, testNode("n1", "north", 1000, 0))
	requireSuccess(t, st)
	if score != v1alpha1.NeutralFixedPointScore() {
		t.Fatalf("score=%d", score)
	}
}

func TestScoreNilStateIsSuccess(t *testing.T) {
	p := &CarbonAwarePlugin{}
	score, st := p.Score(context.Background(), nil, nil, nil)
	requireSuccess(t, st)
	if score != v1alpha1.NeutralFixedPointScore() {
		t.Fatalf("score=%d", score)
	}
}

func TestNormalizeScoreEqualValuesStayEqual(t *testing.T) {
	p := &CarbonAwarePlugin{}
	scores := fwk.NodeScoreList{{Name: "a", Score: 5000}, {Name: "b", Score: 5000}}
	requireSuccess(t, p.NormalizeScore(context.Background(), nil, nil, scores))
	if scores[0].Score != 50 || scores[1].Score != 50 {
		t.Fatalf("Max==Min must keep a tie on 50, got %v", scores)
	}
}

func TestScoreParallelLookups(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 100, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	n1 := testNode("n1", "north", 1000, 200)
	n2 := testNode("n2", "west", 1000, 400)
	state := k8sframework.NewCycleState()
	requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{n1, n2}))

	errCh := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			_, st := p.Score(context.Background(), state, nil, n1)
			if !st.IsSuccess() {
				errCh <- st.AsError()
				return
			}
			errCh <- nil
		}()
	}
	for i := 0; i < 16; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}
