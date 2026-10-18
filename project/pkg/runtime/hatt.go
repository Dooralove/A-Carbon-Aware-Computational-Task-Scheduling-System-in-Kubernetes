// Package runtime resolves expected Pod runtime T̂ from class tables (2.12, G-20).
// Pure: no I/O (GO-13, GO-19).
package runtime

import (
	"fmt"

	"carbon-aware-k8s/api/v1alpha1"
)

// ResolveHatTSeconds returns T̂ in seconds.
//
// Order (API §6.3): explicit expected-duration; else runtimeClassSeconds[class];
// else runtimeClassSeconds[defaultClass]; else G-20.
func ResolveHatTSeconds(profile v1alpha1.WorkloadProfile, classSeconds map[v1alpha1.WorkloadClass]int64, defaultClass v1alpha1.WorkloadClass) (int64, error) {
	if profile.ExpectedDurationSeconds != nil {
		if *profile.ExpectedDurationSeconds < 0 {
			return 0, fmt.Errorf("expected duration is negative: %w", v1alpha1.ErrSLAPrecondition)
		}
		return *profile.ExpectedDurationSeconds, nil
	}
	if profile.Class.IsKnown() {
		if sec, ok := classSeconds[profile.Class]; ok {
			if sec < 0 {
				return 0, fmt.Errorf("runtime class %s is negative: %w", profile.Class, v1alpha1.ErrSLAPrecondition)
			}
			return sec, nil
		}
	}
	if defaultClass.IsKnown() {
		if sec, ok := classSeconds[defaultClass]; ok {
			if sec < 0 {
				return 0, fmt.Errorf("default runtime class is negative: %w", v1alpha1.ErrSLAPrecondition)
			}
			return sec, nil
		}
	}
	return 0, fmt.Errorf("workload horizon is undefined: %w", v1alpha1.ErrSLAPrecondition)
}

// HardSLAViolated is (2.13): T̂ > T_deadline when a finite deadline is set (G-12).
func HardSLAViolated(hatTSeconds int64, profile v1alpha1.WorkloadProfile) bool {
	if !profile.HasFiniteSLA() {
		return false
	}
	return hatTSeconds > *profile.SLADeadlineSeconds
}
