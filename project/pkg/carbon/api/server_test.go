package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/cache"
	"carbon-aware-k8s/pkg/carbon/provider"
)

func newTestServer() *Server {
	return New(":0", cache.New(), provider.NewMockProvider(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func seededServer(t *testing.T) *Server {
	t.Helper()
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p := provider.NewMockProviderWithClock(clock)
	c := cache.NewWithOptions(cache.Options{
		Provider: p,
		Clock:    clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return New(":0", c, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestHealthzUnavailableWhenEmpty(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, v1alpha1.PathHealthz, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	var body v1alpha1.HealthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != v1alpha1.ServingUnavailable || body.Ready {
		t.Fatalf("body: %+v", body)
	}
	if body.Mode != v1alpha1.ProviderModeMock {
		t.Fatalf("mode: %s", body.Mode)
	}
}

func TestCurrentRejectsInvalidZone(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/NOT_A_ZONE/current", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestForecastRejectsBareDuration(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast?duration=300", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestMetricsOK(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, v1alpha1.PathMetrics, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestServeShutsDownOnCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := New(addr, cache.New(), provider.NewMockProvider(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("server did not start listening")
		}
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete")
	}
}

func TestCurrentAndZonesAfterRefresh(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/current", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control: %s", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("X-Carbon-Quality") != string(v1alpha1.QualityFresh) {
		t.Fatalf("quality header: %s", rec.Header().Get("X-Carbon-Quality"))
	}
	if rec.Header().Get("X-Carbon-Source") != string(v1alpha1.SourceMock) {
		t.Fatalf("source header: %s", rec.Header().Get("X-Carbon-Source"))
	}
	if rec.Header().Get("X-Carbon-Data-Age-Seconds") != "0" {
		t.Fatalf("age header: %s", rec.Header().Get("X-Carbon-Data-Age-Seconds"))
	}
	var body v1alpha1.CarbonRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.APIVersion != v1alpha1.GroupVersion || body.Kind != v1alpha1.KindCarbonRecord {
		t.Fatalf("type meta: %s %s", body.APIVersion, body.Kind)
	}
	if body.CarbonIntensityGCO2ePerKWh != 80 || body.Zone != "north" {
		t.Fatalf("record: %+v", body)
	}

	req = httptest.NewRequest(http.MethodGet, v1alpha1.PathZones, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("zones status %d body %s", rec.Code, rec.Body.String())
	}
	var zones v1alpha1.ZoneProfileList
	if err := json.Unmarshal(rec.Body.Bytes(), &zones); err != nil {
		t.Fatal(err)
	}
	if zones.Kind != v1alpha1.KindZoneProfileList || len(zones.Items) != 3 {
		t.Fatalf("zones: %+v", zones)
	}

	req = httptest.NewRequest(http.MethodGet, v1alpha1.PathHealthz, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status %d", rec.Code)
	}
	var health v1alpha1.HealthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != v1alpha1.ServingOK || !health.Ready || health.ZonesReady != 3 {
		t.Fatalf("health: %+v", health)
	}
}

func TestCurrentUnknownZoneNotFound(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/east/current", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var st v1alpha1.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Reason != v1alpha1.ReasonNotFound || st.Code != 404 {
		t.Fatalf("status body: %+v", st)
	}
}

func TestForecastEmptyArrayAndDurationMax(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var fc v1alpha1.CarbonForecast
	if err := json.Unmarshal(rec.Body.Bytes(), &fc); err != nil {
		t.Fatal(err)
	}
	if fc.Kind != v1alpha1.KindCarbonForecast || fc.ForecastMode != v1alpha1.ForecastModeNone {
		t.Fatalf("forecast: %+v", fc)
	}
	if fc.Horizon != "7200s" {
		t.Fatalf("horizon=%s", fc.Horizon)
	}
	if fc.Intervals == nil || len(fc.Intervals) != 0 {
		t.Fatalf("forecast array must be empty slice, got %#v", fc.Intervals)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/forecast?duration=90000s", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("max duration status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestHealthAliasAndMetricsAfterRefresh(t *testing.T) {
	srv := seededServer(t)
	req := httptest.NewRequest(http.MethodGet, v1alpha1.PathHealth, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health alias %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, v1alpha1.PathMetrics, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "carbon_aware_cds_zones 3") {
		t.Fatalf("metrics: %s", rec.Body.String())
	}
}

func TestMetricsExposeStage7Families(t *testing.T) {
	srv := seededServer(t)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, v1alpha1.PathZones, nil))
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, v1alpha1.PathMetrics, nil))
	body := rec.Body.String()
	for _, want := range []string{
		`carbon_aware_api_requests_total{code="200",endpoint="/api/v1/zones"} 2`,
		`carbon_aware_zone_intensity_gco2_kwh{zone="north"}`,
		`carbon_aware_data_age_seconds `,
		"carbon_aware_cds_zones 3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestStaleLastGoodStill200(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	inner := provider.NewMockProviderWithClock(clock)
	tp := &failingProvider{inner: inner}
	c := cache.NewWithOptions(cache.Options{
		Provider: tp,
		Clock:    clock,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tp.fail = true
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected fail")
	}
	srv := New(":0", c, tp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/current", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Carbon-Quality") != string(v1alpha1.QualityStale) {
		t.Fatalf("quality header: %s", rec.Header().Get("X-Carbon-Quality"))
	}
	req = httptest.NewRequest(http.MethodGet, v1alpha1.PathHealthz, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health %d %s", rec.Code, rec.Body.String())
	}
	var health v1alpha1.HealthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != v1alpha1.ServingDegraded || !health.Ready {
		t.Fatalf("health: %+v", health)
	}
}

type failingProvider struct {
	inner v1alpha1.CarbonProvider
	fail  bool
}

func (p *failingProvider) Name() string                { return p.inner.Name() }
func (p *failingProvider) Mode() v1alpha1.ProviderMode { return p.inner.Mode() }
func (p *failingProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if p.fail {
		return nil, provider.ErrTimeout
	}
	return p.inner.ListZones(ctx)
}
func (p *failingProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if p.fail {
		return v1alpha1.CarbonRecord{}, provider.ErrTimeout
	}
	return p.inner.Current(ctx, zone, at)
}
func (p *failingProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	if p.fail {
		return nil, provider.ErrTimeout
	}
	return p.inner.Forecast(ctx, zone, at, horizon)
}
