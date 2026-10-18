package unit_test

// CarbonAware scoring suite (T5.5 / DOD-TEST-07).
//
// Framework doubles:
//   - Handle  = framework/runtime.NewFramework (the in-tree FakeHandle)
//   - CycleState = k8sframework.NewCycleState
//   - NodeInfo   = k8sframework.NewNodeInfo + SetNode / AddPod
//
// Snapshot is seeded with SeedSnapshot (no HTTP, FakeClock — DOD-TEST-01).

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"
	fwruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/scheduler"
)

const (
	testNowYear  = 2026
	testNowMonth = time.September
	testNowDay   = 4
)

func testNow() time.Time {
	return time.Date(testNowYear, testNowMonth, testNowDay, 12, 0, 0, 0, time.UTC)
}

func validScoreArgs() scheduler.PluginArgs {
	return scheduler.PluginArgs{
		ScoringMode:            v1alpha1.ScoringModeExpectedCF,
		CarbonWeight:           0.6,
		ResourceWeight:         0.2,
		PerformanceWeight:      0.2,
		Lambda:                 1,
		PowerIdleWatts:         40,
		PowerMaxWatts:          100,
		MaxDataAgeSeconds:      300,
		StaleWarnSeconds:       90,
		ClockSkewSeconds:       60,
		RefreshIntervalSeconds: 30,
		HTTPTimeoutSeconds:     2,
		CarbonServiceURL:       "http://carbon-data-service.carbon-aware.svc:8080",
	}
}

func newFakeHandle(t *testing.T) fwk.Handle {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fh, err := fwruntime.NewFramework(ctx, nil, nil)
	if err != nil {
		t.Fatalf("FakeHandle (NewFramework): %v", err)
	}
	return fh
}

func newPlugin(t *testing.T, args scheduler.PluginArgs, clock v1alpha1.Clock, snap *v1alpha1.CarbonSnapshot) *scheduler.CarbonAwarePlugin {
	t.Helper()
	handle := newFakeHandle(t)
	p, err := scheduler.NewForTests(args, clock, handle)
	if err != nil {
		t.Fatalf("NewForTests: %v", err)
	}
	if snap != nil {
		p.SeedSnapshot(*snap)
	}
	return p
}

func scoringPod(cpuMilli int64, duration, deadline string) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job",
			Namespace: "default",
			UID:       types.UID("pod-score-1"),
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

func requireSuccess(t *testing.T, st *fwk.Status) {
	t.Helper()
	if !st.IsSuccess() {
		t.Fatalf("want Success, got %v", st)
	}
}

func requireSLAError(t *testing.T, st *fwk.Status) {
	t.Helper()
	if st == nil || !st.IsError() {
		t.Fatalf("want PreScore Error, got %v", st)
	}
	if st.Message() != v1alpha1.SLAPreconditionMessage {
		t.Fatalf("message=%q, want %q", st.Message(), v1alpha1.SLAPreconditionMessage)
	}
}

func scoreOf(t *testing.T, p *scheduler.CarbonAwarePlugin, state fwk.CycleState, ni fwk.NodeInfo) int64 {
	t.Helper()
	score, st := p.Score(context.Background(), state, nil, ni)
	requireSuccess(t, st)
	return score
}

func northWestFresh(t *testing.T, now time.Time) (*scheduler.CarbonAwarePlugin, fwk.NodeInfo, fwk.NodeInfo) {
	t.Helper()
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
	p := newPlugin(t, validScoreArgs(), v1alpha1.NewFakeClock(now), &snap)
	return p, testNode("n-north", "north", 1000, 0), testNode("n-west", "west", 1000, 0)
}

// Scenario 1: equal resources, Zone North CI=80 vs Zone West CI=400.
//
// Hand oracle (IDLE-NONE, η=1/3.6e6, λ=1, Ť=7200 s, T_d=14400 s, R_p=300 mCPU):
//
//	u+=0.3, S_R=0.7, P_pod=18 W, S_P=0.5
//	CF_north=18·7200·80/3.6e6=2.88, CF_west=14.4 → S_C=1 / 0
//	Score=100(0.6 S_C + 0.2·0.7 + 0.2·0.5) → 84 / 24
func TestScenario1PreferCleanerZoneEqualResources(t *testing.T) {
	now := testNow()
	p, north, west := northWestFresh(t, now)
	state := k8sframework.NewCycleState()
	pod := scoringPod(300, "7200s", "14400s")
	requireSuccess(t, p.PreScore(context.Background(), state, pod, []fwk.NodeInfo{north, west}))

	sN, sW := scoreOf(t, p, state, north), scoreOf(t, p, state, west)
	if sN != 8400 || sW != 2400 {
		t.Fatalf("fixed-point north=%d west=%d, want 8400 and 2400 (Score 84 vs 24)", sN, sW)
	}
	if sN <= sW {
		t.Fatal("equal resources: cleaner North (CI=80) must rank above West (CI=400)")
	}

	scores := fwk.NodeScoreList{{Name: "n-north", Score: sN}, {Name: "n-west", Score: sW}}
	requireSuccess(t, p.NormalizeScore(context.Background(), state, pod, scores))
	if scores[0].Score != 84 || scores[1].Score != 24 {
		t.Fatalf("framework scores=%v, want 84 and 24", scores)
	}
}

