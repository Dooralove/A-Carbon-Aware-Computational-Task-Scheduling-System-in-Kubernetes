// Package metrics holds the Stage 7 Prometheus instrumentation shared by the
// Carbon Data Service and the CarbonAware scheduler plugin (ARCH §6.8, §7.1).
//
// Names are canonized in docs/ssot/metrics.md §2a/§3a (additive amendment
// v1.1.0, ADR-003). Export is best-effort: nothing here returns an error to a
// scheduling or REST code path (ARCH §7.3).
package metrics

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"carbon-aware-k8s/api/v1alpha1"
)

// Metric names (metrics.md §2a, §3a). Kept as constants so dashboards, alert
// rules and tests reference one spelling.
const (
	ZoneIntensityName           = "carbon_aware_zone_intensity_gco2_kwh"
	DataAgeName                 = "carbon_aware_data_age_seconds"
	APIRequestsTotalName        = "carbon_aware_api_requests_total"
	PodEstimatedCarbonName      = "carbon_aware_pod_estimated_carbon_gco2"
	PodEstimatedEnergyName      = "carbon_aware_pod_estimated_energy_kwh"
	SchedulerDecisionsTotalName = "carbon_aware_scheduler_decisions_total"
	SchedulerFallbackTotalName  = "carbon_aware_scheduler_fallback_total"
	PluginDurationName          = "carbon_aware_plugin_duration_seconds"
)

// Extension point label values for PluginDurationName.
const (
	PointPreScore       = "PreScore"
	PointScore          = "Score"
	PointNormalizeScore = "NormalizeScore"
	PointPostBind       = "PostBind"
)

// Fallback reason label values for SchedulerFallbackTotalName. empty, dead and
// all_invalid are G-08 (Score_neutral=50 for all nodes); stale is SM-STALE
// (scoring continues with a Warning). The label keeps them apart, as metrics.md
// §1 requires; opt-out is not a failure and is not counted here.
const (
	FallbackReasonEmpty      = "empty"
	FallbackReasonDead       = "dead"
	FallbackReasonAllInvalid = "all_invalid"
	FallbackReasonStale      = "stale"
)

// UnknownLabel replaces an empty zone or node label value.
const UnknownLabel = "unknown"

// Buckets. Plugin hooks run in 100 ns (Score) to a few ms (PreScore on 50 nodes),
// so DefBuckets (5 ms … 10 s) would put every observation in the first bucket.
var (
	pluginDurationBuckets  = prometheus.ExponentialBuckets(1e-7, 4, 12) // 100 ns … ~0.42 s
	estimatedCarbonBuckets = prometheus.ExponentialBuckets(0.01, 4, 10) // 0.01 … ~2621 gCO2e
)

// ---------------------------------------------------------------------------
// Scheduler plugin metrics (metrics.md §2a).
// ---------------------------------------------------------------------------

var (
	// PodEstimatedCarbonGCO2 is the expected CF of the Pod on the node it was bound to.
	PodEstimatedCarbonGCO2 = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    PodEstimatedCarbonName,
		Help:    "Expected carbon footprint (2.10) of a bound Pod on its selected node, gCO2e.",
		Buckets: estimatedCarbonBuckets,
	}, []string{"zone", "scoring_mode"})

	// PodEstimatedEnergyKWh is the expected marginal energy of the last Pod bound in a zone.
	PodEstimatedEnergyKWh = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: PodEstimatedEnergyName,
		Help: "Expected marginal energy P_pod * T_hat of the last Pod bound in the zone, kWh.",
	}, []string{"zone", "scoring_mode"})

	// SchedulerDecisionsTotal counts successful bindings made by a CarbonAware profile.
	SchedulerDecisionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: SchedulerDecisionsTotalName,
		Help: "Pods bound by a CarbonAware scheduler profile, by selected zone and node.",
	}, []string{"zone", "node", "scoring_mode"})

	// SchedulerFallbackTotal counts degraded scoring attempts by reason.
	SchedulerFallbackTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: SchedulerFallbackTotalName,
		Help: "Scoring attempts that ran in a degraded mode, by reason (empty|dead|all_invalid|stale).",
	}, []string{"reason"})

	// PluginDurationSeconds is the CarbonAware time spent per extension point.
	PluginDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    PluginDurationName,
		Help:    "Duration of one CarbonAware extension point call, seconds.",
		Buckets: pluginDurationBuckets,
	}, []string{"extension_point"})
)

// Pre-resolved children keep the Score hot path allocation-free (ARCH-05).
var (
	preScoreDuration  = PluginDurationSeconds.WithLabelValues(PointPreScore)
	scoreDuration     = PluginDurationSeconds.WithLabelValues(PointScore)
	normalizeDuration = PluginDurationSeconds.WithLabelValues(PointNormalizeScore)
	postBindDuration  = PluginDurationSeconds.WithLabelValues(PointPostBind)
)

func init() {
	// Expose every reason at 0 so rate() and the Gate 7 checks see the series
	// before the first failure.
	for _, r := range []string{FallbackReasonEmpty, FallbackReasonDead, FallbackReasonAllInvalid, FallbackReasonStale} {
		SchedulerFallbackTotal.WithLabelValues(r)
	}
}

// SchedulerCollectors returns the plugin collectors for registration in the
// kube-scheduler legacy registry (served on the secure port at /metrics).
func SchedulerCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		PodEstimatedCarbonGCO2,
		PodEstimatedEnergyKWh,
		SchedulerDecisionsTotal,
		SchedulerFallbackTotal,
		PluginDurationSeconds,
	}
}

