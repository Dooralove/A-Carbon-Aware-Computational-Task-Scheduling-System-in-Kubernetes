package cache

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/provider"
)

func TestStateClockMaxAgeAndMissingZone(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{
		Provider:   p,
		Clock:      clock,
		Logger:     discardLogger(),
		MaxDataAge: 120 * time.Second,
	})
	if _, ok := c.State("north"); ok {
		t.Fatal("empty State must miss")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Clock() != clock {
		t.Fatal("Clock must be the injected FakeClock")
	}
	if c.MaxDataAge() != 120*time.Second {
		t.Fatalf("MaxDataAge=%s", c.MaxDataAge())
	}
	st, ok := c.State("central")
	if !ok || st.Current.CarbonIntensityGCO2ePerKWh != 240 {
		t.Fatalf("state: ok=%v %+v", ok, st)
	}
	if _, ok := c.Get("east"); ok {
		t.Fatal("unknown zone must miss")
	}
	age, ok := c.SnapshotAge(now)
	if !ok || age != 0 {
		t.Fatalf("age=%s ok=%v", age, ok)
	}
	age, ok = c.SnapshotAge(now.Add(-time.Second))
	if !ok || age != 0 {
		t.Fatalf("future fetchedAt must clamp age to 0, got %s", age)
	}
}

func TestRefreshValidationTable(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	cases := []struct {
		name string
		p    *scriptedProvider
	}{
		{name: "nan", p: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: math.NaN(), at: now}},
		{name: "over-max", p: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 2001, at: now}},
		{name: "direct", p: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now, emission: v1alpha1.EmissionDirect}},
		{name: "invalid-quality", p: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now, quality: v1alpha1.QualityInvalid}},
		{name: "bad-zone-id", p: &scriptedProvider{zones: []v1alpha1.ZoneID{"NOT_A_ZONE"}, ci: 80, at: now}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewWithOptions(Options{Provider: tc.p, Clock: clock, Logger: discardLogger()})
			if err := c.Refresh(context.Background()); err == nil {
				t.Fatal("expected validation failure")
			}
			if _, err := c.Load(); !errors.Is(err, v1alpha1.ErrEmptyCache) {
				t.Fatal("invalid tick must not publish")
			}
		})
	}
}

func TestRefreshNilProviderCanceledEmptyCatalogue(t *testing.T) {
	c := New()
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("nil provider")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c = NewWithOptions(Options{Provider: provider.NewMockProvider(), Logger: discardLogger()})
	if err := c.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	c = NewWithOptions(Options{
		Provider: &scriptedProvider{},
		Logger:   discardLogger(),
	})
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("empty catalogue")
	}
}

func TestRefreshForecastDropEmptyAndError(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	good := []v1alpha1.ForecastInterval{{
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: 90,
	}}
	bad := v1alpha1.ForecastInterval{CarbonIntensityGCO2ePerKWh: 90} // no timestamp
	c := NewWithOptions(Options{
		Provider: &scriptedProvider{
			zones:    []v1alpha1.ZoneID{"north"},
			ci:       80,
			at:       now,
			forecast: []v1alpha1.ForecastInterval{bad, good[0]},
		},
		Clock:  clock,
		Logger: discardLogger(),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, ok := c.State("north")
	if !ok || len(st.Forecast) != 1 || st.Forecast[0].Unit != v1alpha1.IntensityUnitGCO2ePerKWh {
		t.Fatalf("dropped invalid interval: %+v", st.Forecast)
	}

	c = NewWithOptions(Options{
		Provider: &scriptedProvider{
			zones:       []v1alpha1.ZoneID{"north"},
			ci:          80,
			at:          now,
			forecastErr: v1alpha1.ErrEmptyForecast,
		},
		Clock:  clock,
		Logger: discardLogger(),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	goodCache := NewWithOptions(Options{
		Provider: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now},
		Clock:    clock,
		Logger:   discardLogger(),
	})
	if err := goodCache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	goodCache.provider = &scriptedProvider{
		zones:       []v1alpha1.ZoneID{"north"},
		ci:          80,
		at:          now,
		forecastErr: errors.New("upstream forecast"),
	}
	if err := goodCache.Refresh(context.Background()); err == nil {
		t.Fatal("forecast error must fail the tick")
	}
	rec, ok := goodCache.Get("north")
	if !ok || rec.CarbonIntensityGCO2ePerKWh != 80 {
		t.Fatal("last-good must remain")
	}
}

func TestRefreshCanceledDoesNotMarkStale(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	c := NewWithOptions(Options{
		Provider: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now},
		Clock:    clock,
		Logger:   discardLogger(),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.provider = &scriptedProvider{
		zones:      []v1alpha1.ZoneID{"north"},
		ci:         80,
		at:         now,
		currentErr: context.Canceled,
	}
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected cancel")
	}
	if c.Stale() {
		t.Fatal("canceled refresh must not flip last-good to stale")
	}
}

func TestFailClassifiesProviderErrors(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	seed := &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now}
	c := NewWithOptions(Options{Provider: seed, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.provider = &scriptedProvider{listErr: provider.ErrInvalidResponse}
	_ = c.Refresh(context.Background())
	c.provider = &scriptedProvider{listErr: errors.New("connection reset")}
	_ = c.Refresh(context.Background())
	var buf bytes.Buffer
	c.WriteMetrics(&buf)
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte(`kind="parse"`)) || !bytes.Contains(buf.Bytes(), []byte(`kind="http"`)) {
		t.Fatalf("metrics: %s", out)
	}
}

func TestClonePointerFieldsAndZeroFetchedAt(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	c := NewWithOptions(Options{Clock: clock, Logger: discardLogger(), StaleWarn: time.Second})
	pct := 40.0
	method := "ml"
	flow := true
	rec := v1alpha1.CarbonRecord{
		Zone:                       "north",
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: 80,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		Quality:                    v1alpha1.QualityEstimated,
		CarbonFreePercentage:       &pct,
		EstimationMethod:           &method,
		FlowTraced:                 &flow,
	}
	c.Store(&v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: now},
		Quality:   v1alpha1.QualityFresh,
		Zones:     map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState{"north": {Current: rec}},
	})
	got, ok := c.Get("north")
	if !ok {
		t.Fatal("missing")
	}
	*got.FlowTraced = false
	*got.CarbonFreePercentage = 0
	*got.EstimationMethod = "mutated"
	again, _ := c.Get("north")
	if !*again.FlowTraced || *again.CarbonFreePercentage != 40 || *again.EstimationMethod != "ml" {
		t.Fatal("Get must clone pointer fields")
	}
	clock.Advance(2 * time.Second)
	staleish, _ := c.Get("north")
	if staleish.Quality != v1alpha1.QualityEstimated {
		t.Fatalf("estimated must not be overwritten to stale, got %s", staleish.Quality)
	}

	c.Store(&v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: now},
		Zones:     map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState{"west": {Current: v1alpha1.CarbonRecord{Zone: "west", Quality: v1alpha1.QualityFresh}}},
	})
	c.stale.Store(true)
	zero, ok := c.Get("west")
	if !ok || zero.Quality != v1alpha1.QualityStale || zero.FreshnessSeconds != 0 {
		t.Fatalf("zero fetchedAt + failed refresh: %+v", zero)
	}
}

