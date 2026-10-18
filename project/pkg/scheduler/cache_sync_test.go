package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestGetZoneStateEmptyIsFallbackReady(t *testing.T) {
	cache := NewLocalCarbonCache(v1alpha1.NewFakeClock(time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)), 60, 300)
	if _, ok := cache.GetZoneState("north"); ok {
		t.Fatal("empty cache must not report a zone (FF-EMPTY)")
	}
	if cache.CarbonSnapshot() != nil {
		t.Fatal("CarbonSnapshot must be nil until the first Store")
	}
}

func TestUpdateFromServiceStoresValidZone(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	srv := newFakeCDS(t, &fakeCDS{
		zones:   []string{"north"},
		current: map[string]v1alpha1.CarbonRecord{"north": validRecord("north", now)},
		forecast: map[string]v1alpha1.CarbonForecast{
			"north": emptyForecast("north", now),
		},
	})
	clock := v1alpha1.NewFakeClock(now)
	cache := NewLocalCarbonCache(clock, 60, 300)
	client := NewCarbonServiceClient(srv.URL, time.Second)

	if err := cache.UpdateFromService(context.Background(), client); err != nil {
		t.Fatalf("UpdateFromService: %v", err)
	}
	got, ok := cache.GetZoneState("north")
	if !ok || !got.IsValid {
		t.Fatalf("GetZoneState: ok=%v state=%+v", ok, got)
	}
	if got.CurrentCIGCO2ePerKWh != 82.4 {
		t.Fatalf("CI=%v", got.CurrentCIGCO2ePerKWh)
	}
	if cache.CarbonSnapshot() == nil || len(cache.CarbonSnapshot().Zones) != 1 {
		t.Fatal("API snapshot must contain the published zone")
	}
}

func TestUpdateFromServiceKeepsLastGoodOnHTTPFailure(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	fake := &fakeCDS{
		zones:   []string{"north"},
		current: map[string]v1alpha1.CarbonRecord{"north": validRecord("north", now)},
		forecast: map[string]v1alpha1.CarbonForecast{
			"north": emptyForecast("north", now),
		},
	}
	srv := newFakeCDS(t, fake)
	cache := NewLocalCarbonCache(v1alpha1.NewFakeClock(now), 60, 300)
	client := NewCarbonServiceClient(srv.URL, time.Second)

	if err := cache.UpdateFromService(context.Background(), client); err != nil {
		t.Fatalf("seed: %v", err)
	}

	failuresBefore := testutil.ToFloat64(carbonAwareSyncFailureTotal)
	fake.setStatus(http.StatusServiceUnavailable)
	plugin := &CarbonAwarePlugin{
		cache:  cache,
		client: client,
		clock:  v1alpha1.NewFakeClock(now),
	}
	plugin.syncTick(context.Background())
	got, ok := cache.GetZoneState("north")
	if !ok || got.CurrentCIGCO2ePerKWh != 82.4 {
		t.Fatalf("last-good must be kept: ok=%v state=%+v", ok, got)
	}
	if testutil.ToFloat64(carbonAwareSyncFailureTotal) <= failuresBefore {
		t.Fatal("carbon_aware_sync_failure_total must increase when the tick does not Store")
	}
}

func TestUpdateFromServiceTimeoutDoesNotStore(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		srv.Close()
	})

	cache := NewLocalCarbonCache(v1alpha1.NewFakeClock(time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)), 60, 300)
	client := NewCarbonServiceClient(srv.URL, 40*time.Millisecond)
	err := cache.UpdateFromService(context.Background(), client)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if _, ok := cache.GetZoneState("north"); ok {
		t.Fatal("timeout must not publish a snapshot")
	}
}

