package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Strategy maps a T10.3 strategy onto a scheduler profile of the stand.
type Strategy struct {
	Name          string
	SchedulerName string
	// Weights are (w_C, w_R, w_P) as configured in the profile; nil for the
	// default scheduler, which has no CarbonAware plugin.
	Weights *[3]float64
}

var strategies = map[string]Strategy{
	"default":     {Name: "default", SchedulerName: "default-scheduler"},
	"carbon-only": {Name: "carbon-only", SchedulerName: "carbon-only-scheduler", Weights: &[3]float64{1, 0, 0}},
	"proposed":    {Name: "proposed", SchedulerName: "carbon-aware-scheduler", Weights: &[3]float64{0.6, 0.2, 0.2}},
}

// Scenario is a replay dataset baked into the CDS image plus the replay
// speed-up and the model-time anchor where task submission starts.
type Scenario struct {
	Name        string
	DatasetPath string
	DatasetID   string
	ModelStart  time.Time
	// Multiplier is model seconds per wall second (CDS replayTimeMultiplier).
	Multiplier float64
	// Anchor is the model-time offset from ModelStart at which the first task
	// is submitted; the runner waits for it after restarting the CDS. It must
	// leave the scheduler's Cache Sync Worker time to reconnect (~15 s wall).
	Anchor time.Duration
}

// The 24 h datasets play 300x (1 wall s = 5 model min, a ~4 min run spans
// most of the day); the 2 h volatile dataset plays 30x so its three ranking
// reversals fall inside one run.
var scenarios = map[string]Scenario{
	"stable": {
		Name:        "stable",
		DatasetPath: "/data/datasets/synthetic_multizone_stable_24h_v1.json",
		DatasetID:   "synthetic_multizone_stable_24h_v1",
		ModelStart:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Multiplier:  300,
		Anchor:      2 * time.Hour,
	},
	"dynamic": {
		Name:        "dynamic",
		DatasetPath: "/data/datasets/synthetic_multizone_dynamic_24h_v1.json",
		DatasetID:   "synthetic_multizone_dynamic_24h_v1",
		ModelStart:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Multiplier:  300,
		Anchor:      2 * time.Hour,
	},
	"volatile": {
		Name:        "volatile",
		DatasetPath: "/data/datasets/synthetic_multizone_volatile_2h_v1.json",
		DatasetID:   "synthetic_multizone_volatile_2h_v1",
		ModelStart:  time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		Multiplier:  30,
		Anchor:      15 * time.Minute,
	},
}

// RunPlan is one run of the matrix.
type RunPlan struct {
	ID             string
	Experiment     string
	Strategy       Strategy
	Scenario       Scenario
	Repetition     int
	Seed           uint64
	CarbonWeight   *float64
	DeadlineFactor float64
}

// EffectiveWeights returns (w_C, w_R, w_P) as the plugin applies them, or
// nil for the default scheduler. A Pod carbon-weight scales w_R and w_P to
// the remaining 1-w_C (pkg/scheduler/prescore.go weights()).
func (r RunPlan) EffectiveWeights() *[3]float64 {
	if r.Strategy.Weights == nil {
		return nil
	}
	w := *r.Strategy.Weights
	if r.CarbonWeight == nil || r.Strategy.Name != "proposed" {
		return &w
	}
	wc := *r.CarbonWeight
	rest := w[1] + w[2]
	if rest == 0 {
		return &[3]float64{1, 0, 0}
	}
	scale := (1 - wc) / rest
	return &[3]float64{wc, w[1] * scale, w[2] * scale}
}

func runID(experiment, scenario, strategy string, wc *float64, df float64, rep int) string {
	parts := []string{experiment, scenario, strategy}
	if wc != nil {
		parts = append(parts, "wc"+strconv.FormatFloat(*wc, 'f', -1, 64))
	}
	if df > 0 {
		parts = append(parts, "df"+strconv.FormatFloat(df, 'f', -1, 64))
	}
	parts = append(parts, fmt.Sprintf("r%d", rep))
	return strings.Join(parts, "-")
}

// buildPlan expands the flag lists into runs. Repetitions are the outer loop
// and strategies the inner one, so slow drift of the host hits every strategy
// alike; the seed depends only on the repetition, so every strategy and
// parameter value of one repetition gets the identical task pack.
func buildPlan(experiment string, strategyNames, scenarioNames []string, weights []*float64, factors []float64, reps, firstRep int, seed uint64) ([]RunPlan, error) {
	if reps <= 0 {
		return nil, fmt.Errorf("--repetitions must be > 0")
	}
	if len(weights) == 0 {
		weights = []*float64{nil}
	}
	if len(factors) == 0 {
		factors = []float64{0}
	}
	var plan []RunPlan
	for rep := firstRep; rep < firstRep+reps; rep++ {
		for _, sc := range scenarioNames {
			scenario, ok := scenarios[sc]
			if !ok {
				return nil, fmt.Errorf("unknown scenario %q (want %s)", sc, keys(scenarios))
			}
			for _, df := range factors {
				for _, wc := range weights {
					for _, st := range strategyNames {
						strategy, ok := strategies[st]
						if !ok {
							return nil, fmt.Errorf("unknown strategy %q (want %s)", st, keys(strategies))
						}
						if wc != nil && st != "proposed" {
							return nil, fmt.Errorf("--carbon-weight applies only to strategy proposed")
						}
						plan = append(plan, RunPlan{
							ID:             runID(experiment, sc, st, wc, df, rep),
							Experiment:     experiment,
							Strategy:       strategy,
							Scenario:       scenario,
							Repetition:     rep,
							Seed:           seed + uint64(rep),
							CarbonWeight:   wc,
							DeadlineFactor: df,
						})
					}
				}
			}
		}
	}
	return plan, nil
}

func keys[V any](m map[string]V) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseWeights(s string) ([]*float64, error) {
	var out []*float64
	for _, p := range splitList(s) {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || v > 1 {
			return nil, fmt.Errorf("carbon weight %q must be in [0, 1]", p)
		}
		out = append(out, &v)
	}
	return out, nil
}

func parseFactors(s string) ([]float64, error) {
	var out []float64
	for _, p := range splitList(s) {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("deadline factor %q must be >= 1", p)
		}
		out = append(out, v)
	}
	return out, nil
}
