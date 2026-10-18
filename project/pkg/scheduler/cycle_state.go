package scheduler

import (
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

// cycleStateKey is the CycleState key written in PreScore and read in Score (API-STATE-01).
const cycleStateKey fwk.StateKey = v1alpha1.CarbonCycleStateKey

// cycleStateData wraps the immutable PreScore payload so it satisfies fwk.StateData.
// api/v1alpha1 must not import the scheduler framework (ARCH-14, GO-19).
type cycleStateData struct {
	payload *v1alpha1.CarbonCycleState
}

// Clone implements fwk.StateData. The payload is immutable after Write, so identity clone is race-free.
func (d *cycleStateData) Clone() fwk.StateData {
	return d
}

func writeCycleState(state fwk.CycleState, payload *v1alpha1.CarbonCycleState) {
	if state == nil {
		return
	}
	state.Write(cycleStateKey, &cycleStateData{payload: payload})
}

func readCycleState(state fwk.CycleState) (*v1alpha1.CarbonCycleState, error) {
	if state == nil {
		return nil, fwk.ErrNotFound
	}
	raw, err := state.Read(cycleStateKey)
	if err != nil {
		return nil, err
	}
	data, ok := raw.(*cycleStateData)
	if !ok || data == nil || data.payload == nil {
		return nil, fwk.ErrNotFound
	}
	return data.payload, nil
}

func nodeName(nodeInfo fwk.NodeInfo) string {
	if nodeInfo == nil {
		return ""
	}
	node := nodeInfo.Node()
	if node == nil {
		return ""
	}
	return node.Name
}
