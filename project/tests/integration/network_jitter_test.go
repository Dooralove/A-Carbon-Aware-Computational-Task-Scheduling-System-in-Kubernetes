package integration_test

import (
	"context"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/scheduler"
)

// TestNetworkJitterWithinTimeout: every CDS response is delayed 0–300 ms
// (below httpTimeoutSeconds=2). Sync keeps working while 8 goroutines run the
// scoring path and the provider value flips underneath them.
func TestNetworkJitterWithinTimeout(t *testing.T) {
	prov := newTunableProvider(map[string]float64{"north": 80, "west": 400, "central": 240})
	cds := startCDS(t, prov, cdsRefresh, jitter(0, 300*time.Millisecond))
	args := pluginArgs(cds.URL)
	args.RefreshIntervalSeconds = 1
	p := startPlugin(t, args)
	nodes := standNodes()

	if _, err := waitFor(10*time.Second, func() bool { return p.Snapshot() != nil }); err != nil {
		t.Fatalf("initial sync under jitter: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var decisions, failures atomic.Int64
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				d, err := decide(p, carbonPod("jitter"), nodes)
				if err != nil || d.neutral() {
					failures.Add(1)
					continue
				}
				decisions.Add(1)
			}
		}()
	}
	flip := time.NewTicker(400 * time.Millisecond)
	high := false
	for loop := true; loop; {
		select {
		case <-ctx.Done():
			loop = false
		case <-flip.C:
			high = !high
			if high {
				prov.Set("north", 500)
			} else {
				prov.Set("north", 80)
			}
		}
	}
	flip.Stop()
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d scoring cycles failed or fell back under jitter (%d ok)", failures.Load(), decisions.Load())
	}
	t.Logf("scoring cycles under jitter: %d, CDS requests served: %d", decisions.Load(), cds.requests.Load())

	// Converge: with jitter the bound grows by one tick of 2*zones+1 requests.
	prov.Set("north", 77)
	bound := propagationBound + 7*300*time.Millisecond
	lat, err := waitFor(bound+2*time.Second, func() bool {
		ci, ok := snapshotCI(p, "north")
		return ok && ci == 77
	})
	if err != nil {
		t.Fatalf("north=77 not propagated under jitter: %v", err)
	}
	t.Logf("propagation under 0–300ms jitter: %v (bound %v)", lat, bound)
	if lat > bound {
		t.Fatalf("propagation %v exceeds jitter bound %v", lat, bound)
	}
	d, err := decide(p, carbonPod("final"), nodes)
	if err != nil {
		t.Fatal(err)
	}
	if d.bestZone(nodes) != "north" {
		t.Fatalf("north=77 must win, got %v", d.scores)
	}
}

// TestNetworkDelayBeyondTimeoutKeepsLastGood: a CDS that answers slower than
// the client timeout must make the tick fail fast and keep the old snapshot.
func TestNetworkDelayBeyondTimeoutKeepsLastGood(t *testing.T) {
	var delay atomic.Int64
	slow := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if d := time.Duration(delay.Load()); d > 0 {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	prov := newTunableProvider(map[string]float64{"north": 80, "west": 400})
	cds := startCDS(t, prov, cdsRefresh, slow)

	clock := v1alpha1.NewFakeClock(time.Now().UTC())
	local := scheduler.NewLocalCarbonCache(clock, 60, 300)
	const timeout = 200 * time.Millisecond
	client := scheduler.NewCarbonServiceClient(cds.URL, timeout)
	if err := local.UpdateFromService(context.Background(), client); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fetched := local.CarbonSnapshot().FetchedAt

	delay.Store(int64(time.Second))
	prov.Set("north", 500)
	time.Sleep(3 * cdsRefresh)
	for i := 0; i < 3; i++ {
		start := time.Now()
		err := local.UpdateFromService(context.Background(), client)
		el := time.Since(start)
		if err == nil {
			t.Fatal("tick must fail when CDS is slower than httpTimeout")
		}
		if el > timeout+300*time.Millisecond {
			t.Fatalf("tick took %v with timeout %v: Worker must not hang", el, timeout)
		}
	}
	st, ok := local.GetZoneState("north")
	if !ok || !st.IsValid || st.CurrentCIGCO2ePerKWh != 80 {
		t.Fatalf("last-good lost under timeout: ok=%v %+v", ok, st)
	}
	if !local.CarbonSnapshot().FetchedAt.Equal(fetched.Time) {
		t.Fatal("fetchedAt must not move on timed-out ticks")
	}

	delay.Store(0)
	if err := local.UpdateFromService(context.Background(), client); err != nil {
		t.Fatalf("recovery tick: %v", err)
	}
	if st, _ := local.GetZoneState("north"); st.CurrentCIGCO2ePerKWh != 500 {
		t.Fatalf("after recovery north=%v, want 500", st.CurrentCIGCO2ePerKWh)
	}
}

// TestIntermittentJitterNeverPublishesPartialSnapshot: 10 % of requests are
// delayed past the timeout (seeded, deterministic). Concurrent readers must
// only ever see complete snapshots (FF-INV-12, all-or-nothing publish).
func TestIntermittentJitterNeverPublishesPartialSnapshot(t *testing.T) {
	var mu sync.Mutex
	rng := rand.New(rand.NewPCG(8, 1))
	flaky := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			slow := rng.Float64() < 0.10
			mu.Unlock()
			if slow {
				select {
				case <-time.After(300 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
	zones := map[string]float64{"north": 80, "west": 400, "central": 240}
	prov := newTunableProvider(zones)
	cds := startCDS(t, prov, 20*time.Millisecond, flaky)

	local := scheduler.NewLocalCarbonCache(v1alpha1.SystemClock{}, 60, 300)
	client := scheduler.NewCarbonServiceClient(cds.URL, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var reads, partial atomic.Int64
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				snap := local.CarbonSnapshot()
				if snap == nil {
					continue
				}
				reads.Add(1)
				if len(snap.Zones) != len(zones) {
					partial.Add(1)
				}
				for z := range zones {
					if _, ok := local.GetZoneState(z); !ok {
						partial.Add(1)
					}
				}
			}
		}()
	}

	ok, failed := 0, 0
	for i := 0; i < 40; i++ {
		prov.Set("north", float64(80+i))
		if err := local.UpdateFromService(context.Background(), client); err != nil {
			failed++
		} else {
			ok++
		}
	}
	cancel()
	wg.Wait()
	t.Logf("ticks ok=%d failed=%d, concurrent reads=%d", ok, failed, reads.Load())
	if ok == 0 || failed == 0 {
		t.Fatalf("scenario must mix successful and failed ticks (ok=%d failed=%d)", ok, failed)
	}
	if partial.Load() != 0 {
		t.Fatalf("%d partial snapshot observations", partial.Load())
	}
}
