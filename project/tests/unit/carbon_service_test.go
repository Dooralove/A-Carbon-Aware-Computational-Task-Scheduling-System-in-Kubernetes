package unit_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/api"
	"carbon-aware-k8s/pkg/carbon/cache"
	"carbon-aware-k8s/pkg/carbon/provider"
)

func repoPath(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", rel))
}

func TestReplayFileToCacheToREST(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := v1alpha1.NewFakeClock(now)
	p, err := provider.New(v1alpha1.ProviderModeReplay, provider.Options{
		ReplayDatasetPath: repoPath(t, filepath.Join("pkg", "carbon", "replay", "testdata", "mini.json")),
		Clock:             clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := cache.NewWithOptions(cache.Options{
		Provider: p,
		Clock:    provider.ClockOf(p),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := api.New(":0", c, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := srv.Handler()

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	before := clock.Now()
	cur := get("/api/v1/zones/north/current")
	if cur.Code != http.StatusOK {
		t.Fatalf("current %d %s", cur.Code, cur.Body.String())
	}
	var rec v1alpha1.CarbonRecord
	if err := json.Unmarshal(cur.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 100 || rec.Unit != v1alpha1.IntensityUnitGCO2ePerKWh {
		t.Fatalf("replay current: %+v", rec)
	}
	if rec.APIVersion != v1alpha1.GroupVersion || rec.Kind != v1alpha1.KindCarbonRecord {
		t.Fatalf("type meta: %s %s", rec.APIVersion, rec.Kind)
	}
	if !clock.Now().Equal(before) {
		t.Fatal("REST GET must not advance replay clock")
	}

	unknown := get("/api/v1/zones/east/current")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown zone %d", unknown.Code)
	}
	var st v1alpha1.Status
	if err := json.Unmarshal(unknown.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Reason != v1alpha1.ReasonNotFound || st.Code != 404 {
		t.Fatalf("status: %+v", st)
	}

	zones := get(v1alpha1.PathZones)
	if zones.Code != http.StatusOK {
		t.Fatalf("zones %d %s", zones.Code, zones.Body.String())
	}
	var list v1alpha1.ZoneProfileList
	if err := json.Unmarshal(zones.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Kind != v1alpha1.KindZoneProfileList || len(list.Items) != 2 {
		t.Fatalf("zone list: %+v", list)
	}

	health := get(v1alpha1.PathHealthz)
	if health.Code != http.StatusOK {
		t.Fatalf("healthz %d", health.Code)
	}
	var hs v1alpha1.HealthStatus
	if err := json.Unmarshal(health.Body.Bytes(), &hs); err != nil {
		t.Fatal(err)
	}
	if hs.Mode != v1alpha1.ProviderModeReplay || !hs.Ready || hs.ModelTime == nil {
		t.Fatalf("health: %+v", hs)
	}

	clock.Set(time.Date(2026, 9, 1, 10, 5, 0, 0, time.UTC))
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	cur = get("/api/v1/zones/north/current")
	if err := json.Unmarshal(cur.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 200 {
		t.Fatalf("after 10:05 refresh: %v", rec.CarbonIntensityGCO2ePerKWh)
	}
}

func TestVolatileReplayStepThroughREST(t *testing.T) {
	clock := v1alpha1.NewFakeClock(time.Date(2026, 9, 1, 10, 24, 59, 999000000, time.UTC))
	p, err := provider.New(v1alpha1.ProviderModeReplay, provider.Options{
		ReplayDatasetPath: repoPath(t, filepath.Join("experiments", "datasets", "examples", "synthetic_multizone_volatile_2h_v1.json")),
		Clock:             clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := cache.NewWithOptions(cache.Options{
		Provider: p,
		Clock:    provider.ClockOf(p),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := api.New(":0", c, p, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/current", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var body v1alpha1.CarbonRecord
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CarbonIntensityGCO2ePerKWh != 85 {
		t.Fatalf("before jump: %v", body.CarbonIntensityGCO2ePerKWh)
	}

	clock.Set(time.Date(2026, 9, 1, 10, 25, 0, 0, time.UTC))
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/zones/north/current", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CarbonIntensityGCO2ePerKWh != 420 {
		t.Fatalf("after jump: %v", body.CarbonIntensityGCO2ePerKWh)
	}
}
