package carbon

import (
	"fmt"
	"math"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

// maxBreakpoints is 2 endpoints plus start and end of every forecast interval.
// Sized for the SSOT cap (24 h × 5 min = 288). The integrator uses a stack array
// of this length so the Δt loop does not allocate.
const maxBreakpoints = 2 + 2*v1alpha1.MaxForecastIntervals

// PowerModel is the power envelope sampled once per horizon (2.4b).
// LinearPowerModel.PredictNodePower yields P_n(u) including idle (cluster KPI).
// energy.ConstantPower{Watts: P_pod} yields scoring CF (IDLE-NONE).
type PowerModel interface {
	PredictNodePower(utilization float64) float64
}

// CalculateIntegralFootprint integrates η Σ P(u) Δt CI on [t0, t0+T).
// t0 and persistence CI are taken from the earliest valid forecast interval;
// an empty forecast yields 0 (call IntegrateExpectedFootprint with current CI).
// Duration beyond the last forecast piece uses persistence of that current CI (2.11).
func CalculateIntegralFootprint(powerModel PowerModel, forecast []v1alpha1.ForecastInterval, expectedDuration time.Duration, currentUtil float64) float64 {
	if powerModel == nil || expectedDuration <= 0 {
		return 0
	}
	power := powerModel.PredictNodePower(currentUtil)
	now, currentCI := inferHorizon(forecast)
	cf, err := IntegrateExpectedFootprint(now, power, expectedDuration, currentCI, forecast)
	if err != nil {
		return 0
	}
	return cf
}

// IntegrateExpectedFootprint is (2.10s): η Σ P Δt_k CI_k with Δt in seconds.
// Uncovered time, including T̂ past the forecast horizon, uses currentCI (2.11).
// The breakpoint buffer lives on the stack; the inner loop does not allocate.
func IntegrateExpectedFootprint(now time.Time, powerWatts float64, horizon time.Duration, currentCI float64, forecast []v1alpha1.ForecastInterval) (float64, error) {
	if horizon <= 0 || powerWatts == 0 {
		return 0, nil
	}
	if math.IsNaN(powerWatts) || math.IsInf(powerWatts, 0) || powerWatts < 0 {
		return 0, fmt.Errorf("power %v W is not usable: %w", powerWatts, v1alpha1.ErrInvalidCarbonIntensity)
	}
	if err := v1alpha1.ValidateCarbonIntensity(currentCI); err != nil {
		return 0, err
	}

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
		a, b := points[i], points[i+1]
		dt := b.Sub(a).Seconds()
		if dt <= 0 {
			continue
		}
		ci := ciAt(a, currentCI, forecast)
		if err := v1alpha1.ValidateCarbonIntensity(ci); err != nil {
			return 0, err
		}
		sum += powerWatts * dt * ci
	}
	cf := sum * EtaKWhPerWattSecond
	if math.IsNaN(cf) || math.IsInf(cf, 0) {
		return 0, fmt.Errorf("expected CF is NaN or Inf: %w", v1alpha1.ErrInvalidCarbonIntensity)
	}
	if cf < 0 {
		return 0, nil
	}
	return cf, nil
}

func inferHorizon(forecast []v1alpha1.ForecastInterval) (time.Time, float64) {
	var earliest time.Time
	var ci float64
	for i := range forecast {
		iv := forecast[i]
		if err := iv.Validate(); err != nil {
			continue
		}
		t := iv.Timestamp.UTC()
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
			ci = iv.CarbonIntensityGCO2ePerKWh
		}
	}
	if earliest.IsZero() {
		return time.Unix(0, 0).UTC(), 0
	}
	return earliest, ci
}

func appendInterior(buf []time.Time, n int, t, lo, hi time.Time) int {
	if t.IsZero() || n >= len(buf) {
		return n
	}
	t = t.UTC()
	if !t.After(lo) || !t.Before(hi) {
		return n
	}
	buf[n] = t
	return n + 1
}

func sortUniqueTimes(buf []time.Time, n int) int {
	for i := 1; i < n; i++ {
		key := buf[i]
		j := i - 1
		for j >= 0 && buf[j].After(key) {
			buf[j+1] = buf[j]
			j--
		}
		buf[j+1] = key
	}
	w := 0
	for i := 0; i < n; i++ {
		if w == 0 || !buf[i].Equal(buf[w-1]) {
			buf[w] = buf[i]
			w++
		}
	}
	return w
}
