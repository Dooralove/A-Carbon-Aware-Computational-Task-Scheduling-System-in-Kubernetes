package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/cache"
	"carbon-aware-k8s/pkg/carbon/provider"
)

func TestEmptyCacheRESTStatus503(t *testing.T) {
	srv := newTestServer()
	paths := []string{
		v1alpha1.PathZones,
		"/api/v1/zones/north/current",
		"/api/v1/zones/north/forecast",
	}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status %d body %s", p, rec.Code, rec.Body.String())
		}
		var st v1alpha1.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.APIVersion != "v1" || st.Kind != v1alpha1.KindStatus || st.Reason != v1alpha1.ReasonServiceUnavailable {
			t.Fatalf("%s status body: %+v", p, st)
		}
	}
}

func TestWriteErrorMapping(t *testing.T) {
	srv := newTestServer()
	cases := []struct {
		err    error
		code   int
		reason v1alpha1.StatusReason
	}{
		{v1alpha1.ErrEmptyCache, http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable},
		{provider.ErrNotImplemented, http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable},
		{provider.ErrTimeout, http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable},
		{provider.ErrInvalidResponse, http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable},
		{v1alpha1.ErrUnknownZone, http.StatusNotFound, v1alpha1.ReasonNotFound},
		{v1alpha1.ErrInvalidDuration, http.StatusBadRequest, v1alpha1.ReasonBadRequest},
		{errors.New("upstream"), http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		srv.writeError(rec, tc.err)
		if rec.Code != tc.code {
			t.Fatalf("%v: code %d want %d", tc.err, rec.Code, tc.code)
		}
		var st v1alpha1.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		if st.Reason != tc.reason {
			t.Fatalf("%v: reason %s", tc.err, st.Reason)
		}
	}
}

func TestCurrentJSONContract(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/west/current", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	if !strings.Contains(raw, `"apiVersion":"carbon-aware.io/v1alpha1"`) {
		t.Fatalf("apiVersion: %s", raw)
	}
	if !strings.Contains(raw, `"kind":"CarbonRecord"`) {
		t.Fatalf("kind: %s", raw)
	}
	if !strings.Contains(raw, `"unit":"gCO2e/kWh"`) {
		t.Fatalf("unit: %s", raw)
	}
	if !strings.Contains(raw, `"carbonIntensity":400`) {
		t.Fatalf("carbonIntensity: %s", raw)
	}
	var body v1alpha1.CarbonRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	stamp := body.Timestamp.UTC().Format(time.RFC3339)
	if !strings.HasSuffix(stamp, "Z") {
		t.Fatalf("timestamp must be RFC3339 Z, got %s", stamp)
	}
}

func TestForecastContractDurationAnd404(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/NOT_A_ZONE/forecast", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid zone forecast: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/zones/east/forecast?duration=300s", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown zone forecast: %d %s", rec.Code, rec.Body.String())
	}
	var st v1alpha1.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Details == nil || st.Details.Name != "east" || st.Details.Kind != "Zone" {
		t.Fatalf("details: %+v", st.Details)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast?duration=-5s", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative duration: %d", rec.Code)
	}

	for _, q := range []string{"0s", "2h", "300s"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast?duration="+q, nil)
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("duration %s: %d %s", q, rec.Code, rec.Body.String())
		}
		var fc v1alpha1.CarbonForecast
		if err := json.Unmarshal(rec.Body.Bytes(), &fc); err != nil {
			t.Fatal(err)
		}
		if fc.Kind != v1alpha1.KindCarbonForecast || fc.Unit != v1alpha1.IntensityUnitGCO2ePerKWh {
			t.Fatalf("forecast envelope: %+v", fc)
		}
	}
}

