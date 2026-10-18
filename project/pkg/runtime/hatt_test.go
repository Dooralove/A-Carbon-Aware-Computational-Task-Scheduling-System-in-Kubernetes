package runtime

import (
	"errors"
	"testing"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestResolveHatTPrefersAnnotation(t *testing.T) {
	sec := int64(300)
	got, err := ResolveHatTSeconds(v1alpha1.WorkloadProfile{
		ExpectedDurationSeconds: &sec,
		Class:                   v1alpha1.WorkloadClassBatch,
	}, map[v1alpha1.WorkloadClass]int64{v1alpha1.WorkloadClassBatch: 7200}, v1alpha1.WorkloadClassBatch)
	if err != nil || got != 300 {
		t.Fatalf("got %d %v, want 300", got, err)
	}
}

func TestResolveHatTDefaultClass(t *testing.T) {
	got, err := ResolveHatTSeconds(v1alpha1.WorkloadProfile{}, map[v1alpha1.WorkloadClass]int64{
		v1alpha1.WorkloadClassBatch: 1800,
	}, v1alpha1.WorkloadClassBatch)
	if err != nil || got != 1800 {
		t.Fatalf("got %d %v", got, err)
	}
}

func TestResolveHatTG20(t *testing.T) {
	_, err := ResolveHatTSeconds(v1alpha1.WorkloadProfile{Enabled: true}, nil, "")
	if !errors.Is(err, v1alpha1.ErrSLAPrecondition) {
		t.Fatalf("want G-20, got %v", err)
	}
}

func TestHardSLA(t *testing.T) {
	deadline := int64(1200)
	if !HardSLAViolated(1800, v1alpha1.WorkloadProfile{SLADeadlineSeconds: &deadline}) {
		t.Fatal("14.6 must violate")
	}
	ok := int64(3600)
	if HardSLAViolated(1800, v1alpha1.WorkloadProfile{SLADeadlineSeconds: &ok}) {
		t.Fatal("3600s deadline must pass")
	}
	if HardSLAViolated(1800, v1alpha1.WorkloadProfile{}) {
		t.Fatal("G-13 infinite deadline")
	}
}
