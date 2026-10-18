package e2e_test

// Runs without the e2e tag and without a cluster: it keeps the E2E overlay
// loadable by the real CDS replay engine and the real plugin args validation.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/replay"
)

func TestE2EDatasetsLoadInReplayEngine(t *testing.T) {
	want := map[string]map[v1alpha1.ZoneID]float64{
		"e2e_baseline.json": {"north": 100, "west": 400},
		"e2e_spike.json":    {"north": 900, "west": 100},
	}
	for file, ci := range want {
		eng, err := replay.Open(filepath.Join("manifests", "datasets", file), replay.Options{})
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		// linear loop holds the last sample, so the profile is constant forever.
		for _, at := range []time.Time{time.Now(), time.Now().Add(48 * time.Hour)} {
			for zone, v := range ci {
				rec, err := eng.Current(context.Background(), zone, at)
				if err != nil {
					t.Fatalf("%s %s: %v", file, zone, err)
				}
				if rec.CarbonIntensityGCO2ePerKWh != v {
					t.Fatalf("%s %s=%v, want %v", file, zone, rec.CarbonIntensityGCO2ePerKWh, v)
				}
			}
		}
	}
}

func TestE2ESchedulerConfigPatchIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("manifests", "scheduler-config-patch.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Profiles []struct {
			SchedulerName string `json:"schedulerName"`
			PluginConfig  []struct {
				Name string                         `json:"name"`
				Args v1alpha1.CarbonAwarePluginArgs `json:"args"`
			} `json:"pluginConfig"`
		} `json:"profiles"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data["scheduler-config.yaml"]), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 2 {
		t.Fatalf("want 2 profiles, got %d", len(cfg.Profiles))
	}
	for _, p := range cfg.Profiles {
		for _, pc := range p.PluginConfig {
			if err := pc.Args.Validate(); err != nil {
				t.Errorf("%s: %v", p.SchedulerName, err)
			}
			if pc.Args.MaxDataAgeSeconds != 30 || pc.Args.StaleWarnSeconds != 10 || pc.Args.RefreshIntervalSeconds != 2 {
				t.Errorf("%s: unexpected scaled timings %+v", p.SchedulerName, pc.Args)
			}
			if !strings.HasSuffix(pc.Args.CarbonServiceURL, ".svc:8080") {
				t.Errorf("%s: carbonServiceURL %q", p.SchedulerName, pc.Args.CarbonServiceURL)
			}
		}
	}
}