func TestForecastRecordedWindowFilter(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	c := cache.NewWithOptions(cache.Options{Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	flow := true
	rec := v1alpha1.CarbonRecord{
		APIVersion:                 v1alpha1.GroupVersion,
		Kind:                       v1alpha1.KindCarbonRecord,
		Zone:                       "north",
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: 82.4,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		TemporalGranularity:        v1alpha1.Granularity5Minutes,
		IntervalSeconds:            300,
		Quality:                    v1alpha1.QualityFresh,
		FetchedAt:                  v1alpha1.RFC3339Time{Time: now},
		FlowTraced:                 &flow,
	}
	c.Store(&v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: now},
		Quality:   v1alpha1.QualityFresh,
		Zones: map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState{
			"north": {
				Current: rec,
				Forecast: []v1alpha1.ForecastInterval{
					{Timestamp: v1alpha1.RFC3339Time{Time: now}, CarbonIntensityGCO2ePerKWh: 82.4},
					{Timestamp: v1alpha1.RFC3339Time{Time: now.Add(5 * time.Minute)}, CarbonIntensityGCO2ePerKWh: 90, Unit: v1alpha1.IntensityUnitGCO2ePerKWh},
					{Timestamp: v1alpha1.RFC3339Time{Time: now.Add(3 * time.Hour)}, CarbonIntensityGCO2ePerKWh: 110, Unit: v1alpha1.IntensityUnitGCO2ePerKWh},
				},
			},
		},
	})
	srv := New(":0", c, provider.NewMockProvider(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast?duration=7200s", nil)
	recw := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recw, req)
	if recw.Code != http.StatusOK {
		t.Fatalf("status %d %s", recw.Code, recw.Body.String())
	}
	var fc v1alpha1.CarbonForecast
	if err := json.Unmarshal(recw.Body.Bytes(), &fc); err != nil {
		t.Fatal(err)
	}
	if fc.ForecastMode != v1alpha1.ForecastModeRecorded || fc.Horizon != "7200s" {
		t.Fatalf("mode/horizon: %+v", fc)
	}
	if len(fc.Intervals) != 2 {
		t.Fatalf("window filter: %d intervals %#v", len(fc.Intervals), fc.Intervals)
	}
	if fc.Intervals[0].Unit != v1alpha1.IntensityUnitGCO2ePerKWh {
		t.Fatal("empty unit must be filled")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast?duration=300s", nil)
	recw = httptest.NewRecorder()
	srv.Handler().ServeHTTP(recw, req)
	if err := json.Unmarshal(recw.Body.Bytes(), &fc); err != nil {
		t.Fatal(err)
	}
	if len(fc.Intervals) != 1 {
		t.Fatalf("300s window: %d", len(fc.Intervals))
	}
}

func TestGETDoesNotCallProvider(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	inner := provider.NewMockProviderWithClock(clock)
	cp := &countingProvider{inner: inner}
	c := cache.NewWithOptions(cache.Options{
		Provider: cp,
		Clock:    clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := cp.current.Load()
	srv := New(":0", c, cp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/current", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		req = httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast", nil)
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("forecast status %d", rec.Code)
		}
	}
	if cp.current.Load() != before {
		t.Fatalf("GET called provider Current %d extra times", cp.current.Load()-before)
	}
	if !clock.Now().Equal(now) {
		t.Fatal("GET must not advance FakeClock")
	}
}

func TestNewNilLoggerAndNilProvider(t *testing.T) {
	srv := New(":0", cache.New(), nil, nil)
	if srv.mode() != v1alpha1.ProviderModeMock {
		t.Fatalf("mode %s", srv.mode())
	}
	req := httptest.NewRequest(http.MethodGet, v1alpha1.PathHealthz, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestWriteJSONEncodeError(t *testing.T) {
	srv := newTestServer()
	rec := httptest.NewRecorder()
	srv.writeJSON(rec, http.StatusOK, make(chan int))
}

func TestScorePathIsNotRegistered(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/score", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Score endpoint must not exist, got %d", rec.Code)
	}
}

func TestServeListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	srv := New(ln.Addr().String(), cache.New(), provider.NewMockProvider(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Serve(ctx); err == nil {
		t.Fatal("expected listen error on occupied address")
	}
}

func TestZonesEmptyCacheUsesWriteError(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, v1alpha1.PathZones, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content-type %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control %s", rec.Header().Get("Cache-Control"))
	}
}

type countingProvider struct {
	inner   v1alpha1.CarbonProvider
	current atomic.Int64
}

func (p *countingProvider) Name() string                { return p.inner.Name() }
func (p *countingProvider) Mode() v1alpha1.ProviderMode { return p.inner.Mode() }
func (p *countingProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	return p.inner.ListZones(ctx)
}
func (p *countingProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	p.current.Add(1)
	return p.inner.Current(ctx, zone, at)
}
func (p *countingProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	return p.inner.Forecast(ctx, zone, at, horizon)
}
