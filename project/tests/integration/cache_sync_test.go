package integration_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/scheduler"
)

const (
	// cdsRefresh is the CDS provider poll period used by the suite (canon 30s, scaled).
	cdsRefresh = 100 * time.Millisecond
	// pluginRefresh is the minimum Worker period expressible in PluginArgs (whole seconds).
	pluginRefresh = time.Second
	// propagationBound is the worst case provider → CDS → Worker → snapshot:
	// one CDS tick + one Worker tick + scheduling slack for -race.
	propagationBound = cdsRefresh + pluginRefresh + time.Second
)

// TestCacheSyncDynamicProviderChange: North 80 → 500 at the provider must reach
// the plugin snapshot and flip the placement decision from north to west.
func TestCacheSyncDynamicProviderChange(t *testing.T) {
	prov := newTunableProvider(map[string]float64{"north": 80, "west": 400})
	cds := startCDS(t, prov, cdsRefresh, nil)
	args := pluginArgs(cds.URL)
	args.RefreshIntervalSeconds = 1
	p := startPlugin(t, args)
	nodes := standNodes()

	if _, err := waitFor(5*time.Second, func() bool {
		ci, ok := snapshotCI(p, "north")
		return ok && ci == 80
	}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	d, err := decide(p, carbonPod("before"), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if d.neutral() || d.bestZone(nodes) != "north" {
		t.Fatalf("north=80 west=400: want north preferred, got best=%s scores=%v", d.best, d.scores)
	}

	prov.Set("north", 500)
	latency, err := waitFor(5*time.Second, func() bool {
		ci, ok := snapshotCI(p, "north")
		return ok && ci == 500
	})
	if err != nil {
		t.Fatalf("north=500 never reached plugin cache: %v", err)
	}
	t.Logf("provider → plugin propagation: %v (bound %v)", latency, propagationBound)
	if latency > propagationBound {
		t.Fatalf("propagation %v exceeds bound %v", latency, propagationBound)
	}

	// The CDS REST view and the plugin view must agree.
	client := scheduler.NewCarbonServiceClient(cds.URL, time.Second)
	rec, err := client.Current(context.Background(), "north")
	if err != nil || rec.CarbonIntensityGCO2ePerKWh != 500 {
		t.Fatalf("CDS /current north: ci=%v err=%v", rec.CarbonIntensityGCO2ePerKWh, err)
	}

	d, err = decide(p, carbonPod("after"), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if d.neutral() || d.bestZone(nodes) != "west" {
		t.Fatalf("north=500 west=400: want west preferred, got best=%s scores=%v", d.best, d.scores)
	}
	for _, n := range []string{"worker-01", "worker-02"} {
		for _, w := range []string{"worker-03", "worker-04"} {
			if d.scores[n] >= d.scores[w] {
				t.Fatalf("after spike %s (north) must score below %s (west): %v", n, w, d.scores)
			}
		}
	}
}

// TestCacheSyncPropagationLatency measures provider → plugin delay over
// several changes and checks every sample against the bound.
func TestCacheSyncPropagationLatency(t *testing.T) {
	prov := newTunableProvider(map[string]float64{"north": 80, "west": 400, "central": 240})
	cds := startCDS(t, prov, cdsRefresh, nil)
	args := pluginArgs(cds.URL)
	args.RefreshIntervalSeconds = 1
	p := startPlugin(t, args)

	if _, err := waitFor(5*time.Second, func() bool { _, ok := snapshotCI(p, "north"); return ok }); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	values := []float64{500, 81, 650, 120, 900, 95}
	samples := make([]time.Duration, 0, len(values))
	for _, v := range values {
		prov.Set("north", v)
		lat, err := waitFor(5*time.Second, func() bool {
			ci, ok := snapshotCI(p, "north")
			return ok && ci == v
		})
		if err != nil {
			t.Fatalf("north=%v not propagated: %v", v, err)
		}
		samples = append(samples, lat)
	}
	t.Logf("propagation latency n=%d p50=%v p95=%v max=%v (CDS tick %v, Worker tick %v)",
		len(samples), percentile(samples, 0.5), percentile(samples, 0.95), percentile(samples, 1), cdsRefresh, pluginRefresh)
	for i, s := range samples {
		if s > propagationBound {
			t.Fatalf("sample %d: %v exceeds bound %v", i, s, propagationBound)
		}
	}
}

// TestCacheValidityWhenServiceUnavailable checks the validity flags of the
// plugin cache when CDS disappears, and the SM-FRESH → SM-STALE → SM-DEAD
// transitions at the canonical thresholds (90 s / 300 s).
func TestCacheValidityWhenServiceUnavailable(t *testing.T) {
	prov := newTunableProvider(map[string]float64{"north": 80, "west": 400})
	cds := startCDS(t, prov, cdsRefresh, nil)
	args := pluginArgs(cds.URL)

	clock := v1alpha1.NewFakeClock(time.Now().UTC())
	local := scheduler.NewLocalCarbonCache(clock, args.ClockSkewSeconds, args.MaxDataAgeSeconds)
	client := scheduler.NewCarbonServiceClient(cds.URL, 500*time.Millisecond)

	if err := local.UpdateFromService(context.Background(), client); err != nil {
		t.Fatalf("seed tick: %v", err)
	}
	st, ok := local.GetZoneState("north")
	if !ok || !st.IsValid || st.CurrentCIGCO2ePerKWh != 80 {
		t.Fatalf("north after seed: ok=%v state=%+v", ok, st)
	}
	fetchedAt := local.CarbonSnapshot().FetchedAt

	cds.Close()
	start := time.Now()
	if err := local.UpdateFromService(context.Background(), client); err == nil {
		t.Fatal("tick against a closed CDS must fail")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("failed tick took %v; Worker must be bounded by httpTimeout", el)
	}
	st, ok = local.GetZoneState("north")
	if !ok || !st.IsValid || st.CurrentCIGCO2ePerKWh != 80 {
		t.Fatalf("last-good must survive CDS outage (FF-INV-12): ok=%v state=%+v", ok, st)
	}
	if got := local.CarbonSnapshot().FetchedAt; !got.Equal(fetchedAt.Time) {
		t.Fatalf("fetchedAt moved on failed tick: %v → %v", fetchedAt, got)
	}

	p, err := scheduler.NewForTests(args, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.SeedSnapshot(*local.CarbonSnapshot())
	nodes := standNodes()

	steps := []struct {
		name    string
		age     time.Duration
		scoring bool
	}{
		{"fresh", 0, true},
		{"stale-in-window", 91 * time.Second, true},
		{"stale-at-limit", 300 * time.Second, true},
		{"dead", 301 * time.Second, false},
	}
	for _, s := range steps {
		clock.Set(fetchedAt.Time.Add(s.age))
		if age, ok := local.SnapshotAge(clock.Now()); !ok || age != s.age {
			t.Fatalf("%s: SnapshotAge=%v ok=%v", s.name, age, ok)
		}
		deadBefore := counterValue(t, fallbackMetricNames, "reason", "dead")
		d, err := decide(p, carbonPod(s.name), nodes)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		deadAfter := counterValue(t, fallbackMetricNames, "reason", "dead")
		if s.scoring {
			if d.neutral() || d.bestZone(nodes) != "north" {
				t.Fatalf("%s (age %v): carbon scoring must stay on, got %v", s.name, s.age, d.scores)
			}
			if deadAfter != deadBefore {
				t.Fatalf("%s: fallback{dead} must not grow", s.name)
			}
			continue
		}
		if !d.neutral() {
			t.Fatalf("%s (age %v): G-08 must give equal scores, got %v", s.name, s.age, d.scores)
		}
		if deadAfter != deadBefore+1 {
			t.Fatalf("%s: fallback{dead} %v → %v, want +1", s.name, deadBefore, deadAfter)
		}
	}

	// Cold plugin (never synced) is FF-EMPTY: neutral and counted as empty.
	cold, err := scheduler.NewForTests(args, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyBefore := counterValue(t, fallbackMetricNames, "reason", "empty")
	d, err := decide(cold, carbonPod("cold"), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if !d.neutral() {
		t.Fatalf("cold cache must be neutral, got %v", d.scores)
	}
	if got := counterValue(t, fallbackMetricNames, "reason", "empty"); got != emptyBefore+1 {
		t.Fatalf("fallback{empty} %v → %v, want +1", emptyBefore, got)
	}
}

// TestWorkerOutageAndRecovery runs the real Worker goroutine: CDS answers 503,
// the plugin keeps its last-good snapshot, then CDS recovers and the snapshot
// moves forward again.
func TestWorkerOutageAndRecovery(t *testing.T) {
	var down atomic.Bool
	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if down.Load() {
				http.Error(w, `{"reason":"ServiceUnavailable"}`, http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	prov := newTunableProvider(map[string]float64{"north": 80, "west": 400})
	cds := startCDS(t, prov, cdsRefresh, gate)
	args := pluginArgs(cds.URL)
	args.RefreshIntervalSeconds = 1
	p := startPlugin(t, args)

	if _, err := waitFor(5*time.Second, func() bool { return p.Snapshot() != nil }); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	fetched := p.Snapshot().FetchedAt.Time

	failures := func() float64 {
		return counterValue(t, []string{"carbon_aware_sync_failure_total"}, "", "")
	}
	before := failures()
	down.Store(true)
	prov.Set("north", 500) // must NOT be visible while CDS is down
	if _, err := waitFor(5*time.Second, func() bool { return failures() >= before+2 }); err != nil {
		t.Fatalf("Worker did not record failing ticks: %v", err)
	}
	snap := p.Snapshot()
	if snap == nil || !snap.FetchedAt.Equal(fetched) {
		t.Fatalf("last-good snapshot replaced during outage: %+v", snap)
	}
	if ci, _ := snapshotCI(p, "north"); ci != 80 {
		t.Fatalf("north=%v during outage, want last-good 80", ci)
	}
	d, err := decide(p, carbonPod("outage"), standNodes())
	if err != nil {
		t.Fatal(err)
	}
	if d.neutral() || d.bestZone(standNodes()) != "north" {
		t.Fatalf("young last-good must keep scoring: %v", d.scores)
	}

	down.Store(false)
	if _, err := waitFor(propagationBound+time.Second, func() bool {
		ci, ok := snapshotCI(p, "north")
		return ok && ci == 500
	}); err != nil {
		t.Fatalf("Worker did not recover after CDS came back: %v", err)
	}
	if !p.Snapshot().FetchedAt.After(fetched) {
		t.Fatal("fetchedAt must advance after recovery")
	}
}