func TestUpdateFromServiceRejectsInvalidCarbonIntensity(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	bad := validRecord("north", now)
	bad.CarbonIntensityGCO2ePerKWh = 2500
	srv := newFakeCDS(t, &fakeCDS{
		zones:    []string{"north"},
		current:  map[string]v1alpha1.CarbonRecord{"north": bad},
		forecast: map[string]v1alpha1.CarbonForecast{"north": emptyForecast("north", now)},
	})
	cache := NewLocalCarbonCache(v1alpha1.NewFakeClock(now), 60, 300)
	err := cache.UpdateFromService(context.Background(), NewCarbonServiceClient(srv.URL, time.Second))
	if err == nil {
		t.Fatal("G-10 invalid CI must not Store")
	}
	if _, ok := cache.GetZoneState("north"); ok {
		t.Fatal("invalid record must not appear in the cache")
	}
}

func TestNewStartsWhenCDSUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	started := make(chan struct{})
	var newErr error
	var plugin fwk.Plugin
	go func() {
		plugin, newErr = New(ctx, mustUnknownArgs(t, argsForCDS(srv.URL)), nil)
		close(started)
	}()

	select {
	case <-started:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("New must not wait for Carbon Data Service")
	}
	if newErr != nil {
		t.Fatalf("New: %v", newErr)
	}
	got, ok := plugin.(*CarbonAwarePlugin)
	if !ok {
		t.Fatalf("type %T", plugin)
	}
	if _, found := got.cache.GetZoneState("north"); found {
		t.Fatal("unavailable CDS must leave the cache empty (Fallback ready)")
	}
}

func TestLocalCarbonCacheGetZoneStateRace(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	fake := &fakeCDS{
		zones:    []string{"north", "west"},
		current:  map[string]v1alpha1.CarbonRecord{"north": validRecord("north", now), "west": validRecord("west", now)},
		forecast: map[string]v1alpha1.CarbonForecast{"north": emptyForecast("north", now), "west": emptyForecast("west", now)},
	}
	srv := newFakeCDS(t, fake)
	cache := NewLocalCarbonCache(v1alpha1.NewFakeClock(now), 60, 300)
	client := NewCarbonServiceClient(srv.URL, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var wg sync.WaitGroup
	var reads atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = cache.GetZoneState("north")
				_, _ = cache.GetZoneState("west")
				_ = cache.CarbonSnapshot()
				reads.Add(1)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				_ = cache.UpdateFromService(ctx, client)
				if j == 10 {
					fake.setStatus(http.StatusGatewayTimeout)
					_ = cache.UpdateFromService(ctx, client)
					fake.setStatus(http.StatusOK)
				}
			}
		}()
	}
	wg.Wait()
	if reads.Load() == 0 {
		t.Fatal("readers did not run")
	}
}

func TestSnapshotAgeKeepsFetchedAtOnFailedTick(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	fake := &fakeCDS{
		zones:    []string{"north"},
		current:  map[string]v1alpha1.CarbonRecord{"north": validRecord("north", now)},
		forecast: map[string]v1alpha1.CarbonForecast{"north": emptyForecast("north", now)},
	}
	srv := newFakeCDS(t, fake)
	clock := v1alpha1.NewFakeClock(now)
	cache := NewLocalCarbonCache(clock, 60, 300)
	client := NewCarbonServiceClient(srv.URL, time.Second)
	if err := cache.UpdateFromService(context.Background(), client); err != nil {
		t.Fatalf("seed: %v", err)
	}

	clock.Advance(90 * time.Second)
	fake.setStatus(http.StatusGatewayTimeout)
	plugin := &CarbonAwarePlugin{cache: cache, client: client, clock: clock}
	plugin.syncTick(context.Background())

	age, ok := cache.SnapshotAge(clock.Now())
	if !ok {
		t.Fatal("last-good snapshot must still expose age")
	}
	if age != 90*time.Second {
		t.Fatalf("age=%s, want 90s (fetchedAt must not move on HTTP failure)", age)
	}
}