// ObservePluginDuration records d for one extension point.
func ObservePluginDuration(point string, d time.Duration) {
	var o prometheus.Observer
	switch point {
	case PointPreScore:
		o = preScoreDuration
	case PointScore:
		o = scoreDuration
	case PointNormalizeScore:
		o = normalizeDuration
	case PointPostBind:
		o = postBindDuration
	default:
		o = PluginDurationSeconds.WithLabelValues(point)
	}
	o.Observe(d.Seconds())
}

// RecordFallback increments the fallback counter for reason.
func RecordFallback(reason string) {
	SchedulerFallbackTotal.WithLabelValues(reason).Inc()
}

// Decision is what the plugin knows about a Pod at PostBind.
type Decision struct {
	Node        string
	Zone        string
	ScoringMode string
	// HasEstimate is true when PreScore computed CF/energy for the selected node
	// (expected-cf on fresh or stale-in-window data). current-ci, G-08 and
	// opt-out decisions are counted but carry no estimate.
	HasEstimate bool
	CarbonGCO2e float64
	EnergyKWh   float64
}

// RecordDecision updates the decision counter and, when available, the
// per-Pod estimates for the selected node.
func RecordDecision(d Decision) {
	zone, node := orUnknown(d.Zone), orUnknown(d.Node)
	SchedulerDecisionsTotal.WithLabelValues(zone, node, d.ScoringMode).Inc()
	if !d.HasEstimate {
		return
	}
	PodEstimatedCarbonGCO2.WithLabelValues(zone, d.ScoringMode).Observe(d.CarbonGCO2e)
	PodEstimatedEnergyKWh.WithLabelValues(zone, d.ScoringMode).Set(d.EnergyKWh)
}

func orUnknown(s string) string {
	if s == "" {
		return UnknownLabel
	}
	return s
}

// ---------------------------------------------------------------------------
// Carbon Data Service metrics (metrics.md §3a).
// ---------------------------------------------------------------------------

// SnapshotSource is the read side of the CDS cache the collector needs.
// *cache.Cache satisfies it; tests use a stub.
type SnapshotSource interface {
	Load() (*v1alpha1.CarbonSnapshot, error)
	SnapshotAge(now time.Time) (time.Duration, bool)
	Clock() v1alpha1.Clock
}

// CDSMetrics owns a private registry for the Carbon Data Service process.
// Gauges are read from the cache at scrape time, so values are never older
// than the snapshot being served by REST.
type CDSMetrics struct {
	registry    *prometheus.Registry
	apiRequests *prometheus.CounterVec
}

// NewCDSMetrics registers the CDS collectors against src.
func NewCDSMetrics(src SnapshotSource) *CDSMetrics {
	reg := prometheus.NewRegistry()
	m := &CDSMetrics{
		registry: reg,
		apiRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: APIRequestsTotalName,
			Help: "HTTP requests served by the Carbon Data Service, by route and status code.",
		}, []string{"endpoint", "code"}),
	}
	reg.MustRegister(m.apiRequests, &cdsCollector{src: src})
	return m
}

// Registry exposes the registry (tests, extra collectors).
func (m *CDSMetrics) Registry() *prometheus.Registry {
	return m.registry
}

// Instrument wraps h so each response increments carbon_aware_api_requests_total.
// endpoint must be the route pattern, not the raw path, to bound cardinality.
func (m *CDSMetrics) Instrument(endpoint string, h http.HandlerFunc) http.HandlerFunc {
	// Pre-create the success series so the counter exists before the first request.
	m.apiRequests.WithLabelValues(endpoint, "200")
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		h(sw, r)
		m.apiRequests.WithLabelValues(endpoint, strconv.Itoa(sw.code)).Inc()
	}
}

// WriteText writes the registry in Prometheus text exposition format 0.0.4.
func (m *CDSMetrics) WriteText(w io.Writer) error {
	families, err := m.registry.Gather()
	if err != nil {
		return err
	}
	enc := expfmt.NewEncoder(w, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range families {
		if err := enc.Encode(mf); err != nil {
			return err
		}
	}
	return nil
}

type statusWriter struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.code = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

var (
	zoneIntensityDesc = prometheus.NewDesc(ZoneIntensityName,
		"Current carbon intensity served by the CDS for the zone, gCO2e/kWh.",
		[]string{"zone"}, nil)
	dataAgeDesc = prometheus.NewDesc(DataAgeName,
		"Age of the CDS snapshot (now - fetchedAt), seconds; same value as healthz dataAgeSeconds.",
		nil, nil)
)

type cdsCollector struct {
	src SnapshotSource
}

func (c *cdsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- zoneIntensityDesc
	ch <- dataAgeDesc
}

// Collect follows the metrics.md convention for snapshot gauges: nothing is
// exported while the cache is empty.
func (c *cdsCollector) Collect(ch chan<- prometheus.Metric) {
	if c.src == nil {
		return
	}
	snap, err := c.src.Load()
	if err != nil || snap == nil {
		return
	}
	for zone, st := range snap.Zones {
		if !st.Current.IsUsableForScoring() {
			continue
		}
		ch <- prometheus.MustNewConstMetric(zoneIntensityDesc, prometheus.GaugeValue,
			st.Current.CarbonIntensityGCO2ePerKWh, string(zone))
	}
	clock := c.src.Clock()
	if clock == nil {
		clock = v1alpha1.SystemClock{}
	}
	if age, ok := c.src.SnapshotAge(clock.Now()); ok {
		ch <- prometheus.MustNewConstMetric(dataAgeDesc, prometheus.GaugeValue, age.Seconds())
	}
}
