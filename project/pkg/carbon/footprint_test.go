package carbon

import (
	"math"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestExpectedFootprintOracle14_2(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	rec := v1alpha1.CarbonRecord{
		Zone:                       "north",
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: 100,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		Quality:                    v1alpha1.QualityFresh,
	}
	cf, err := ExpectedFootprint(now, 18, 7200, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(cf-3.6) > 1e-9 {
		t.Fatalf("CF=%v, want 3.6", cf)
	}
}

func TestExpectedFootprintOracle14_7(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	rec := v1alpha1.CarbonRecord{
		Zone:                       "north",
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: 100,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		Quality:                    v1alpha1.QualityFresh,
	}
	forecast := []v1alpha1.ForecastInterval{
		{
			Timestamp:                  v1alpha1.RFC3339Time{Time: now},
			DurationSeconds:            3600,
			CarbonIntensityGCO2ePerKWh: 100,
			Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		},
		{
			Timestamp:                  v1alpha1.RFC3339Time{Time: now.Add(time.Hour)},
			DurationSeconds:            3600,
			CarbonIntensityGCO2ePerKWh: 400,
			Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		},
	}
	cf, err := ExpectedFootprint(now, 18, 7200, rec, forecast)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(cf-9.0) > 1e-9 {
		t.Fatalf("CF=%v, want 9.0", cf)
	}
}

func TestCarbonScoreOracle14_3(t *testing.T) {
	s1 := CarbonScore(3.6, 3.6, 14.4, DefaultEpsilonCFGCO2e)
	s2 := CarbonScore(14.4, 3.6, 14.4, DefaultEpsilonCFGCO2e)
	if s1 != 1 || s2 != 0 {
		t.Fatalf("S_C=%v,%v want 1,0", s1, s2)
	}
	if CarbonScore(3.6, 3.6, 3.6, DefaultEpsilonCFGCO2e) != NeutralScore {
		t.Fatal("G-04 must yield 1/2")
	}
}

func TestWeightedScoreOracle14_3(t *testing.T) {
	if got := WeightedScore(0.6, 0.2, 0.2, 1, 0.5, 0.5); got != 80 {
		t.Fatalf("Score(n1)=%v, want 80", got)
	}
	if got := WeightedScore(0.6, 0.2, 0.2, 0, 0.3, 0.5); got != 16 {
		t.Fatalf("Score(n2)=%v, want 16", got)
	}
}

func TestPerformanceScore(t *testing.T) {
	if PerformanceScore(1800, 0, false) != 1 {
		t.Fatal("G-13")
	}
	if PerformanceScore(0, 0, true) != 1 {
		t.Fatal("G-14")
	}
	if PerformanceScore(1800, 3600, true) != 0.5 {
		t.Fatal("14.6 contrast S_P=0.5")
	}
}
