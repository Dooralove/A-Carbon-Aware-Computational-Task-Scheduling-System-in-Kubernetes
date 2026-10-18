package energy

// PowerModel is the runtime power envelope used by the carbon integrator.
type PowerModel interface {
	PredictNodePower(utilization float64) float64
}

// LinearPowerModel is (2.2) with γ = 1: P(u) = P_idle + (P_max - P_idle) * clip(u, 0, 1).
// Clip is required by SSOT even though the informal assignment wrote a bare u.
type LinearPowerModel struct {
	PowerIdleWatts float64
	PowerMaxWatts  float64
}

var _ PowerModel = LinearPowerModel{}

// PredictNodePower is (2.2). Utilization outside [0, 1] is clipped (G-01).
func (m LinearPowerModel) PredictNodePower(utilization float64) float64 {
	return NodePowerWatts(m.PowerIdleWatts, m.PowerMaxWatts, utilization)
}

// PredictPodMarginal is (2.4) IDLE-NONE: P(u+) - P(u-).
func (m LinearPowerModel) PredictPodMarginal(utilizationBefore, utilizationAfter float64) float64 {
	return PodMarginalPowerWatts(m.PowerIdleWatts, m.PowerMaxWatts, utilizationBefore, utilizationAfter)
}

// PredictPodAttribution is the Kepler-style CPU-share of dynamic power (2.4a)
// for a Pod placed on an otherwise empty node:
//
//	(P_max - P_idle) * clip(R_p / R_alloc, 0, 1)
//
// nodePower is P_max (P(1)); idlePower is P_idle. Zero nodePower and idlePower
// fall back to the struct fields. Existing node load must use PredictPodMarginal.
func (m LinearPowerModel) PredictPodAttribution(podRequestCPU, totalAllocatableCPU, nodePower, idlePower float64) float64 {
	pMax := nodePower
	pIdle := idlePower
	if pMax == 0 && pIdle == 0 {
		pMax = m.PowerMaxWatts
		pIdle = m.PowerIdleWatts
	}
	share := 1.0
	if totalAllocatableCPU > 0 {
		share = Clip(podRequestCPU/totalAllocatableCPU, 0, 1)
	}
	p := (pMax - pIdle) * share
	if p < 0 {
		return 0
	}
	return p
}

// ConstantPower is a P_pod envelope (2.4b): power is independent of utilization.
type ConstantPower struct {
	Watts float64
}

var _ PowerModel = ConstantPower{}

// PredictNodePower ignores utilization and returns the constant watts.
func (c ConstantPower) PredictNodePower(float64) float64 {
	return c.Watts
}
