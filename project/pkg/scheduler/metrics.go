package scheduler

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/component-base/metrics/legacyregistry"
)

var registerCacheMetricsOnce sync.Once

var (
	carbonAwareSyncFailureTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "carbon_aware_sync_failure_total",
		Help: "Cache Sync Worker ticks that did not Store a new plugin snapshot.",
	})
	carbonAwareSyncDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "carbon_aware_sync_duration_seconds",
		Help:    "Duration of one Cache Sync Worker tick in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"status"})
	carbonAwareInvalidRecordTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "carbon_aware_invalid_record_total",
		Help: "Carbon records rejected by G-10 / FF-FUT while assembling the plugin snapshot.",
	}, []string{"stage"})
	carbonAwareSnapshotAgeSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "carbon_aware_snapshot_age_seconds",
		Help: "Age of the plugin carbon snapshot in seconds (now - fetchedAt). Not set while Load() is nil.",
	})
	carbonAwareScoringAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "carbon_aware_scoring_attempts_total",
		Help: "PreScore attempts with carbon-aware.io/enabled=true that did not fail SLA.",
	}, []string{"scoring_mode"})
	carbonAwareFallbackTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "carbon_aware_fallback_total",
		Help: "G-08: Score_neutral=50 written for every node. Not SM-STALE, not opt-out.",
	}, []string{"reason"})
	carbonAwareStaleInWindowTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "carbon_aware_stale_in_window_total",
		Help: "SM-STALE: full (2.19)/(2.23) with a Warning.",
	})
	carbonAwareOptOutTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "carbon_aware_opt_out_total",
		Help: "FF-OPT: carbon-aware.io/enabled is not true.",
	})
	carbonAwareSLARejectionTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "carbon_aware_sla_rejection_total",
		Help: "PreScore Error G-12/G-20 (SLA precondition failed).",
	})
	carbonAwareUnknownZoneNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "carbon_aware_unknown_zone_nodes",
		Help: "NM-UNKNOWN node count in the last successful scoring PreScore.",
	})
	carbonAwarePreScoreDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "carbon_aware_prescore_duration_seconds",
		Help:    "Duration of CarbonAware.PreScore in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	carbonAwareEstimatedCFGCO2e = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "carbon_aware_estimated_cf_gco2e",
		Help:    "Expected marginal CF of a feasible node on a scoring attempt (gCO2e).",
		Buckets: prometheus.DefBuckets,
	}, []string{"zone"})
)

func init() {
	registerCacheMetrics()
}

func registerCacheMetrics() {
	registerCacheMetricsOnce.Do(func() {
		legacyregistry.RawMustRegister(
			carbonAwareSyncFailureTotal,
			carbonAwareSyncDurationSeconds,
			carbonAwareInvalidRecordTotal,
			carbonAwareSnapshotAgeSeconds,
			carbonAwareScoringAttemptsTotal,
			carbonAwareFallbackTotal,
			carbonAwareStaleInWindowTotal,
			carbonAwareOptOutTotal,
			carbonAwareSLARejectionTotal,
			carbonAwareUnknownZoneNodes,
			carbonAwarePreScoreDurationSeconds,
			carbonAwareEstimatedCFGCO2e,
		)
	})
}

func recordSyncTick(durationSeconds float64, stored bool, invalidRecords int) {
	registerCacheMetrics()
	if invalidRecords > 0 {
		carbonAwareInvalidRecordTotal.WithLabelValues("worker").Add(float64(invalidRecords))
	}
	if stored {
		carbonAwareSyncDurationSeconds.WithLabelValues("success").Observe(durationSeconds)
		return
	}
	carbonAwareSyncFailureTotal.Inc()
	carbonAwareSyncDurationSeconds.WithLabelValues("failure").Observe(durationSeconds)
}

func recordSnapshotAgeSeconds(ageSeconds float64) {
	registerCacheMetrics()
	carbonAwareSnapshotAgeSeconds.Set(ageSeconds)
}

func recordPreScoreDuration(seconds float64) {
	registerCacheMetrics()
	carbonAwarePreScoreDurationSeconds.Observe(seconds)
}

func recordScoringAttempt(mode string) {
	registerCacheMetrics()
	carbonAwareScoringAttemptsTotal.WithLabelValues(mode).Inc()
}

func recordFallback(reason string) {
	registerCacheMetrics()
	carbonAwareFallbackTotal.WithLabelValues(reason).Inc()
}

func recordStaleInWindow() {
	registerCacheMetrics()
	carbonAwareStaleInWindowTotal.Inc()
}

func recordOptOut() {
	registerCacheMetrics()
	carbonAwareOptOutTotal.Inc()
}

func recordSLARejection() {
	registerCacheMetrics()
	carbonAwareSLARejectionTotal.Inc()
}

func recordUnknownZoneNodes(n int) {
	registerCacheMetrics()
	carbonAwareUnknownZoneNodes.Set(float64(n))
}

func recordEstimatedCF(zone string, gco2e float64) {
	registerCacheMetrics()
	if zone == "" {
		zone = "unknown"
	}
	carbonAwareEstimatedCFGCO2e.WithLabelValues(zone).Observe(gco2e)
}
