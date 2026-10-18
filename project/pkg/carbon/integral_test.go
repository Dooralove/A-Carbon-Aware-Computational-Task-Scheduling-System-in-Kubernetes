package carbon

import (
	"math"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/energy"
)

const oracleTol = 1e-12 // T2.2 math.isclose abs_tol; relative error is 0 at this scale

func almostEqual(t *testing.T, got, want float64, note string) {
	t.Helper()
	if math.Abs(got-want) > oracleTol {
		rel := 0.0
		if want != 0 {
			rel = math.Abs(got-want) / math.Abs(want)
		}
		t.Fatalf("%s: got %.16g want %.16g (abs=%g rel=%.3g%%) — %s",
			t.Name(), got, want, math.Abs(got-want), rel*100, note)
	}
}

func recordAt(now time.Time, ci float64) v1alpha1.CarbonRecord {
	return v1alpha1.CarbonRecord{
		Zone:                       "north",
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		Quality:                    v1alpha1.QualityFresh,
	}
}

func interval(start time.Time, duration time.Duration, ci float64) v1alpha1.ForecastInterval {
	return v1alpha1.ForecastInterval{
		Timestamp:                  v1alpha1.RFC3339Time{Time: start},
		DurationSeconds:            int64(duration / time.Second),
		CarbonIntensityGCO2ePerKWh: ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
	}
}

func TestIntegrateTableT2_2(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	pod := energy.ConstantPower{Watts: 18}
	node := energy.LinearPowerModel{PowerIdleWatts: 40, PowerMaxWatts: 100}

	type tc struct {
		name string
		got  float64
		want float64
		err  error
		note string
	}

	cf14_2s, err14_2s := IntegrateExpectedFootprint(now, 18, 2*time.Hour, 100, nil)
	cf14_2h := 18.0 * 2.0 * 100.0 * 1e-3
	cf14_7, err14_7 := IntegrateExpectedFootprint(now, 18, 2*time.Hour, 100, []v1alpha1.ForecastInterval{
		interval(now, time.Hour, 100),
		interval(now.Add(time.Hour), time.Hour, 400),
	})
	cfPersist, errPersist := IntegrateExpectedFootprint(now, 18, 2*time.Hour, 100, []v1alpha1.ForecastInterval{
		interval(now, time.Hour, 400),
	})
	cfNode, errNode := IntegrateExpectedFootprint(now, node.PredictNodePower(0.5), 2*time.Hour, 100, nil)
	cfZeroT, errZeroT := IntegrateExpectedFootprint(now, 18, 0, 100, nil)
	viaAPI := CalculateIntegralFootprint(pod, []v1alpha1.ForecastInterval{
		interval(now, time.Hour, 100),
		interval(now.Add(time.Hour), time.Hour, 400),
	}, 2*time.Hour, 0)

	grid := make([]v1alpha1.ForecastInterval, 0, 24)
	for k := 0; k < 24; k++ {
		grid = append(grid, interval(now.Add(time.Duration(k)*5*time.Minute), 5*time.Minute, 100))
	}
	cfGrid, errGrid := IntegrateExpectedFootprint(now, 18, 2*time.Hour, 100, grid)

	tests := []tc{
		{
			name: "14.2 seconds (2.10s)",
			got:  cf14_2s, err: err14_2s, want: 3.6,
			note: "18*7200*100 / 3.6e6 = 3.6 gCO2e",
		},
		{
			name: "14.2 hours identity (2.10h)",
			got:  cf14_2h, want: 3.6,
			note: "18*2*100*1e-3 = 3.6; η_h ≡ η_s",
		},
		{
			name: "14.2 seconds vs hours",
			got:  cf14_2s, err: err14_2s, want: cf14_2h,
			note: "T2.2 §2.2 η identity",
		},
		{
			name: "14.7 two forecast hours",
			got:  cf14_7, err: err14_7, want: 9.0,
			note: "18*1*100*1e-3 + 18*1*400*1e-3 = 9.0",
		},
		{
			name: "G-06 duration > forecast horizon",
			got:  cfPersist, err: errPersist, want: 9.0,
			note: "1 h at 400 then persist CI_now=100: 7.2+1.8=9.0",
		},
		{
			name: "14.2 via CalculateIntegralFootprint (P_pod)",
			got:  viaAPI, want: 9.0,
			note: "ConstantPower 18 W, two intervals",
		},
		{
			name: "2.9 node CF including idle (not scoring)",
			got:  cfNode, err: errNode, want: 14.0,
			note: "P(0.5)=70 W → 70*2*100*1e-3 = 14; idle 8 gCO2e is in this object",
		},
		{
			name: "G-11 hatT=0",
			got:  cfZeroT, err: errZeroT, want: 0,
			note: "K=0 ⇒ CF=0",
		},
		{
			name: "5 min grid K=24 equals 14.2",
			got:  cfGrid, err: errGrid, want: 3.6,
			note: "T2.2: Δt=5 min, T=2 h, constant CI — Riemann sum exact",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err != nil {
				t.Fatal(tc.err)
			}
			almostEqual(t, tc.got, tc.want, tc.note)
		})
	}
}

