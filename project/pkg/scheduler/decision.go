package scheduler

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/metrics"
)

// decisionStateKey holds per-node estimates for the PostBind decision metrics
// (ADR-003). It is separate from cycleStateKey so the frozen CarbonCycleState
// DTO (API-STATE-01) does not change.
const decisionStateKey fwk.StateKey = "CarbonAware/decision"

// secondsPerHour and wattsPerKilowatt convert P_pod [W] * T_hat [s] to kWh.
const (
	secondsPerHour   = 3600.0
	wattsPerKilowatt = 1000.0
)

// nodeEstimate is what PreScore knew about one candidate node.
type nodeEstimate struct {
	zone        string
	hasEstimate bool
	carbonGCO2e float64
	energyKWh   float64
}

// decisionState is immutable after Write (Clone returns the receiver).
type decisionState struct {
	scoringMode string
	nodes       map[string]nodeEstimate
}

func (d *decisionState) Clone() fwk.StateData {
	return d
}

func energyKWh(podWatts float64, hatTSeconds int64) float64 {
	return podWatts * float64(hatTSeconds) / secondsPerHour / wattsPerKilowatt
}

func nodeZone(ni fwk.NodeInfo) string {
	if ni == nil || ni.Node() == nil {
		return ""
	}
	n := ni.Node()
	return string(v1alpha1.ParseNodeEnergyIdentity(n.Name, n.Labels).Zone)
}

// writeDecisionZones stores zones only (G-08, opt-out): decisions are counted
// but carry no CF estimate.
func writeDecisionZones(state fwk.CycleState, nodes []fwk.NodeInfo, mode v1alpha1.ScoringMode) {
	if state == nil {
		return
	}
	est := make(map[string]nodeEstimate, len(nodes))
	for _, ni := range nodes {
		if name := nodeName(ni); name != "" {
			est[name] = nodeEstimate{zone: nodeZone(ni)}
		}
	}
	state.Write(decisionStateKey, &decisionState{scoringMode: string(mode), nodes: est})
}

func writeDecisionWorks(state fwk.CycleState, works []nodeWork, mode v1alpha1.ScoringMode) {
	if state == nil {
		return
	}
	est := make(map[string]nodeEstimate, len(works))
	currentCI := mode == v1alpha1.ScoringModeCurrentCI
	for _, w := range works {
		e := nodeEstimate{zone: w.zone}
		if w.mark == markOK && !currentCI {
			e.hasEstimate = true
			e.carbonGCO2e = w.cfGCO2e
			e.energyKWh = w.energyKWh
		}
		est[w.name] = e
	}
	state.Write(decisionStateKey, &decisionState{scoringMode: string(mode), nodes: est})
}

func readDecisionState(state fwk.CycleState) *decisionState {
	if state == nil {
		return nil
	}
	raw, err := state.Read(decisionStateKey)
	if err != nil {
		return nil
	}
	d, _ := raw.(*decisionState)
	return d
}

// PostBind records carbon_aware_scheduler_decisions_total and the selected
// node's estimates. It runs after a successful Bind, so the counter equals the
// number of Pods this profile placed. Informational only: no Status, no I/O.
func (p *CarbonAwarePlugin) PostBind(_ context.Context, state fwk.CycleState, _ *v1.Pod, node string) {
	start := time.Now()
	defer func() { metrics.ObservePluginDuration(metrics.PointPostBind, time.Since(start)) }()

	d := metrics.Decision{Node: node, ScoringMode: string(p.args.ScoringMode)}
	if ds := readDecisionState(state); ds != nil {
		if e, ok := ds.nodes[node]; ok {
			d.Zone = e.zone
			d.HasEstimate = e.hasEstimate
			d.CarbonGCO2e = e.carbonGCO2e
			d.EnergyKWh = e.energyKWh
		}
	}
	if d.Zone == "" {
		// PreScore is skipped when only one node is feasible; read the zone
		// from the shared Node informer instead.
		d.Zone = p.zoneFromInformer(node)
	}
	metrics.RecordDecision(d)
}

func (p *CarbonAwarePlugin) zoneFromInformer(node string) string {
	if p == nil || p.handle == nil || node == "" {
		return ""
	}
	factory := p.handle.SharedInformerFactory()
	if factory == nil {
		return ""
	}
	n, err := factory.Core().V1().Nodes().Lister().Get(node)
	if err != nil || n == nil {
		return ""
	}
	return string(v1alpha1.ParseNodeEnergyIdentity(n.Name, n.Labels).Zone)
}