func TestHealthDegradedByAgeAndReplayModelTime(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger(), StaleWarn: 90 * time.Second})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(91 * time.Second)
	h := c.Health(v1alpha1.ProviderModeReplay)
	if h.Status != v1alpha1.ServingDegraded || h.ModelTime == nil || !h.Ready {
		t.Fatalf("health: %+v", h)
	}
	empty := NewWithOptions(Options{Clock: clock, Logger: discardLogger()})
	eh := empty.Health(v1alpha1.ProviderModeReplay)
	if eh.ModelTime == nil || eh.Ready {
		t.Fatalf("empty replay health: %+v", eh)
	}
}

func TestWriteMetricsReplayAndStale(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now, mode: v1alpha1.ProviderModeReplay}
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.stale.Store(true)
	var buf bytes.Buffer
	c.WriteMetrics(&buf)
	if !bytes.Contains(buf.Bytes(), []byte("carbon_aware_cds_replay_model_time_seconds")) {
		t.Fatalf("replay gauge missing: %s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(`quality="stale"} 1`)) {
		t.Fatalf("stale quality: %s", buf.String())
	}
	empty := New()
	buf.Reset()
	empty.WriteMetrics(&buf)
	if bytes.Contains(buf.Bytes(), []byte("carbon_aware_cds_cache_age_seconds")) {
		t.Fatal("age gauge must be omitted when snapshot is nil")
	}
}

func TestRunInitialRefreshThenCancel(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	c := NewWithOptions(Options{
		Provider:        provider.NewMockProviderWithClock(clock),
		Clock:           clock,
		Logger:          discardLogger(),
		RefreshInterval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := c.Get("north"); ok {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run did not publish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestLoopTicksRefresh(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	inner := provider.NewMockProviderWithClock(clock)
	cp := &countListProvider{inner: inner}
	c := NewWithOptions(Options{
		Provider:        cp,
		Clock:           clock,
		Logger:          discardLogger(),
		RefreshInterval: 15 * time.Millisecond,
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := cp.n.Load()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Loop(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for cp.n.Load() <= before {
		if time.Now().After(deadline) {
			t.Fatal("Loop did not tick")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type countListProvider struct {
	inner v1alpha1.CarbonProvider
	n     atomic.Int64
}

func (p *countListProvider) Name() string                { return p.inner.Name() }
func (p *countListProvider) Mode() v1alpha1.ProviderMode { return p.inner.Mode() }
func (p *countListProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	p.n.Add(1)
	return p.inner.ListZones(ctx)
}
func (p *countListProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	return p.inner.Current(ctx, zone, at)
}
func (p *countListProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	return p.inner.Forecast(ctx, zone, at, horizon)
}