func TestExpectedFootprintUsesIntegrator(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	cf, err := ExpectedFootprint(now, 18, 7200, recordAt(now, 100), nil)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, cf, 3.6, "ExpectedFootprint persistence 14.2")
}

func TestScoringTableT2_2_14_3_14_4(t *testing.T) {
	tests := []struct {
		name string
		got  float64
		want float64
		note string
	}{
		{"14.3 S_C n1", CarbonScore(3.6, 3.6, 14.4, DefaultEpsilonCFGCO2e), 1, "best CF → 1"},
		{"14.3 S_C n2", CarbonScore(14.4, 3.6, 14.4, DefaultEpsilonCFGCO2e), 0, "worst CF → 0"},
		{"14.3 Score n1", WeightedScore(0.6, 0.2, 0.2, 1, 0.5, 0.5), 80, "100*(0.6+0.1+0.1)=80"},
		{"14.3 Score n2", WeightedScore(0.6, 0.2, 0.2, 0, 0.3, 0.5), 16, "100*(0+0.06+0.1)=16"},
		{"14.4 S_C degenerate", CarbonScore(3.6, 3.6, 3.6, DefaultEpsilonCFGCO2e), 0.5, "G-04 S_C=1/2 not 1.0"},
		{"14.4 Score n1", WeightedScore(0.6, 0.2, 0.2, 0.5, 0.5, 0.5), 50, "100*(0.3+0.1+0.1)=50"},
		{"14.4 Score n2", WeightedScore(0.6, 0.2, 0.2, 0.5, 0.3, 0.5), 46, "100*(0.3+0.06+0.1)=46"},
		{"14.6 S_P", PerformanceScore(1800, 3600, true), 0.5, "(3600-1800)/3600"},
		{"G-14 S_P", PerformanceScore(0, 0, true), 1, "0/0 → 1"},
		{"G-13 S_P", PerformanceScore(1800, 0, false), 1, "no deadline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			almostEqual(t, tc.got, tc.want, tc.note)
		})
	}
}

func TestIntegrateNoHeapAlloc(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	forecast := []v1alpha1.ForecastInterval{
		interval(now, time.Hour, 100),
		interval(now.Add(time.Hour), time.Hour, 400),
	}
	allocs := testing.AllocsPerRun(200, func() {
		_, _ = IntegrateExpectedFootprint(now, 18, 2*time.Hour, 100, forecast)
	})
	if allocs != 0 {
		t.Fatalf("integrator allocated %.2f objects/run; the Δt loop must stay allocation-free", allocs)
	}
}

func TestCurrentCIScore23(t *testing.T) {
	almostEqual(t, CurrentCIScore(100, 100, 400, DefaultEpsilonCIGCO2ePerKWh), 1, "(2.23) best CI")
	almostEqual(t, CurrentCIScore(400, 100, 400, DefaultEpsilonCIGCO2ePerKWh), 0, "(2.23) worst CI")
	almostEqual(t, CurrentCIScore(100, 100, 100, DefaultEpsilonCIGCO2ePerKWh), 0.5, "(2.23) degenerate")
}
