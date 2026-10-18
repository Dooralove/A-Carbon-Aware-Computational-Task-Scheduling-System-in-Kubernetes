package replay

import (
	"context"
	"fmt"
	"sort"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

// LoopMode selects how model time outside [modelStart, modelEnd) is mapped.
type LoopMode string

const (
	// LoopLinear plays the window once and clamps to the last millisecond (hold last sample).
	LoopLinear LoopMode = "linear"
	// LoopCycle wraps model time modulo the dataset window.
	LoopCycle LoopMode = "cycle"
)

// LookupMode selects how CI is read inside an interval.
type LookupMode string

const (
	// LookupStep is piecewise-constant on [t_k, t_k+Δt) — the Replay canon (T1.1 §2.4).
	LookupStep LookupMode = "step"
	// LookupLinear interpolates between consecutive samples (sensitivity only).
	LookupLinear LookupMode = "linear"
)

// Options configure a Replay Engine instance.
type Options struct {
	Clock      v1alpha1.Clock
	Multiplier float64
	Loop       LoopMode
	Lookup     LookupMode
}

// Engine reproduces CI(z, t_model) from a loaded dataset. Queries do not
// advance the clock (ARCH §15, API-REST-02).
type Engine struct {
	dataset *Dataset
	clock   v1alpha1.Clock
	loop    LoopMode
	lookup  LookupMode
}

// Open loads path (JSON or CSV) and returns a ready Engine.
func Open(path string, opts Options) (*Engine, error) {
	ds, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	return NewEngine(ds, opts), nil
}

// NewEngine builds an Engine over an already parsed dataset.
func NewEngine(ds *Dataset, opts Options) *Engine {
	if opts.Loop == "" {
		opts.Loop = LoopLinear
	}
	if opts.Lookup == "" {
		opts.Lookup = LookupStep
	}
	clock := opts.Clock
	if clock == nil {
		mult := opts.Multiplier
		if mult <= 0 {
			mult = 1
		}
		clock = NewScaledClock(v1alpha1.SystemClock{}, ds.ModelStart, mult)
	}
	return &Engine{dataset: ds, clock: clock, loop: opts.Loop, lookup: opts.Lookup}
}

// Path returns the source file path, if loaded from disk.
func (e *Engine) Path() string {
	if e.dataset == nil {
		return ""
	}
	return e.dataset.Path
}

// DatasetID returns the envelope identifier.
func (e *Engine) DatasetID() string {
	if e.dataset == nil {
		return ""
	}
	return e.dataset.DatasetID
}

// Clock returns the injected model clock.
func (e *Engine) Clock() v1alpha1.Clock {
	return e.clock
}

// Name implements v1alpha1.CarbonProvider.
func (e *Engine) Name() string {
	return "historical-dataset"
}

// Mode implements v1alpha1.CarbonProvider.
func (e *Engine) Mode() v1alpha1.ProviderMode {
	return v1alpha1.ProviderModeReplay
}

// ListZones implements v1alpha1.CarbonProvider.
func (e *Engine) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]v1alpha1.ZoneProfile, 0, len(e.dataset.Zones))
	for _, z := range e.dataset.Zones {
		_, has := e.dataset.series[z]
		out = append(out, v1alpha1.ZoneProfile{
			APIVersion:          v1alpha1.GroupVersion,
			Kind:                v1alpha1.KindZoneProfile,
			Zone:                z,
			SourceZone:          e.dataset.SourceZones[z],
			Quality:             v1alpha1.QualityFresh,
			Source:              e.dataset.Source,
			TemporalGranularity: e.dataset.Granularity,
			IntervalSeconds:     int64(e.dataset.Interval / time.Second),
			HasCurrent:          has,
			HasForecast:         e.dataset.ForecastMode == v1alpha1.ForecastModeRecorded && len(e.dataset.forecasts[z]) > 0,
			FreshnessSeconds:    0,
		})
	}
	return out, nil
}

