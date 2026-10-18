// Package carbon implements expected carbon footprint (2.10s), persistence (2.11)
// and private scorings S_C / (2.23). Pure functions; no I/O (GO-13, GO-19).
//
// This is the formula package at pkg/carbon/*.go, distinct from CDS subpackages
// pkg/carbon/{api,cache,provider,config,replay} (CONF-05).
package carbon

import (
	"fmt"
	"math"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	// EtaKWhPerWattSecond is η for Δt in seconds (2.1): 1 / 3.6e6.
	EtaKWhPerWattSecond = 1.0 / 3.6e6

	// NeutralScore is S_C^neutral = 1/2 (G-04, G-09).
	NeutralScore = 0.5

	// DefaultEpsilonCFGCO2e is ε_CF (gCO2e).
	DefaultEpsilonCFGCO2e = 1e-6

	// DefaultEpsilonCIGCO2ePerKWh is ε_CI (gCO2e/kWh) for (2.23).
	DefaultEpsilonCIGCO2ePerKWh = 1e-6
)

// Calculator implements v1alpha1.CarbonCalculator.
type Calculator struct{}

var _ v1alpha1.CarbonCalculator = Calculator{}

// ExpectedFootprintGCO2e implements the API interface. Horizon starts at
// current.Timestamp when set; callers that have a scheduler clock should use
// ExpectedFootprint instead.
func (Calculator) ExpectedFootprintGCO2e(podPowerWatts float64, horizonSeconds int64, current v1alpha1.CarbonRecord, forecast []v1alpha1.ForecastInterval) (float64, error) {
	t0 := current.Timestamp.Time
	if t0.IsZero() {
		t0 = time.Unix(0, 0).UTC()
	}
	return ExpectedFootprint(t0, podPowerWatts, horizonSeconds, current, forecast)
}

// ExpectedFootprint is (2.10s) on [now, now+hatT). Empty forecast → persistence (2.11).
func ExpectedFootprint(now time.Time, podPowerWatts float64, horizonSeconds int64, current v1alpha1.CarbonRecord, forecast []v1alpha1.ForecastInterval) (float64, error) {
	if !current.IsUsableForScoring() {
		return 0, fmt.Errorf("current carbon record is not usable: %w", v1alpha1.ErrInvalidCarbonIntensity)
	}
	return IntegrateExpectedFootprint(
		now,
		podPowerWatts,
		time.Duration(horizonSeconds)*time.Second,
		current.CarbonIntensityGCO2ePerKWh,
		forecast,
	)
}

func intervalEnd(iv v1alpha1.ForecastInterval) time.Time {
	if !iv.ExpiresAt.IsZero() {
		return iv.ExpiresAt.Time
	}
	if iv.DurationSeconds > 0 {
		return iv.Timestamp.Add(time.Duration(iv.DurationSeconds) * time.Second)
	}
	return time.Time{}
}

func ciAt(t time.Time, current float64, forecast []v1alpha1.ForecastInterval) float64 {
	t = t.UTC()
	for i := range forecast {
		iv := forecast[i]
		if err := iv.Validate(); err != nil {
			continue
		}
		start := iv.Timestamp.UTC()
		if start.IsZero() || t.Before(start) {
			continue
		}
		end := intervalEnd(iv)
		if end.IsZero() || t.Before(end.UTC()) {
			return iv.CarbonIntensityGCO2ePerKWh
		}
	}
	return current
}

func epsilonOrDefault(value, fallback float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return value
}