// Scenario 2: Anti-Thrashing — at critical load S_R can outrank S_C.
//
// Default simplex (0.6, 0.2, 0.2) still prefers the full green node (70 vs 28).
// A valid resource-first simplex (0.2, 0.6, 0.2) is used so the empty dirty
// node wins when the clean node is at u+=1 (G-18 still holds).
//
//	north: CI=80, R_req=900 m, R_p=100 m, alloc=1000 m → u+=1, S_R=0, P_pod=6 W, CF=0.96, S_C=1
//	west:  CI=400, empty, R_p=100 m → u+=0.1, S_R=0.9, P_pod=6 W, CF=4.8, S_C=0
//	S_P=0.5; Score_north=30, Score_west=64
func TestScenario2ResourcePriorityAtCriticalLoad(t *testing.T) {
	now := testNow()
	args := validScoreArgs()
	args.CarbonWeight = 0.2
	args.ResourceWeight = 0.6
	args.PerformanceWeight = 0.2
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
	p := newPlugin(t, args, v1alpha1.NewFakeClock(now), &snap)

	north := testNode("n-north", "north", 1000, 900)
	west := testNode("n-west", "west", 1000, 0)
	state := k8sframework.NewCycleState()
	pod := scoringPod(100, "7200s", "14400s")
	requireSuccess(t, p.PreScore(context.Background(), state, pod, []fwk.NodeInfo{north, west}))

	sN, sW := scoreOf(t, p, state, north), scoreOf(t, p, state, west)
	if sN != 3000 || sW != 6400 {
		t.Fatalf("fixed-point north=%d west=%d, want 3000 and 6400 (Score 30 vs 64)", sN, sW)
	}
	if sW <= sN {
		t.Fatal("critical load on the clean node: S_R must outrank S_C under resource-first weights")
	}
}

// Scenario 3: Ť > T_deadline → PreScore Error (ARCH-10). Framework does not call Score.
func TestScenario3SLACutsNode(t *testing.T) {
	now := testNow()
	p, north, west := northWestFresh(t, now)
	state := k8sframework.NewCycleState()
	pod := scoringPod(300, "1800s", "1200s")
	requireSLAError(t, p.PreScore(context.Background(), state, pod, []fwk.NodeInfo{north, west}))
	if ext := p.ScoreExtensions(); ext == nil {
		t.Fatal("ScoreExtensions must return the plugin (NormalizeScore)")
	}
}

// Scenario 4: SM-STALE (stale-in-window) is full (2.19), not Score=50 (FF-INV-07).
// Dead cache (age > maxDataAge) is the G-08 branch and is checked as a sibling.
func TestScenario4StaleCacheFallback(t *testing.T) {
	t.Run("stale_in_window_full_score", func(t *testing.T) {
		fetched := testNow()
		clock := v1alpha1.NewFakeClock(fetched.Add(120 * time.Second))
		snap := snapWithCI(fetched, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
		p := newPlugin(t, validScoreArgs(), clock, &snap)
		north := testNode("n-north", "north", 1000, 0)
		west := testNode("n-west", "west", 1000, 0)
		state := k8sframework.NewCycleState()
		requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{north, west}))

		sN, sW := scoreOf(t, p, state, north), scoreOf(t, p, state, west)
		neutral := v1alpha1.NeutralFixedPointScore()
		if sN == neutral && sW == neutral {
			t.Fatal("SM-STALE must not replace Score with 50")
		}
		if sN <= sW {
			t.Fatal("stale-in-window: North must still rank above West")
		}
		if sN != 8400 || sW != 2400 {
			t.Fatalf("SM-STALE must keep the fresh ranking, got north=%d west=%d", sN, sW)
		}
	})

	t.Run("dead_cache_g08_neutral", func(t *testing.T) {
		fetched := testNow()
		clock := v1alpha1.NewFakeClock(fetched.Add(301 * time.Second))
		snap := snapWithCI(fetched, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
		p := newPlugin(t, validScoreArgs(), clock, &snap)
		north := testNode("n-north", "north", 1000, 0)
		west := testNode("n-west", "west", 1000, 0)
		state := k8sframework.NewCycleState()
		requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{north, west}))

		neutral := v1alpha1.NeutralFixedPointScore()
		if scoreOf(t, p, state, north) != neutral || scoreOf(t, p, state, west) != neutral {
			t.Fatal("SM-DEAD / G-08: every node Score_neutral=50")
		}
	})
}

