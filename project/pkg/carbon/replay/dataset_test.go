package replay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected read error")
	}
}

func TestParseJSONRejects(t *testing.T) {
	cases := []string{
		`{`,
		`{"schemaVersion":"9.9.9","unit":"gCO2e/kWh","modelStart":"2026-09-01T10:00:00Z","modelEnd":"2026-09-01T10:10:00Z","intervalSeconds":300,"series":[]}`,
		`{"unit":"kg/kWh","modelStart":"2026-09-01T10:00:00Z","modelEnd":"2026-09-01T10:10:00Z","intervalSeconds":300,"series":[]}`,
		`{"unit":"gCO2e/kWh","modelStart":"2026-09-01T10:10:00Z","modelEnd":"2026-09-01T10:00:00Z","intervalSeconds":300,"series":[]}`,
		`{"unit":"gCO2e/kWh","modelStart":"2026-09-01T10:00:00Z","modelEnd":"2026-09-01T10:10:00Z","series":[]}`,
		`{"unit":"gCO2e/kWh","modelStart":"not-a-time","modelEnd":"2026-09-01T10:10:00Z","intervalSeconds":300,"series":[]}`,
		`{"unit":"gCO2e/kWh","modelStart":"2026-09-01T10:00:00Z","modelEnd":"2026-09-01T10:10:00Z","intervalSeconds":300,"zones":[{"zone":"BAD_ZONE"}],"series":[]}`,
	}
	for i, raw := range cases {
		if _, err := parseJSON([]byte(raw)); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestParseJSONGranularityFromInterval(t *testing.T) {
	raw := `{
		"datasetId":"gran_v1","unit":"gCO2e/kWh",
		"modelStart":"2026-09-01T10:00:00Z","modelEnd":"2026-09-01T11:00:00Z",
		"intervalSeconds":900,
		"series":[{"zone":"north","timestamp":"2026-09-01T10:00:00Z","carbonIntensity":10}]
	}`
	ds, err := parseJSON([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Granularity != v1alpha1.Granularity15Minutes {
		t.Fatalf("granularity %s", ds.Granularity)
	}
	if ds.Source != v1alpha1.SourceHistoricalDataset {
		t.Fatalf("source %s", ds.Source)
	}
}

func TestParseCSVErrorsAndSeries(t *testing.T) {
	if _, err := parseCSV([]byte("zone,timestamp\n")); err == nil {
		t.Fatal("header-only")
	}
	if _, err := parseCSV([]byte("foo,bar\n1,2\n")); err == nil {
		t.Fatal("missing columns")
	}
	if _, err := parseCSV([]byte("zone,timestamp,carbonIntensity\nnorth,nope,80\n")); err == nil {
		t.Fatal("bad timestamp")
	}
	if _, err := parseCSV([]byte("zone,timestamp,carbonIntensity\nnorth,2026-09-01T10:00:00Z,nope\n")); err == nil {
		t.Fatal("bad intensity")
	}
	if _, err := parseCSV([]byte("zone,timestamp,carbonIntensity\nnorth,2026-09-01T10:00:00Z,80\nnorth,2026-09-01T10:00:00Z,90\n")); err == nil {
		t.Fatal("duplicate timestamps")
	}
	ds, err := parseCSV([]byte("zone,timestamp,carbonIntensity\nnorth,2026-09-01T10:00:00.500Z,80\nwest,2026-09-01T10:00:00Z,400\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.series[v1alpha1.ZoneID("north")]) != 1 || ds.series[v1alpha1.ZoneID("north")][0].CarbonIntensityGCO2ePerKWh != 80 {
		t.Fatalf("csv series: %+v", ds.series)
	}
}

func TestEngineMetadataAndListZones(t *testing.T) {
	eng, err := Open(testdata("mini.json"), Options{Clock: v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	if eng.Path() == "" || eng.DatasetID() != "testdata_mini_v1" {
		t.Fatalf("meta path=%s id=%s", eng.Path(), eng.DatasetID())
	}
	if eng.Name() != "historical-dataset" || eng.Mode() != v1alpha1.ProviderModeReplay {
		t.Fatalf("provider id %s %s", eng.Name(), eng.Mode())
	}
	if eng.Clock() == nil {
		t.Fatal("clock")
	}
	zones, err := eng.ListZones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 2 {
		t.Fatalf("zones %d", len(zones))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := eng.ListZones(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list: %v", err)
	}
}

func TestRecordedForecastSeries(t *testing.T) {
	eng, err := Open(testdata("recorded.json"), Options{
		Clock: v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")),
	})
	if err != nil {
		t.Fatal(err)
	}
	fc, err := eng.Forecast(context.Background(), "north", mustParse(t, "2026-09-01T10:10:00Z"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(fc) != 2 {
		t.Fatalf("recorded points: %d", len(fc))
	}
	if fc[0].CarbonIntensityGCO2ePerKWh != 110 || fc[1].CarbonIntensityGCO2ePerKWh != 120 {
		t.Fatalf("ci: %+v", fc)
	}
	empty, err := eng.Forecast(context.Background(), "north", mustParse(t, "2026-09-01T10:00:00Z"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("before issuedAt must be empty, got %d", len(empty))
	}
}

func TestOpenMissingAndScaledDefaults(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing.json"), Options{}); err == nil {
		t.Fatal("open missing")
	}
	ds, err := LoadFile(testdata("mini.json"))
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(ds, Options{Multiplier: 0})
	if eng.Clock() == nil {
		t.Fatal("default scaled clock")
	}
	sc := NewScaledClock(nil, ds.ModelStart, 0)
	if sc.Multiplier() != 1 {
		t.Fatalf("multiplier %v", sc.Multiplier())
	}
}

func TestNilDatasetAccessors(t *testing.T) {
	e := &Engine{}
	if e.Path() != "" || e.DatasetID() != "" {
		t.Fatal("nil dataset accessors")
	}
}

func TestLoadCSVFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "row.csv")
	body := "zone,timestamp,carbonIntensity\nnorth,2026-09-01T10:00:00Z,80\nnorth,2026-09-01T10:05:00Z,90\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := Open(p, Options{Clock: v1alpha1.NewFakeClock(mustParse(t, "2026-09-01T10:00:00Z")), Loop: LoopLinear})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := eng.Current(context.Background(), "north", mustParse(t, "2026-09-01T10:04:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 80 {
		t.Fatalf("step series from csv: %v", rec.CarbonIntensityGCO2ePerKWh)
	}
	rec, err = eng.Current(context.Background(), "north", mustParse(t, "2026-09-01T10:05:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.CarbonIntensityGCO2ePerKWh != 90 {
		t.Fatalf("second sample: %v", rec.CarbonIntensityGCO2ePerKWh)
	}
}
