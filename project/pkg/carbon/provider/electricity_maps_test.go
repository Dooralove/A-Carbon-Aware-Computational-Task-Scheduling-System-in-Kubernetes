package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestElectricityMapsCurrentAuthAndMap(t *testing.T) {
	var gotAuth, gotZone string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("auth-token")
		gotZone = r.URL.Query().Get("zone")
		if r.URL.Path != "/carbon-intensity/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"zone":"DK-DK2","carbonIntensity":318,
			"datetime":"2024-05-19T03:00:00.000Z",
			"updatedAt":"2025-07-25T12:32:42.039Z",
			"emissionFactorType":"lifecycle","flowTraced":true,
			"isEstimated":false,"temporalGranularity":"hourly"
		}`)
	}))
	defer srv.Close()

	clock := v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z"))
	p, err := newElectricityMaps(Options{
		LiveAPIKey:  "secret-token",
		BaseURL:     srv.URL,
		HTTPClient:  srv.Client(),
		HTTPTimeout: time.Second,
		Clock:       clock,
		SourceZones: map[string]string{"north": "DK-DK2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := p.GetCurrentIntensity(context.Background(), "north")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "secret-token" {
		t.Fatalf("auth-token header: %q", gotAuth)
	}
	if gotZone != "DK-DK2" {
		t.Fatalf("query zone: %q", gotZone)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 318 || rec.Zone != "north" || rec.SourceZone != "DK-DK2" {
		t.Fatalf("record: %+v", rec)
	}
	if rec.Unit != v1alpha1.IntensityUnitGCO2ePerKWh || rec.IntervalSeconds != 3600 {
		t.Fatalf("unit/interval: %+v", rec)
	}
}

func TestElectricityMapsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 40 * time.Millisecond
	p, err := newElectricityMaps(Options{
		LiveAPIKey:   "k",
		BaseURL:      srv.URL,
		HTTPClient:   client,
		HTTPTimeout:  40 * time.Millisecond,
		MaxAttempts:  1,
		RetryBackoff: time.Millisecond,
		Clock:        v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Current(context.Background(), "north", time.Time{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
}

func TestElectricityMapsRetryThenSuccess(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"zone":"north","carbonIntensity":80,"datetime":"2026-09-01T10:00:00Z","emissionFactorType":"lifecycle","temporalGranularity":"5_minutes"}`)
	}))
	defer srv.Close()
	p, err := newElectricityMaps(Options{
		LiveAPIKey:   "k",
		BaseURL:      srv.URL,
		HTTPClient:   srv.Client(),
		MaxAttempts:  3,
		RetryBackoff: time.Millisecond,
		Clock:        v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := p.Current(context.Background(), "north", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 80 {
		t.Fatalf("got %v", rec.CarbonIntensityGCO2ePerKWh)
	}
}

func TestElectricityMapsInvalidJSONAndIntensity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{not json`)
	}))
	defer srv.Close()
	p, err := newElectricityMaps(Options{
		LiveAPIKey:  "k",
		BaseURL:     srv.URL,
		HTTPClient:  srv.Client(),
		MaxAttempts: 1,
		Clock:       v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Current(context.Background(), "north", time.Time{})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("json: %v", err)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"zone":"north","carbonIntensity":9000,"datetime":"2026-09-01T10:00:00Z"}`)
	}))
	defer srv2.Close()
	p2, err := newElectricityMaps(Options{
		LiveAPIKey:  "k",
		BaseURL:     srv2.URL,
		HTTPClient:  srv2.Client(),
		MaxAttempts: 1,
		Clock:       v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p2.Current(context.Background(), "north", time.Time{})
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("ci: %v", err)
	}
}

func TestElectricityMapsForecast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"zone":"north","forecast":[
			{"carbonIntensity":90,"datetime":"2026-09-01T10:05:00Z"},
			{"carbonIntensity":95,"datetime":"2026-09-01T11:00:00Z"}
		]}`)
	}))
	defer srv.Close()
	p, err := newElectricityMaps(Options{
		LiveAPIKey: "k",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
		Clock:      v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	fc, err := p.GetForecast(context.Background(), "north", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc) != 2 {
		t.Fatalf("forecast len %d", len(fc))
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts.UTC()
}
