package replay

import (
	"context"
	"math"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

func testdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", name)
}

func exampleVolatile(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(file), "..", "..", "..", "experiments", "datasets", "examples", "synthetic_multizone_volatile_2h_v1.json")
	return p
}

func TestLoadJSONAndCSV(t *testing.T) {
	js, err := LoadFile(testdata("mini.json"))
	if err != nil {
		t.Fatal(err)
	}
	if js.DatasetID != "testdata_mini_v1" || len(js.series[v1alpha1.ZoneID("north")]) != 2 {
		t.Fatalf("json dataset: %+v", js.DatasetID)
	}

	csv, err := LoadFile(testdata("mini.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(csv.series[v1alpha1.ZoneID("north")]) != 2 {
		t.Fatalf("csv north samples: %d", len(csv.series[v1alpha1.ZoneID("north")]))
	}
}

func TestLoadRejectsInvalidIntensity(t *testing.T) {
	_, err := parseJSON([]byte(`{
		"schemaVersion":"1.0.0","datasetId":"bad_ci_v1","source":"synthetic",
		"unit":"gCO2e/kWh","emissionFactorType":"lifecycle",
		"temporalGranularity":"5_minutes","intervalSeconds":300,
		"modelStart":"2026-09-01T10:00:00Z","modelEnd":"2026-09-01T10:05:00Z",
		"forecastMode":"none","zones":[{"zone":"north"}],
		"series":[{"zone":"north","timestamp":"2026-09-01T10:00:00Z","carbonIntensity":9000}]
	}`))
	if err == nil {
		t.Fatal("expected G-10 rejection")
	}
}

func TestStepLookupMillisecondBoundary(t *testing.T) {
	eng, err := Open(testdata("mini.json"), Options{
		Clock:  v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
		Lookup: LookupStep,
		Loop:   LoopLinear,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	zone := v1alpha1.ZoneID("north")

	before := mustParse(t, "2026-09-01T10:04:59.999Z")
	rec, err := eng.Current(ctx, zone, before)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 100 {
		t.Fatalf("10:04:59.999: got %v want 100", rec.CarbonIntensityGCO2ePerKWh)
	}

	at := mustParse(t, "2026-09-01T10:05:00.000Z")
	rec, err = eng.Current(ctx, zone, at)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 200 {
		t.Fatalf("10:05:00.000: got %v want 200", rec.CarbonIntensityGCO2ePerKWh)
	}

	ptr, err := eng.GetCurrentIntensity(ctx, "north")
	if err != nil {
		t.Fatal(err)
	}
	if ptr.CarbonIntensityGCO2ePerKWh != 100 {
		t.Fatalf("clock now: got %v", ptr.CarbonIntensityGCO2ePerKWh)
	}
}

func TestLinearInterpolation(t *testing.T) {
	eng, err := Open(testdata("mini.json"), Options{
		Clock:  v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
		Lookup: LookupLinear,
	})
	if err != nil {
		t.Fatal(err)
	}
	mid := mustParse(t, "2026-09-01T10:02:30Z")
	rec, err := eng.Current(context.Background(), v1alpha1.ZoneID("north"), mid)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rec.CarbonIntensityGCO2ePerKWh-150) > 1e-9 {
		t.Fatalf("linear mid-interval: got %v want 150", rec.CarbonIntensityGCO2ePerKWh)
	}
}

func TestLoopCycleAndLinearClamp(t *testing.T) {
	ctx := context.Background()
	start := mustParse(t, "2026-09-01T10:00:00Z")

	cycle, err := Open(testdata("mini.json"), Options{Clock: v1alpha1.NewFakeClock(start), Loop: LoopCycle})
	if err != nil {
		t.Fatal(err)
	}
	// window is 10 minutes; 10:12 → 10:02 still first interval (100).
	rec, err := cycle.Current(ctx, "north", mustParse(t, "2026-09-01T10:12:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 100 {
		t.Fatalf("cycle: got %v", rec.CarbonIntensityGCO2ePerKWh)
	}

	linear, err := Open(testdata("mini.json"), Options{Clock: v1alpha1.NewFakeClock(start), Loop: LoopLinear})
	if err != nil {
		t.Fatal(err)
	}
	rec, err = linear.Current(ctx, "north", mustParse(t, "2026-09-01T11:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 200 {
		t.Fatalf("linear clamp last sample: got %v", rec.CarbonIntensityGCO2ePerKWh)
	}
}

func TestTimeMultiplier(t *testing.T) {
	wall := v1alpha1.NewFakeClock(mustParse(t, "2026-01-01T00:00:00Z"))
	origin := mustParse(t, "2026-09-01T10:00:00Z")
	scaled := NewScaledClock(wall, origin, 60)
	eng, err := Open(testdata("mini.json"), Options{Clock: scaled, Loop: LoopLinear})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := eng.GetCurrentIntensity(context.Background(), "north")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 100 {
		t.Fatalf("t0: %v", rec.CarbonIntensityGCO2ePerKWh)
	}
	wall.Advance(5 * time.Second) // *60 = 5 minutes of model time
	rec, err = eng.GetCurrentIntensity(context.Background(), "north")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 200 {
		t.Fatalf("after 5s wall / 5min model: got %v want 200", rec.CarbonIntensityGCO2ePerKWh)
	}
}

func TestGetDoesNotAdvanceClock(t *testing.T) {
	clock := v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z"))
	eng, err := Open(testdata("mini.json"), Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	before := clock.Now()
	for i := 0; i < 5; i++ {
		if _, err := eng.GetCurrentIntensity(context.Background(), "north"); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.GetForecast(context.Background(), "north", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if !clock.Now().Equal(before) {
		t.Fatalf("GET advanced clock: %s -> %s", before, clock.Now())
	}
}

func TestForecastNoneIsEmpty(t *testing.T) {
	eng, err := Open(testdata("mini.json"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	fc, err := eng.Forecast(context.Background(), "north", time.Time{}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc) != 0 {
		t.Fatalf("none/persistence must be empty, got %d", len(fc))
	}
}

func TestVolatileFixtureStepAndRecordedForecast(t *testing.T) {
	eng, err := Open(exampleVolatile(t), Options{
		Clock: v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rec, err := eng.Current(ctx, "north", mustParse(t, "2026-09-01T10:24:59.999Z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 85 {
		t.Fatalf("before jump: %v", rec.CarbonIntensityGCO2ePerKWh)
	}
	rec, err = eng.Current(ctx, "north", mustParse(t, "2026-09-01T10:25:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 420 {
		t.Fatalf("after jump: %v", rec.CarbonIntensityGCO2ePerKWh)
	}
	fc, err := eng.Forecast(ctx, "west", mustParse(t, "2026-09-01T10:00:00Z"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc) == 0 {
		t.Fatal("recorded forecast for west must be non-empty")
	}
}

func TestConcurrentLookupRace(t *testing.T) {
	eng, err := Open(testdata("mini.json"), Options{
		Clock: v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := eng.Current(ctx, "north", mustParse(t, "2026-09-01T10:03:00.123Z"))
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		t.Fatal(err)
	}
	return ts.UTC()
}
