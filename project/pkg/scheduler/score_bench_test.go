package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	fwk "k8s.io/kube-scheduler/framework"
	k8sframework "k8s.io/kubernetes/pkg/scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

const scoreLatencyBudget = 100 * time.Microsecond

func scoreBenchEnv(tb testing.TB, nodes int) (*CarbonAwarePlugin, fwk.CycleState, []fwk.NodeInfo) {
	tb.Helper()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	snap := snapWithCI(now, v1alpha1.QualityFresh, map[string]float64{"north": 80, "west": 400})
	p := testPlugin(v1alpha1.NewFakeClock(now), &snap)
	list := make([]fwk.NodeInfo, 0, nodes)
	for i := 0; i < nodes; i++ {
		zone := "north"
		if i%2 == 1 {
			zone = "west"
		}
		list = append(list, testNode(fmt.Sprintf("n-%02d", i), zone, 1000, 0))
	}
	state := k8sframework.NewCycleState()
	if st := p.PreScore(context.Background(), state, scoringPod(300, "7200s", "14400s"), list); !st.IsSuccess() {
		tb.Fatalf("PreScore: %v", st)
	}
	return p, state, list
}

// TestScoreLatencyBudget is the Gate 5 check: Score is O(1) lookup and must stay
// well below 100 µs per node on a synthetic CycleState (no HTTP, FakeClock).
func TestScoreLatencyBudget(t *testing.T) {
	p, state, nodes := scoreBenchEnv(t, 20)
	ni := nodes[0]
	const iters = 50000
	start := time.Now()
	for i := 0; i < iters; i++ {
		score, st := p.Score(context.Background(), state, nil, ni)
		if !st.IsSuccess() || score <= 0 {
			t.Fatalf("Score=%d status=%v", score, st)
		}
	}
	avg := time.Since(start) / iters
	t.Logf("Score average %s/node over %d lookups (budget %s)", avg, iters, scoreLatencyBudget)
	if avg > scoreLatencyBudget {
		t.Fatalf("Score average %s exceeds 100µs/node", avg)
	}
}

func BenchmarkScorePerNode(b *testing.B) {
	p, state, nodes := scoreBenchEnv(b, 20)
	ni := nodes[7]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, st := p.Score(context.Background(), state, nil, ni)
		if !st.IsSuccess() {
			b.Fatal(st)
		}
	}
}

func BenchmarkNormalizeScore20(b *testing.B) {
	p, state, nodes := scoreBenchEnv(b, 20)
	scores := make(fwk.NodeScoreList, len(nodes))
	for i, ni := range nodes {
		s, st := p.Score(context.Background(), state, nil, ni)
		if !st.IsSuccess() {
			b.Fatal(st)
		}
		scores[i] = fwk.NodeScore{Name: ni.Node().Name, Score: s}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if st := p.NormalizeScore(context.Background(), state, nil, scores); !st.IsSuccess() {
			b.Fatal(st)
		}
	}
}

func BenchmarkPreScore20(b *testing.B) {
	p, _, nodes := scoreBenchEnv(b, 20)
	pod := scoringPod(300, "7200s", "14400s")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		state := k8sframework.NewCycleState()
		if st := p.PreScore(context.Background(), state, pod, nodes); !st.IsSuccess() {
			b.Fatal(st)
		}
	}
}
