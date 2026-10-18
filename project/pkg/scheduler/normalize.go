package scheduler

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"

	pkgcarbon "carbon-aware-k8s/pkg/carbon"
	"carbon-aware-k8s/pkg/metrics"
)

// NormalizeScore maps fixed-point s_fp onto the framework scale [0, 100] (2.21).
// Min-max of S_C is already applied in PreScore (G-04). Equal scores (Max==Min
// after rounding) stay equal — the framework tie is the correct outcome.
func (p *CarbonAwarePlugin) NormalizeScore(_ context.Context, _ fwk.CycleState, _ *v1.Pod, scores fwk.NodeScoreList) *fwk.Status {
	start := time.Now()
	defer func() { metrics.ObservePluginDuration(metrics.PointNormalizeScore, time.Since(start)) }()
	for i := range scores {
		scores[i].Score = pkgcarbon.FrameworkScore(scores[i].Score)
	}
	return statusSuccess()
}
