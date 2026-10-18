package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/provider"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLoadEmpty(t *testing.T) {
	c := New()
	if _, err := c.Load(); !errors.Is(err, v1alpha1.ErrEmptyCache) {
		t.Fatalf("got %v", err)
	}
	if _, ok := c.Get("north"); ok {
		t.Fatal("empty Get must miss")
	}
}

func TestStoreIgnoresNilAndPublishes(t *testing.T) {
	c := New()
	c.Store(nil)
	if _, err := c.Load(); !errors.Is(err, v1alpha1.ErrEmptyCache) {
		t.Fatal("nil store must keep the cache empty")
	}

	snap := &v1alpha1.CarbonSnapshot{Quality: v1alpha1.QualityFresh, Zones: map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState{}}
	c.Store(snap)
	got, err := c.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Quality != v1alpha1.QualityFresh {
		t.Fatalf("quality: %s", got.Quality)
	}
}

func TestRefreshMockAndGet(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, ok := c.Get("north")
	if !ok {
		t.Fatal("north missing")
	}
	if rec.CarbonIntensityGCO2ePerKWh != 80 {
		t.Fatalf("ci=%v", rec.CarbonIntensityGCO2ePerKWh)
	}
	if rec.Quality != v1alpha1.QualityFresh {
		t.Fatalf("quality=%s", rec.Quality)
	}
	if rec.FreshnessSeconds != 0 {
		t.Fatalf("freshness=%d", rec.FreshnessSeconds)
	}
	stored := rec.CarbonIntensityGCO2ePerKWh
	rec.CarbonIntensityGCO2ePerKWh = -1
	again, _ := c.Get("north")
	if again.CarbonIntensityGCO2ePerKWh != stored {
		t.Fatal("Get must return a copy")
	}
}

func TestGetMarksStaleAfterWarn(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger(), StaleWarn: 90 * time.Second})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(91 * time.Second)
	rec, ok := c.Get("north")
	if !ok {
		t.Fatal("missing")
	}
	if rec.Quality != v1alpha1.QualityStale {
		t.Fatalf("quality=%s", rec.Quality)
	}
	if rec.FreshnessSeconds != 91 {
		t.Fatalf("freshness=%d", rec.FreshnessSeconds)
	}
	snap, err := c.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Zones["north"].Current.Quality != v1alpha1.QualityFresh {
		t.Fatal("stored snapshot must stay immutable")
	}
}

func TestRefreshKeepsLastGoodOnProviderError(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	inner := provider.NewMockProviderWithClock(clock)
	tp := &toggleProvider{inner: inner}
	c := NewWithOptions(Options{Provider: tp, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tp.fail.Store(true)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if !c.Stale() {
		t.Fatal("expected stale flag")
	}
	rec, ok := c.Get("west")
	if !ok || rec.CarbonIntensityGCO2ePerKWh != 400 {
		t.Fatalf("last-good lost: ok=%v rec=%+v", ok, rec)
	}
	if rec.Quality != v1alpha1.QualityStale {
		t.Fatalf("quality=%s", rec.Quality)
	}
	h := c.Health(v1alpha1.ProviderModeMock)
	if h.Status != v1alpha1.ServingDegraded || !h.Ready || h.HTTPStatusCode() != 200 {
		t.Fatalf("health=%+v", h)
	}
}

func TestRefreshRejectsInvalidAndFuture(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	c := NewWithOptions(Options{
		Provider: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: -1, at: now},
		Clock:    clock,
		Logger:   discardLogger(),
	})
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("negative CI must fail")
	}
	if _, err := c.Load(); !errors.Is(err, v1alpha1.ErrEmptyCache) {
		t.Fatal("invalid tick must not publish")
	}

	c = NewWithOptions(Options{
		Provider: &scriptedProvider{zones: []v1alpha1.ZoneID{"north"}, ci: 80, at: now.Add(2 * time.Hour)},
		Clock:    clock,
		Logger:   discardLogger(),
	})
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("future timestamp must fail")
	}
}

func TestGetConcurrentWithStore(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var failed atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, ok := c.Get("north"); !ok {
					failed.Store(true)
					return
				}
				if j%20 == 0 {
					_ = c.Refresh(context.Background())
				}
			}
		}()
	}
	wg.Wait()
	if failed.Load() {
		t.Fatal("north missing under concurrent Get/Refresh")
	}
}

