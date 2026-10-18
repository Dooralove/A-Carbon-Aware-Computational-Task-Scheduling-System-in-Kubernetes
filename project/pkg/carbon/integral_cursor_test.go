package carbon

import (
	"math/rand/v2"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

// integrateReference is the pre-Gate-9 integral: ciAt scans the whole forecast
// for every breakpoint (O(F²)). The cursor version must match it bit for bit.
func integrateReference(now time.Time, powerWatts float64, horizon time.Duration, currentCI float64, forecast []v1alpha1.ForecastInterval) float64 {
	now = now.UTC()
	end := now.Add(horizon)
	var points [maxBreakpoints]time.Time
	n := 0
	points[n] = now
	n++
	points[n] = end
	n++
	for i := range forecast {
		iv := forecast[i]
		if err := iv.Validate(); err != nil {
			continue
		}
		n = appendInterior(points[:], n, iv.Timestamp.UTC(), now, end)
		n = appendInterior(points[:], n, intervalEnd(iv), now, end)
	}
	n = sortUniqueTimes(points[:], n)
	var sum float64
	for i := 0; i < n-1; i++ {
		dt := points[i+1].Sub(points[i]).Seconds()
		if dt <= 0 {
			continue
		}
		sum += powerWatts * dt * ciAt(points[i], currentCI, forecast)
	}
	cf := sum * EtaKWhPerWattSecond
	if cf < 0 {
		return 0
	}
	return cf
}

func randomForecast(r *rand.Rand, now time.Time, disjoint bool) []v1alpha1.ForecastInterval {
	count := r.IntN(v1alpha1.MaxForecastIntervals + 1)
	out := make([]v1alpha1.ForecastInterval, 0, count)
	t := now.Add(time.Duration(r.IntN(1200)-600) * time.Second)
	for k := 0; k < count; k++ {
		dur := time.Duration(60+r.IntN(900)) * time.Second
		iv := v1alpha1.ForecastInterval{
			Timestamp:                  v1alpha1.RFC3339Time{Time: t},
			DurationSeconds:            int64(dur / time.Second),
			CarbonIntensityGCO2ePerKWh: 20 + r.Float64()*800,
			Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		}
		if r.IntN(2) == 0 {
			iv.ExpiresAt = v1alpha1.RFC3339Time{Time: t.Add(dur)}
		}
		if !disjoint {
			switch r.IntN(6) {
			case 0: // overlaps the next interval
				iv.ExpiresAt = v1alpha1.RFC3339Time{Time: t.Add(3 * dur)}
			case 1: // invalid, skipped by both implementations
				iv.CarbonIntensityGCO2ePerKWh = -1
			case 2: // open-ended
				iv.ExpiresAt = v1alpha1.RFC3339Time{}
				iv.DurationSeconds = 0
			}
		}
		out = append(out, iv)
		gap := time.Duration(0)
		if r.IntN(4) == 0 {
			gap = time.Duration(r.IntN(600)) * time.Second
		}
		t = t.Add(dur + gap)
	}
	if !disjoint && len(out) > 1 && r.IntN(3) == 0 {
		r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	}
	return out
}

func TestIntegrateCursorMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for trial := 0; trial < 2000; trial++ {
		disjoint := trial%2 == 0
		fc := randomForecast(r, now, disjoint)
		horizon := time.Duration(1+r.IntN(48*3600)) * time.Second
		power := 1 + r.Float64()*200
		current := 50 + r.Float64()*500
		got, err := IntegrateExpectedFootprint(now, power, horizon, current, fc)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		want := integrateReference(now, power, horizon, current, fc)
		if got != want {
			t.Fatalf("trial %d (disjoint=%v, F=%d, T=%s): got %.17g want %.17g", trial, disjoint, len(fc), horizon, got, want)
		}
	}
}

func BenchmarkIntegrate288(b *testing.B) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	fc := make([]v1alpha1.ForecastInterval, v1alpha1.MaxForecastIntervals)
	for k := range fc {
		ts := now.Add(time.Duration(k) * 5 * time.Minute)
		fc[k] = v1alpha1.ForecastInterval{
			Timestamp:                  v1alpha1.RFC3339Time{Time: ts},
			ExpiresAt:                  v1alpha1.RFC3339Time{Time: ts.Add(5 * time.Minute)},
			DurationSeconds:            300,
			CarbonIntensityGCO2ePerKWh: 100 + float64(k%7),
			Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := IntegrateExpectedFootprint(now, 25, 24*time.Hour, 100, fc); err != nil {
			b.Fatal(err)
		}
	}
}
