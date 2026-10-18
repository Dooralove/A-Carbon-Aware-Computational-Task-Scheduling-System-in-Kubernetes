// Package energy implements the affine node power model (2.2)–(2.4).
// Functions are pure: no I/O, no scheduler types (GO-13, GO-19).
package energy

import (
	"fmt"
	"math"

	"carbon-aware-k8s/api/v1alpha1"
)

// AffineModel is the runtime-canon P(u) with γ = 1.
type AffineModel struct{}

var _ v1alpha1.EnergyModel = AffineModel{}

// Clip implements clip(x, lo, hi). NaN yields lo (G-21 must not panic).
func Clip(x, lo, hi float64) float64 {
	if math.IsNaN(x) {
		return lo
	}
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// CPUUtilization is (2.3): requested/allocatable clipped to [0, 1].
// G-02: allocatable == 0 → 1 (no division by zero).
func CPUUtilization(requestedMillicores, allocatableMillicores int64) float64 {
	if allocatableMillicores <= 0 {
		return 1
	}
	return Clip(float64(requestedMillicores)/float64(allocatableMillicores), 0, 1)
}

// MemoryUtilization is (2.3a). G-02: allocatable == 0 → 1.
func MemoryUtilization(requestedBytes, allocatableBytes int64) float64 {
	if allocatableBytes <= 0 {
		return 1
	}
	return Clip(float64(requestedBytes)/float64(allocatableBytes), 0, 1)
}

// NodePowerWatts is (2.2): P(u) = P_idle + (P_max - P_idle) * clip(u, 0, 1).
func NodePowerWatts(idleWatts, maxWatts, utilization float64) float64 {
	return idleWatts + (maxWatts-idleWatts)*Clip(utilization, 0, 1)
}

// PodMarginalPowerWatts is (2.4) IDLE-NONE: P(u+) - P(u-).
func PodMarginalPowerWatts(idleWatts, maxWatts, utilizationBefore, utilizationAfter float64) float64 {
	return NodePowerWatts(idleWatts, maxWatts, utilizationAfter) - NodePowerWatts(idleWatts, maxWatts, utilizationBefore)
}

// NodePowerWatts implements v1alpha1.EnergyModel.
func (AffineModel) NodePowerWatts(profile v1alpha1.NodeEnergyProfile, utilization float64) (float64, error) {
	if err := profile.Validate(); err != nil {
		return 0, err
	}
	p := LinearPowerModel{PowerIdleWatts: profile.PowerIdleWatts, PowerMaxWatts: profile.PowerMaxWatts}.PredictNodePower(utilization)
	return p, nil
}

// PodMarginalPowerWatts implements v1alpha1.EnergyModel.
func (AffineModel) PodMarginalPowerWatts(profile v1alpha1.NodeEnergyProfile, utilizationBefore, utilizationAfter float64) (float64, error) {
	if err := profile.Validate(); err != nil {
		return 0, err
	}
	p := LinearPowerModel{PowerIdleWatts: profile.PowerIdleWatts, PowerMaxWatts: profile.PowerMaxWatts}.
		PredictPodMarginal(utilizationBefore, utilizationAfter)
	if p < 0 {
		return 0, fmt.Errorf("marginal pod power is negative: %w", v1alpha1.ErrInvalidPluginArgs)
	}
	return p, nil
}

// NodeUtilization is (2.16): U_node = clip(λ u+ + (1-λ) v+, 0, 1).
func NodeUtilization(lambda, cpuAfter, memAfter float64) float64 {
	if math.IsNaN(lambda) || lambda < 0 {
		lambda = 1
	}
	if lambda > 1 {
		lambda = 1
	}
	return Clip(lambda*cpuAfter+(1-lambda)*memAfter, 0, 1)
}

// ResourceScore is (2.17): S_R = 1 - U_node.
func ResourceScore(lambda, cpuAfter, memAfter float64) float64 {
	return 1 - NodeUtilization(lambda, cpuAfter, memAfter)
}