// Current implements v1alpha1.CarbonProvider. at is model time; zero means Clock.Now().
func (e *Engine) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if err := ctx.Err(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	if err := zone.Validate(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	model := e.modelTime(at)
	ci, ts, estimated, err := e.lookupCI(zone, model)
	if err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	quality := v1alpha1.QualityFresh
	if estimated {
		quality = v1alpha1.QualityEstimated
	}
	flow := e.dataset.FlowTraced
	rec := v1alpha1.CarbonRecord{
		APIVersion:                 v1alpha1.GroupVersion,
		Kind:                       v1alpha1.KindCarbonRecord,
		Zone:                       zone,
		Timestamp:                  rfc3339(ts),
		CarbonIntensityGCO2ePerKWh: ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     e.dataset.Source,
		SourceZone:                 e.dataset.SourceZones[zone],
		EmissionFactorType:         e.dataset.EmissionFactorType,
		TemporalGranularity:        e.dataset.Granularity,
		IntervalSeconds:            int64(e.dataset.Interval / time.Second),
		Quality:                    quality,
		FreshnessSeconds:           0,
		IsEstimated:                estimated,
		FlowTraced:                 &flow,
		FetchedAt:                  rfc3339(model),
		ExpiresAt:                  rfc3339(ts.Add(e.dataset.Interval)),
	}
	if err := rec.Validate(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	return rec, nil
}

// Forecast implements v1alpha1.CarbonProvider. Persistence/none yield an empty slice
// (plugin applies 2.11). Recorded snapshots never substitute future actuals.
func (e *Engine) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := zone.Validate(); err != nil {
		return nil, err
	}
	if _, ok := e.dataset.series[zone]; !ok && len(e.dataset.forecasts[zone]) == 0 {
		return nil, fmt.Errorf("zone %s: %w", zone, v1alpha1.ErrUnknownZone)
	}
	if horizon <= 0 {
		horizon = time.Duration(v1alpha1.DefaultForecastHorizonSeconds) * time.Second
	}
	model := e.modelTime(at)
	switch e.dataset.ForecastMode {
	case v1alpha1.ForecastModeRecorded:
		return e.recordedForecast(zone, model, horizon), nil
	default:
		// none and persistence: empty array; plugin applies (2.11). Perfect foresight forbidden.
		return []v1alpha1.ForecastInterval{}, nil
	}
}

// GetCurrentIntensity is the T4.2 convenience form (Clock.Now as `at`).
func (e *Engine) GetCurrentIntensity(ctx context.Context, zone string) (*v1alpha1.CarbonRecord, error) {
	rec, err := e.Current(ctx, v1alpha1.ZoneID(zone), e.clock.Now())
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetForecast is the T4.2 convenience form (Clock.Now as `at`).
func (e *Engine) GetForecast(ctx context.Context, zone string, duration time.Duration) ([]v1alpha1.ForecastInterval, error) {
	return e.Forecast(ctx, v1alpha1.ZoneID(zone), e.clock.Now(), duration)
}

func (e *Engine) modelTime(at time.Time) time.Time {
	if at.IsZero() {
		at = e.clock.Now()
	}
	at = at.UTC().Truncate(time.Millisecond)
	return mapLoop(at, e.dataset.ModelStart, e.dataset.ModelEnd, e.loop)
}

func (e *Engine) lookupCI(zone v1alpha1.ZoneID, at time.Time) (float64, time.Time, bool, error) {
	samples := e.dataset.series[zone]
	if len(samples) == 0 {
		return 0, time.Time{}, false, fmt.Errorf("zone %s: %w", zone, v1alpha1.ErrUnknownZone)
	}
	i := sort.Search(len(samples), func(i int) bool {
		return samples[i].Timestamp.After(at)
	}) - 1
	if i < 0 {
		return 0, time.Time{}, false, fmt.Errorf("zone %s at %s: %w", zone, at.Format(time.RFC3339Nano), ErrNoSample)
	}
	cur := samples[i]
	switch e.lookup {
	case LookupLinear:
		if i+1 < len(samples) {
			next := samples[i+1]
			span := next.Timestamp.Sub(cur.Timestamp)
			if span > 0 {
				alpha := float64(at.Sub(cur.Timestamp)) / float64(span)
				if alpha < 0 {
					alpha = 0
				}
				if alpha > 1 {
					alpha = 1
				}
				ci := cur.CarbonIntensityGCO2ePerKWh + alpha*(next.CarbonIntensityGCO2ePerKWh-cur.CarbonIntensityGCO2ePerKWh)
				return ci, cur.Timestamp, true, nil
			}
		}
		return cur.CarbonIntensityGCO2ePerKWh, cur.Timestamp, false, nil
	default:
		end := cur.Timestamp.Add(e.dataset.Interval)
		if !at.Before(end) {
			return 0, time.Time{}, false, fmt.Errorf("zone %s at %s: %w", zone, at.Format(time.RFC3339Nano), ErrNoSample)
		}
		return cur.CarbonIntensityGCO2ePerKWh, cur.Timestamp, cur.IsEstimated, nil
	}
}

func (e *Engine) recordedForecast(zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) []v1alpha1.ForecastInterval {
	snaps := e.dataset.forecasts[zone]
	if len(snaps) == 0 {
		return []v1alpha1.ForecastInterval{}
	}
	idx := -1
	for i, s := range snaps {
		if !s.IssuedAt.After(at) {
			idx = i
		}
	}
	if idx < 0 {
		return []v1alpha1.ForecastInterval{}
	}
	until := at.Add(horizon)
	out := make([]v1alpha1.ForecastInterval, 0, len(snaps[idx].Points))
	for _, p := range snaps[idx].Points {
		if p.Timestamp.Before(at) || !p.Timestamp.Before(until) {
			continue
		}
		exp := p.Timestamp.Add(e.dataset.Interval)
		h := p.Horizon
		if h == 0 {
			h = p.Timestamp.Sub(snaps[idx].IssuedAt)
		}
		out = append(out, v1alpha1.ForecastInterval{
			Timestamp:                  rfc3339(p.Timestamp),
			ExpiresAt:                  rfc3339(exp),
			DurationSeconds:            int64(e.dataset.Interval / time.Second),
			HorizonSeconds:             int64(h / time.Second),
			CarbonIntensityGCO2ePerKWh: p.CarbonIntensityGCO2ePerKWh,
			Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		})
	}
	return out
}

func mapLoop(at, start, end time.Time, mode LoopMode) time.Time {
	if !at.Before(start) && at.Before(end) {
		return at
	}
	if mode == LoopCycle {
		window := end.Sub(start)
		if window <= 0 {
			return start
		}
		elapsed := at.Sub(start) % window
		if elapsed < 0 {
			elapsed += window
		}
		return start.Add(elapsed).Truncate(time.Millisecond)
	}
	if at.Before(start) {
		return start
	}
	return end.Add(-time.Millisecond)
}

func rfc3339(t time.Time) v1alpha1.RFC3339Time {
	return v1alpha1.RFC3339Time{Time: t.UTC()}
}

var _ v1alpha1.CarbonProvider = (*Engine)(nil)
