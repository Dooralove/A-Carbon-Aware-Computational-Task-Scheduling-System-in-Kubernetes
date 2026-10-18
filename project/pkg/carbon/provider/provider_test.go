package provider

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/config"
)

func miniJSON(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "replay", "testdata", "mini.json")
}

func TestNewSelectsMode(t *testing.T) {
	p, err := New(v1alpha1.ProviderModeMock, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "mock" || p.Mode() != v1alpha1.ProviderModeMock {
		t.Fatalf("mock: %s %s", p.Name(), p.Mode())
	}
	rec, err := p.GetCurrentIntensity(context.Background(), "north")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 80 {
		t.Fatalf("mock north: %v", rec.CarbonIntensityGCO2ePerKWh)
	}

	live, err := New(v1alpha1.ProviderModeLive, Options{LiveAPIKey: "x"})
	if err != nil {
		t.Fatal(err)
	}
	em, ok := live.(*ElectricityMapsProvider)
	if !ok || !em.HasAPIKey() {
		t.Fatal("expected ElectricityMapsProvider with key")
	}

	hist, err := New(v1alpha1.ProviderModeReplay, Options{
		ReplayDatasetPath: miniJSON(t),
		Clock:             v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	h, ok := hist.(*HistoricalDatasetProvider)
	if !ok || h.DatasetPath() == "" {
		t.Fatal("expected HistoricalDatasetProvider path")
	}
	got, err := h.GetCurrentIntensity(context.Background(), "north")
	if err != nil {
		t.Fatal(err)
	}
	if got.CarbonIntensityGCO2ePerKWh != 100 {
		t.Fatalf("replay north at start: %v", got.CarbonIntensityGCO2ePerKWh)
	}
}

func TestNewProviderFromConfig(t *testing.T) {
	cfg := &config.Config{
		Port:              8080,
		CacheTTL:          config.DefaultCacheTTL,
		ProviderType:      v1alpha1.ProviderModeReplay,
		ReplayDatasetPath: miniJSON(t),
	}
	p, err := NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode() != v1alpha1.ProviderModeReplay {
		t.Fatalf("mode %s", p.Mode())
	}
}

func TestLiveRequiresKey(t *testing.T) {
	_, err := New(v1alpha1.ProviderModeLive, Options{})
	if err == nil {
		t.Fatal("expected missing API key")
	}
}

func TestMockUnknownZone(t *testing.T) {
	p := NewMockProviderWithClock(v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")))
	_, err := p.Current(context.Background(), "missing", mustParse(t, "2026-09-01T10:00:00Z"))
	if err == nil {
		t.Fatal("expected unknown zone")
	}
}