// Scenario 5: missing carbon-aware.io/zone — G-09 (mixed) or G-08 all_invalid (all unknown).
func TestScenario5MissingCarbonLabels(t *testing.T) {
	now := testNow()
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})

	t.Run("all_nodes_unlabeled_g08", func(t *testing.T) {
		p := newPlugin(t, validScoreArgs(), v1alpha1.NewFakeClock(now), &snap)
		a := testNode("n-a", "", 1000, 0)
		b := testNode("n-b", "", 1000, 0)
		state := k8sframework.NewCycleState()
		requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{a, b}))
		neutral := v1alpha1.NeutralFixedPointScore()
		if scoreOf(t, p, state, a) != neutral || scoreOf(t, p, state, b) != neutral {
			t.Fatal("all unknown zones → G-08 Score=50, Success")
		}
	})

	t.Run("mixed_unlabeled_g09_stays_candidate", func(t *testing.T) {
		p := newPlugin(t, validScoreArgs(), v1alpha1.NewFakeClock(now), &snap)
		labeled := testNode("n-north", "north", 1000, 0)
		unknown := testNode("n-bare", "", 1000, 400)
		state := k8sframework.NewCycleState()
		requireSuccess(t, p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), []fwk.NodeInfo{labeled, unknown}))

		sU := scoreOf(t, p, state, unknown)
		if sU == 0 {
			t.Fatal("G-09 must not use Score=0 as a Filter")
		}
		_, st := p.Score(context.Background(), state, nil, unknown)
		requireSuccess(t, st)
		if scoreOf(t, p, state, labeled) <= sU {
			t.Fatal("labeled North must still rank above the unlabeled node (S_C=1/2)")
		}
	})
}

func TestNormalizeScoreClusterSizes(t *testing.T) {
	for _, n := range []int{3, 5, 20} {
		t.Run(fmt.Sprintf("nodes_%d", n), func(t *testing.T) {
			now := testNow()
			snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
			p := newPlugin(t, validScoreArgs(), v1alpha1.NewFakeClock(now), &snap)

			nodes := make([]fwk.NodeInfo, 0, n)
			var norths, wests []fwk.NodeInfo
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("n-%02d", i)
				zone := "north"
				if i%2 == 1 {
					zone = "west"
				}
				ni := testNode(name, zone, 1000, 0)
				nodes = append(nodes, ni)
				if zone == "north" {
					norths = append(norths, ni)
				} else {
					wests = append(wests, ni)
				}
			}

			state := k8sframework.NewCycleState()
			pod := scoringPod(300, "7200s", "14400s")
			requireSuccess(t, p.PreScore(context.Background(), state, pod, nodes))

			list := make(fwk.NodeScoreList, 0, n)
			for _, ni := range nodes {
				list = append(list, fwk.NodeScore{Name: ni.Node().Name, Score: scoreOf(t, p, state, ni)})
			}
			requireSuccess(t, p.NormalizeScore(context.Background(), state, pod, list))

			northScore, westScore := int64(-1), int64(-1)
			for i, ni := range nodes {
				got := list[i].Score
				if got < 0 || got > 100 {
					t.Fatalf("node %s framework score %d is outside [0,100]", list[i].Name, got)
				}
				if ni.Node().Labels[v1alpha1.LabelZone] == "north" {
					if northScore < 0 {
						northScore = got
					} else if got != northScore {
						t.Fatalf("equal-resource North nodes must stay tied, %d vs %d", northScore, got)
					}
				} else {
					if westScore < 0 {
						westScore = got
					} else if got != westScore {
						t.Fatalf("equal-resource West nodes must stay tied, %d vs %d", westScore, got)
					}
				}
			}
			if len(norths) > 0 && len(wests) > 0 && northScore <= westScore {
				t.Fatalf("cluster size %d: North=%d must beat West=%d after NormalizeScore", n, northScore, westScore)
			}
		})
	}
}

func TestNormalizeScoreSyntheticBounds(t *testing.T) {
	p := newPlugin(t, validScoreArgs(), v1alpha1.NewFakeClock(testNow()), nil)
	cases := []fwk.NodeScoreList{
		{{Name: "a", Score: 0}, {Name: "b", Score: 5000}, {Name: "c", Score: 10000}},
		{{Name: "a", Score: 5000}, {Name: "b", Score: 5000}, {Name: "c", Score: 5000}, {Name: "d", Score: 5000}, {Name: "e", Score: 5000}},
		make(fwk.NodeScoreList, 20),
	}
	for i := range cases[2] {
		cases[2][i] = fwk.NodeScore{Name: fmt.Sprintf("n-%02d", i), Score: int64((i % 11) * 1000)}
	}
	for _, scores := range cases {
		n := len(scores)
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			requireSuccess(t, p.NormalizeScore(context.Background(), nil, nil, scores))
			for _, s := range scores {
				if s.Score < 0 || s.Score > 100 {
					t.Fatalf("%s=%d outside [0,100]", s.Name, s.Score)
				}
			}
			if n == 5 {
				for _, s := range scores {
					if s.Score != v1alpha1.NeutralPluginScore {
						t.Fatalf("tie of 5000 must map to 50, got %v", scores)
					}
				}
			}
		})
	}
}
