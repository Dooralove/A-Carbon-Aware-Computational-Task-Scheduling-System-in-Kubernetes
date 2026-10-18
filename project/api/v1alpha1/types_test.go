package v1alpha1

import (
	"strings"
	"testing"
	"time"
)

func TestParseDurationSecondsRejectsBareInteger(t *testing.T) {
	if _, err := ParseDurationSeconds("300"); err == nil {
		t.Fatal("bare integer must be rejected (ParseDuration would treat it as 300ns)")
	}
}

func TestParseDurationSecondsCanonicalAnnotations(t *testing.T) {
	got, err := ParseDurationSeconds("300s")
	if err != nil {
		t.Fatalf("300s: %v", err)
	}
	if got != 300 {
		t.Fatalf("300s: got %d", got)
	}
	got, err = ParseDurationSeconds("1800s")
	if err != nil {
		t.Fatalf("1800s: %v", err)
	}
	if got != 1800 {
		t.Fatalf("1800s: got %d", got)
	}
	got, err = ParseDurationSeconds("2h")
	if err != nil {
		t.Fatalf("2h: %v", err)
	}
	if got != 7200 {
		t.Fatalf("2h: got %d", got)
	}
}

func TestParseCarbonWeight(t *testing.T) {
	w, err := ParseCarbonWeight("0.7")
	if err != nil {
		t.Fatal(err)
	}
	if w != 0.7 {
		t.Fatalf("got %v", w)
	}
	if _, err := ParseCarbonWeight("0,7"); err == nil {
		t.Fatal("comma decimal must be rejected")
	}
	if _, err := ParseCarbonWeight("1.1"); err == nil {
		t.Fatal("weight > 1 must be rejected")
	}
}

func TestNewWorkloadProfileSurfaces(t *testing.T) {
	p, err := NewWorkloadProfile("ns", "job", "uid", map[string]string{
		LabelEnabled:       EnabledTrue,
		LabelWorkloadClass: string(WorkloadClassBatch),
	}, map[string]string{
		AnnotationExpectedDuration: "300s",
		AnnotationSLADeadline:      "1800s",
		AnnotationCarbonWeight:     "0.7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.Class != WorkloadClassBatch {
		t.Fatalf("profile: %+v", p)
	}
	if p.ExpectedDurationSeconds == nil || *p.ExpectedDurationSeconds != 300 {
		t.Fatalf("expected duration: %v", p.ExpectedDurationSeconds)
	}
	if p.SLADeadlineSeconds == nil || *p.SLADeadlineSeconds != 1800 {
		t.Fatalf("sla: %v", p.SLADeadlineSeconds)
	}
	if p.CarbonWeight == nil || *p.CarbonWeight != 0.7 {
		t.Fatalf("weight: %v", p.CarbonWeight)
	}

	_, err = NewWorkloadProfile("ns", "job", "uid", nil, map[string]string{
		LabelEnabled: EnabledTrue,
	})
	if err == nil || !strings.Contains(err.Error(), "must be a label") {
		t.Fatalf("enabled in annotations must fail, got %v", err)
	}
}

func TestCarbonRecordG10(t *testing.T) {
	ok := CarbonRecord{
		Zone:                       "north",
		Timestamp:                  RFC3339Time{Time: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)},
		CarbonIntensityGCO2ePerKWh: 82.4,
		Unit:                       IntensityUnitGCO2ePerKWh,
		EmissionFactorType:         EmissionLifecycle,
		Quality:                    QualityFresh,
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.CarbonIntensityGCO2ePerKWh = 2001
	if err := bad.Validate(); err == nil {
		t.Fatal("CI > 2000 must fail G-10")
	}
}

func TestNeutralFixedPointScore(t *testing.T) {
	if NeutralFixedPointScore() != 5000 {
		t.Fatalf("got %d", NeutralFixedPointScore())
	}
}
