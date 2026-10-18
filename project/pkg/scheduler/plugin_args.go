package scheduler

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"carbon-aware-k8s/api/v1alpha1"
)

// PluginArgs is the KubeSchedulerConfiguration pluginConfig.args payload for CarbonAware.
// Field names, units and Validate() are the canonical API type (API §4.7, GO-12, G-03, G-18).
type PluginArgs = v1alpha1.CarbonAwarePluginArgs

// decodePluginArgs unmarshals pluginConfig.args from the Scheduler Framework *runtime.Unknown
// envelope. A nil object yields zero-value args; the caller must still Validate (fail-closed).
func decodePluginArgs(obj runtime.Object) (*PluginArgs, error) {
	args := &PluginArgs{}
	if err := fwkruntime.DecodeInto(obj, args); err != nil {
		return nil, fmt.Errorf("decode CarbonAware plugin args: %w", err)
	}
	return args, nil
}
