package metrics

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"carbon-aware-k8s/api/v1alpha1"
)

type stubSource struct {
	snap *v1alpha1.CarbonSnapshot
	now  time.Time
}

func (s stubSource) Load() (*v1alpha1.CarbonSnapshot, error) {
	if s.snap == nil {
		return nil, v1alpha1.ErrEmptyCache
	}
	return s.snap, nil
}

func (s stubSource) SnapshotAge(now time.Time) (time.Duration, bool) {
	if s.snap == nil {
		return 0, false
	}
	return now.Sub(s.snap.FetchedAt.Time), true
}

func (s stubSource) Clock() v1alpha1.Clock { return v1alpha1.NewFakeClock(s.now) }

func record(zone string, ci float64, q v1alpha1.DataQuality) v1alpha1.ZoneCarbonState {
	return v1alpha1.ZoneCarbonState{Current: v1alpha1.CarbonRecord{
		Zone:                       v1alpha1.ZoneID(zone),
		Timestamp:                  v1alpha1.RFC3339Time{Time: time.Date(2026, 10, 18, 11, 55, 0, 0, time.UTC)},
		CarbonIntensityGCO2ePerKWh: ci,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceMock,
		EmissionFactorType:         v1alpha1.EmissionLifecycle,
		Quality:                    q,
	}}
}

// parse returns gauge/counter values keyed as name{label="v",...}.
func parse(t *testing.T, m *CDSMetrics) map[string]float64 {
	t.Helper()
	var buf bytes.Buffer
	if err := m.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(&buf)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for name, mf := range fams {
		for _, mt := range mf.GetMetric() {
			key := name
			if len(mt.GetLabel()) > 0 {
				parts := make([]string, 0, len(mt.GetLabel()))
				for _, l := range mt.GetLabel() {
					parts = append(parts, l.GetName()+`="`+l.GetValue()+`"`)
				}
				key += "{" + strings.Join(parts, ",") + "}"
			}
			switch {
			case mt.GetGauge() != nil:
				out[key] = mt.GetGauge().GetValue()
			case mt.GetCounter() != nil:
				out[key] = mt.GetCounter().GetValue()
			}
		}
	}
	return out
}

func TestCDSCollectorExportsZoneIntensityAndDataAge(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	snap := &v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: now.Add(-12 * time.Second)},
		Zones: map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState{
			"north": record("north", 80, v1alpha1.QualityFresh),
			"west":  record("west", 500, v1alpha1.QualityStale),
			"bad":   record("bad", 999, v1alpha1.QualityInvalid),
		},
	}
	samples := parse(t, NewCDSMetrics(stubSource{snap: snap, now: now}))

	if v, ok := samples[ZoneIntensityName+`{zone="north"}`]; !ok || v != 80 {
		t.Fatalf("north intensity: %v %v", v, ok)
	}
	if v, ok := samples[ZoneIntensityName+`{zone="west"}`]; !ok || v != 500 {
		t.Fatalf("west intensity: %v %v", v, ok)
	}
	if _, ok := samples[ZoneIntensityName+`{zone="bad"}`]; ok {
		t.Fatal("invalid records must not be exported")
	}
	if v, ok := samples[DataAgeName]; !ok || v != 12 {
		t.Fatalf("data age: %v %v", v, ok)
	}
}

func TestCDSCollectorSilentWhileCacheEmpty(t *testing.T) {
	samples := parse(t, NewCDSMetrics(stubSource{now: time.Now()}))
	for k := range samples {
		if strings.HasPrefix(k, ZoneIntensityName) || strings.HasPrefix(k, DataAgeName) {
			t.Fatalf("unexpected %s on empty cache", k)
		}
	}
}