func TestWriteMetricsContainsCanonNames(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	c.WriteMetrics(&buf)
	out := buf.String()
	for _, name := range []string{
		"carbon_aware_cds_zones",
		"carbon_aware_cds_cache_age_seconds",
		"carbon_aware_cds_provider_error_total",
		"carbon_aware_cds_quality",
	} {
		if !bytes.Contains(buf.Bytes(), []byte(name)) {
			t.Fatalf("missing metric %s in %s", name, out)
		}
	}
	if bytes.Contains(buf.Bytes(), []byte("carbon_aware_cds_replay_model_time_seconds")) {
		t.Fatal("replay gauge must not be exported in mock mode")
	}
}

func TestLoopStopsOnCancel(t *testing.T) {
	c := NewWithOptions(Options{
		Provider:        provider.NewMockProvider(),
		Logger:          discardLogger(),
		RefreshInterval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Loop(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return")
	}
}

func BenchmarkCacheGet(b *testing.B) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := c.Get("north"); !ok {
			b.Fatal("miss")
		}
	}
}

func BenchmarkCacheGetParallel(b *testing.B) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := NewWithOptions(Options{Provider: p, Clock: clock, Logger: discardLogger()})
	if err := c.Refresh(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, ok := c.Get("north"); !ok {
				b.Fatal("miss")
			}
		}
	})
}

type toggleProvider struct {
	inner v1alpha1.CarbonProvider
	fail  atomic.Bool
}

func (p *toggleProvider) Name() string                { return p.inner.Name() }
func (p *toggleProvider) Mode() v1alpha1.ProviderMode { return p.inner.Mode() }
func (p *toggleProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if p.fail.Load() {
		return nil, provider.ErrTimeout
	}
	return p.inner.ListZones(ctx)
}
func (p *toggleProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if p.fail.Load() {
		return v1alpha1.CarbonRecord{}, provider.ErrTimeout
	}
	return p.inner.Current(ctx, zone, at)
}
func (p *toggleProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	if p.fail.Load() {
		return nil, provider.ErrTimeout
	}
	return p.inner.Forecast(ctx, zone, at, horizon)
}

type scriptedProvider struct {
	zones       []v1alpha1.ZoneID
	ci          float64
	at          time.Time
	quality     v1alpha1.DataQuality
	emission    v1alpha1.EmissionFactorType
	forecast    []v1alpha1.ForecastInterval
	forecastErr error
	listErr     error
	currentErr  error
	mode        v1alpha1.ProviderMode
}

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Mode() v1alpha1.ProviderMode {
	if p.mode == "" {
		return v1alpha1.ProviderModeMock
	}
	return p.mode
}
func (p *scriptedProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.listErr != nil {
		return nil, p.listErr
	}
	out := make([]v1alpha1.ZoneProfile, 0, len(p.zones))
	for _, z := range p.zones {
		out = append(out, v1alpha1.ZoneProfile{Zone: z, HasCurrent: true})
	}
	return out, nil
}
func (p *scriptedProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if err := ctx.Err(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	if p.currentErr != nil {
		return v1alpha1.CarbonRecord{}, p.currentErr
	}
	ts := p.at
	if ts.IsZero() {
		ts = at
	}
	q := p.quality
	if q == "" {
		q = v1alpha1.QualityFresh
	}
	em := p.emission
	if em == "" {
		em = v1alpha1.EmissionLifecycle
	}
	rec := v1alpha1.CarbonRecord{
		Zone:                       zone,
		Timestamp:                  v1alpha1.RFC3339Time{Time: ts},
		CarbonIntensityGCO2ePerKWh: p.ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         em,
		Quality:                    q,
		FetchedAt:                  v1alpha1.RFC3339Time{Time: ts},
	}
	return rec, nil
}
func (p *scriptedProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	_, _, _ = ctx, zone, at
	_ = horizon
	if p.forecastErr != nil {
		return nil, p.forecastErr
	}
	if p.forecast == nil {
		return []v1alpha1.ForecastInterval{}, nil
	}
	return append([]v1alpha1.ForecastInterval(nil), p.forecast...), nil
}
