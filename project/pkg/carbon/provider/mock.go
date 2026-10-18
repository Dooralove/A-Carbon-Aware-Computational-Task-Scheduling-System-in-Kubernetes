package provider

import (
	"context"
	"fmt"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

// MockProvider is the in-process synthetic adapter (SCOPE-IN-03). No network.
type MockProvider struct {
	clock v1alpha1.Clock
	zones map[v1alpha1.ZoneID]float64
}

// NewMockProvider returns a deterministic mock for north/west/central.
func NewMockProvider() *MockProvider {
	return NewMockProviderWithClock(v1alpha1.SystemClock{})
}

// NewMockProviderWithClock injects a clock for tests (DOD-TEST-01).
func NewMockProviderWithClock(clock v1alpha1.Clock) *MockProvider {
	if clock == nil {
		clock = v1alpha1.SystemClock{}
	}
	return &MockProvider{
		clock: clock,
		zones: map[v1alpha1.ZoneID]float64{
			"north":   80,
			"central": 240,
			"west":    400,
		},
	}
}

// Name implements v1alpha1.CarbonProvider.
func (p *MockProvider) Name() string { return "mock" }

// Mode implements v1alpha1.CarbonProvider.
func (p *MockProvider) Mode() v1alpha1.ProviderMode { return v1alpha1.ProviderModeMock }

// Clock returns the adapter clock (wall or injected FakeClock).
func (p *MockProvider) Clock() v1alpha1.Clock { return p.clock }

// ListZones implements v1alpha1.CarbonProvider.
func (p *MockProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]v1alpha1.ZoneProfile, 0, len(p.zones))
	for z := range p.zones {
		out = append(out, v1alpha1.ZoneProfile{
			APIVersion:          v1alpha1.GroupVersion,
			Kind:                v1alpha1.KindZoneProfile,
			Zone:                z,
			Quality:             v1alpha1.QualityFresh,
			Source:              v1alpha1.SourceMock,
			TemporalGranularity: v1alpha1.Granularity5Minutes,
			IntervalSeconds:     300,
			HasCurrent:          true,
			HasForecast:         false,
		})
	}
	return out, nil
}

// Current implements v1alpha1.CarbonProvider.
func (p *MockProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if err := ctx.Err(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	if err := zone.Validate(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	ci, ok := p.zones[zone]
	if !ok {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("zone %s: %w", zone, v1alpha1.ErrUnknownZone)
	}
	if at.IsZero() {
		at = p.clock.Now()
	}
	at = at.UTC().Truncate(time.Second)
	start := at.Truncate(5 * time.Minute)
	flow := true
	rec := v1alpha1.CarbonRecord{
		APIVersion:                 v1alpha1.GroupVersion,
		Kind:                       v1alpha1.KindCarbonRecord,
		Zone:                       zone,
		Timestamp:                  v1alpha1.RFC3339Time{Time: start},
		CarbonIntensityGCO2ePerKWh: ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		TemporalGranularity:        v1alpha1.Granularity5Minutes,
		IntervalSeconds:            300,
		Quality:                    v1alpha1.QualityFresh,
		FreshnessSeconds:           0,
		FlowTraced:                 &flow,
		FetchedAt:                  v1alpha1.RFC3339Time{Time: at},
		ExpiresAt:                  v1alpha1.RFC3339Time{Time: start.Add(5 * time.Minute)},
	}
	return rec, rec.Validate()
}

// Forecast implements v1alpha1.CarbonProvider: empty → persistence (2.11) in the plugin.
func (p *MockProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := p.Current(ctx, zone, at); err != nil {
		return nil, err
	}
	_ = horizon
	return []v1alpha1.ForecastInterval{}, nil
}

// GetCurrentIntensity is the T4.2 convenience form.
func (p *MockProvider) GetCurrentIntensity(ctx context.Context, zone string) (*v1alpha1.CarbonRecord, error) {
	rec, err := p.Current(ctx, v1alpha1.ZoneID(zone), p.clock.Now())
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetForecast is the T4.2 convenience form.
func (p *MockProvider) GetForecast(ctx context.Context, zone string, duration time.Duration) ([]v1alpha1.ForecastInterval, error) {
	return p.Forecast(ctx, v1alpha1.ZoneID(zone), p.clock.Now(), duration)
}

var _ v1alpha1.CarbonProvider = (*MockProvider)(nil)
