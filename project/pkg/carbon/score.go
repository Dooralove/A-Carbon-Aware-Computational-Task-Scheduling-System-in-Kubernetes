package carbon

import "math"

// CarbonScore is (2.15). Nodes that do not enter (2.14) must not call this
// with their CF; the caller assigns NeutralScore (G-09).
func CarbonScore(cf, cfMin, cfMax, epsilonCFGCO2e float64) float64 {
	eps := epsilonOrDefault(epsilonCFGCO2e, DefaultEpsilonCFGCO2e)
	span := cfMax - cfMin
	if math.IsNaN(span) || math.IsInf(span, 0) || span <= eps {
		return NeutralScore
	}
	s := (cfMax - cf) / span
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// CurrentCIScore is (2.23). Missing/invalid nodes use NeutralScore and are
// excluded from ciMin/ciMax by the caller.
func CurrentCIScore(ci, ciMin, ciMax, epsilonCIGCO2ePerKWh float64) float64 {
	eps := epsilonOrDefault(epsilonCIGCO2ePerKWh, DefaultEpsilonCIGCO2ePerKWh)
	span := ciMax - ciMin
	if math.IsNaN(span) || math.IsInf(span, 0) || span <= eps {
		return NeutralScore
	}
	s := (ciMax - ci) / span
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// PerformanceScore is (2.18). Call only after hard SLA (2.13) succeeded.
// hasDeadline=false is G-13 (T_deadline = +∞) → S_P = 1.
func PerformanceScore(hatTSeconds, deadlineSeconds int64, hasDeadline bool) float64 {
	if !hasDeadline {
		return 1
	}
	if deadlineSeconds == 0 && hatTSeconds == 0 {
		return 1
	}
	if deadlineSeconds <= 0 {
		return 0
	}
	s := float64(deadlineSeconds-hatTSeconds) / float64(deadlineSeconds)
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

// WeightedScore is (2.19): 100 * (w_C S_C + w_R S_R + w_P S_P).
func WeightedScore(carbonWeight, resourceWeight, performanceWeight, sC, sR, sP float64) float64 {
	return 100 * (carbonWeight*sC + resourceWeight*sR + performanceWeight*sP)
}

// FixedPointScore stores rnd(100 * Score) for CycleState (model §9.2).
// math.Round ties away from zero.
func FixedPointScore(pluginScore float64) int64 {
	if math.IsNaN(pluginScore) || math.IsInf(pluginScore, 0) {
		return 50 * 100
	}
	fp := math.Round(100 * pluginScore)
	if fp < 0 {
		return 0
	}
	if fp > 100*100 {
		return 100 * 100
	}
	return int64(fp)
}

// FrameworkScore is (2.21): rnd(s_fp / 100) then clamp to [0, 100].
func FrameworkScore(fixedPoint int64) int64 {
	s := int64(math.Round(float64(fixedPoint) / 100))
	if s < 0 {
		return 0
	}
	if s > 100 {
		return 100
	}
	return s
}

// SafePartial reports whether S_• is usable (G-21).
func SafePartial(s float64) bool {
	return !math.IsNaN(s) && !math.IsInf(s, 0)
}