func TestInstrumentCountsRequestsByRouteAndCode(t *testing.T) {
	m := NewCDSMetrics(stubSource{now: time.Now()})
	ok := m.Instrument("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	missing := m.Instrument("/missing", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	for i := 0; i < 3; i++ {
		ok(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
	}
	missing(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	if got := testutil.ToFloat64(m.apiRequests.WithLabelValues("/ok", "200")); got != 3 {
		t.Fatalf("/ok 200 = %v", got)
	}
	if got := testutil.ToFloat64(m.apiRequests.WithLabelValues("/missing", "404")); got != 1 {
		t.Fatalf("/missing 404 = %v", got)
	}
	if got := testutil.ToFloat64(m.apiRequests.WithLabelValues("/missing", "200")); got != 0 {
		t.Fatalf("/missing 200 = %v", got)
	}
}

func TestWriteTextIsValidExposition(t *testing.T) {
	m := NewCDSMetrics(stubSource{now: time.Now()})
	m.Instrument("/x", func(http.ResponseWriter, *http.Request) {})
	var buf bytes.Buffer
	if err := m.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	p := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := p.TextToMetricFamilies(&buf)
	if err != nil {
		t.Fatalf("invalid text format: %v", err)
	}
	if _, ok := fams[APIRequestsTotalName]; !ok {
		t.Fatalf("missing %s", APIRequestsTotalName)
	}
}

func TestRecordDecision(t *testing.T) {
	before := testutil.ToFloat64(SchedulerDecisionsTotal.WithLabelValues("north", "n1", "expected-cf"))
	RecordDecision(Decision{Node: "n1", Zone: "north", ScoringMode: "expected-cf", HasEstimate: true, CarbonGCO2e: 4, EnergyKWh: 0.05})
	if got := testutil.ToFloat64(SchedulerDecisionsTotal.WithLabelValues("north", "n1", "expected-cf")); got != before+1 {
		t.Fatalf("decisions=%v", got)
	}
	if got := testutil.ToFloat64(PodEstimatedEnergyKWh.WithLabelValues("north", "expected-cf")); got != 0.05 {
		t.Fatalf("energy=%v", got)
	}
	unknownBefore := testutil.ToFloat64(SchedulerDecisionsTotal.WithLabelValues(UnknownLabel, UnknownLabel, "current-ci"))
	RecordDecision(Decision{ScoringMode: "current-ci"})
	if got := testutil.ToFloat64(SchedulerDecisionsTotal.WithLabelValues(UnknownLabel, UnknownLabel, "current-ci")); got != unknownBefore+1 {
		t.Fatalf("unknown decisions=%v", got)
	}
}

func TestFallbackReasonsPreRegistered(t *testing.T) {
	if n := testutil.CollectAndCount(SchedulerFallbackTotal); n < 4 {
		t.Fatalf("fallback series=%d, want >= 4", n)
	}
}

func TestSchedulerMetricNamesPassLint(t *testing.T) {
	for _, c := range SchedulerCollectors() {
		problems, err := testutil.CollectAndLint(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems {
			// Domain units (gco2, kwh) are not base units in promlint's list;
			// names are fixed by metrics.md §2a.
			if strings.Contains(p.Text, "base unit") || strings.Contains(p.Text, "non-base") {
				continue
			}
			t.Errorf("%s: %s", p.Metric, p.Text)
		}
	}
}

func TestObservePluginDurationAllocationFree(t *testing.T) {
	allocs := testing.AllocsPerRun(1000, func() {
		ObservePluginDuration(PointScore, 120*time.Nanosecond)
	})
	if allocs != 0 {
		t.Fatalf("allocs/op=%v", allocs)
	}
}

var errBoom = errors.New("boom")

type errSource struct{ stubSource }

func (errSource) Load() (*v1alpha1.CarbonSnapshot, error) { return nil, errBoom }

func TestCollectorToleratesLoadError(t *testing.T) {
	samples := parse(t, NewCDSMetrics(errSource{}))
	if _, ok := samples[DataAgeName]; ok {
		t.Fatal("no data age on load error")
	}
}
