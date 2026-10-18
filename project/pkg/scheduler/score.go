package scheduler

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/metrics"
)

// Score returns the fixed-point value prepared in PreScore (ARCH-05).
// O(1): CycleState lookup only. No snapshot.Load, no HTTP, no map allocation.
//
// P(u) and the CF integral are not computed here. PreScore.evaluateOne calls
// energy.LinearPowerModel.PredictPodMarginal and carbon.IntegrateExpectedFootprint
// once per candidate and stores s_fp in CycleState. Calling those functions from
// Score would violate ARCH-05 and the O(1) budget.
//
// Every branch returns Success (FF-INV-08). Missing key → Score_neutral=50.
func (p *CarbonAwarePlugin) Score(_ context.Context, state fwk.CycleState, _ *v1.Pod, nodeInfo fwk.NodeInfo) (int64, *fwk.Status) {
	start := time.Now()
	defer func() { metrics.ObservePluginDuration(metrics.PointScore, time.Since(start)) }()
	payload, err := readCycleState(state)
	if err != nil || payload == nil {
		return v1alpha1.NeutralFixedPointScore(), statusSuccess()
	}
	name := nodeName(nodeInfo)
	score, ok := payload.ScorePerNode[name]
	if !ok {
		return v1alpha1.NeutralFixedPointScore(), statusSuccess()
	}
	return score, statusSuccess()
}

// ScoreExtensions returns the NormalizeScore implementation.
func (p *CarbonAwarePlugin) ScoreExtensions() fwk.ScoreExtensions {
	return p
}