func TestGetZoneStateHotPathBudget(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	srv := newFakeCDS(t, &fakeCDS{
		zones:    []string{"north"},
		current:  map[string]v1alpha1.CarbonRecord{"north": validRecord("north", now)},
		forecast: map[string]v1alpha1.CarbonForecast{"north": emptyForecast("north", now)},
	})
	cache := NewLocalCarbonCache(v1alpha1.NewFakeClock(now), 60, 300)
	if err := cache.UpdateFromService(context.Background(), NewCarbonServiceClient(srv.URL, time.Second)); err != nil {
		t.Fatalf("UpdateFromService: %v", err)
	}

	const n = 20000
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, ok := cache.GetZoneState("north"); !ok {
			t.Fatal("zone disappeared")
		}
	}
	avg := time.Since(start) / n
	if avg > 50*time.Microsecond {
		t.Fatalf("GetZoneState avg %s exceeds 50µs budget", avg)
	}
}

func argsForCDS(rawURL string) PluginArgs {
	args := validPluginArgs()
	args.CarbonServiceURL = rawURL
	args.RefreshIntervalSeconds = DefaultCacheSyncIntervalSeconds
	args.HTTPTimeoutSeconds = 1
	return args
}

func validRecord(zone string, now time.Time) v1alpha1.CarbonRecord {
	return v1alpha1.CarbonRecord{
		Zone:                       v1alpha1.ZoneID(zone),
		Timestamp:                  v1alpha1.RFC3339Time{Time: now},
		CarbonIntensityGCO2ePerKWh: 82.4,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		Quality:                    v1alpha1.QualityFresh,
		FreshnessSeconds:           1,
		FetchedAt:                  v1alpha1.RFC3339Time{Time: now},
	}
}

func emptyForecast(zone string, now time.Time) v1alpha1.CarbonForecast {
	return v1alpha1.CarbonForecast{
		Zone:         v1alpha1.ZoneID(zone),
		Unit:         v1alpha1.IntensityUnitGCO2ePerKWh,
		IssuedAt:     v1alpha1.RFC3339Time{Time: now},
		ForecastMode: v1alpha1.ForecastModeNone,
		Intervals:    nil,
	}
}

type fakeCDS struct {
	mu       sync.Mutex
	status   int
	zones    []string
	current  map[string]v1alpha1.CarbonRecord
	forecast map[string]v1alpha1.CarbonForecast
}

func (f *fakeCDS) setStatus(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = code
}

func (f *fakeCDS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	status := f.status
	f.mu.Unlock()
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	path := r.URL.Path
	switch {
	case path == v1alpha1.PathZones:
		f.writeJSON(w, f.zoneList())
	case strings.HasSuffix(path, "/current"):
		zone := zoneFromPath(path)
		rec, ok := f.record(zone)
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.writeJSON(w, rec)
	case strings.HasSuffix(path, "/forecast"):
		zone := zoneFromPath(path)
		fc, ok := f.forecastOf(zone)
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.writeJSON(w, fc)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCDS) zoneList() v1alpha1.ZoneProfileList {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := make([]v1alpha1.ZoneProfile, 0, len(f.zones))
	for _, z := range f.zones {
		items = append(items, v1alpha1.ZoneProfile{
			Zone:             v1alpha1.ZoneID(z),
			Quality:          v1alpha1.QualityFresh,
			HasCurrent:       true,
			HasForecast:      true,
			FreshnessSeconds: 1,
		})
	}
	return v1alpha1.ZoneProfileList{Items: items}
}

func (f *fakeCDS) record(zone string) (v1alpha1.CarbonRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.current[zone]
	return rec, ok
}

func (f *fakeCDS) forecastOf(zone string) (v1alpha1.CarbonForecast, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fc, ok := f.forecast[zone]
	return fc, ok
}

func (f *fakeCDS) writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func zoneFromPath(path string) string {
	path = strings.TrimPrefix(path, "/api/v1/zones/")
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return path
}

func newFakeCDS(t *testing.T, fake *fakeCDS) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return srv
}
