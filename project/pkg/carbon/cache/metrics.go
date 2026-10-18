package cache

import (
	"fmt"
	"io"

	"carbon-aware-k8s/api/v1alpha1"
)

// WriteMetrics writes Prometheus text for CDS gauges/counters (metrics.md §3).
func (c *Cache) WriteMetrics(w io.Writer) {
	name := "unknown"
	mode := v1alpha1.ProviderModeMock
	if c.provider != nil {
		name = c.provider.Name()
		mode = c.provider.Mode()
	}
	now := c.clock.Now().UTC()
	snap := c.snap.Load()
	zones := 0
	age := 0.0
	fresh, staleQ := 0, 0
	if snap != nil {
		zones = len(snap.Zones)
		if !snap.FetchedAt.IsZero() {
			d := now.Sub(snap.FetchedAt.UTC())
			if d < 0 {
				d = 0
			}
			age = d.Seconds()
			if c.stale.Load() || d > c.staleW {
				staleQ = 1
			} else {
				fresh = 1
			}
		}
	}
	_, _ = fmt.Fprintf(w, "# TYPE carbon_aware_cds_zones gauge\ncarbon_aware_cds_zones %d\n", zones)
	if snap != nil {
		_, _ = fmt.Fprintf(w, "# TYPE carbon_aware_cds_cache_age_seconds gauge\ncarbon_aware_cds_cache_age_seconds %g\n", age)
	}
	_, _ = fmt.Fprintf(w, "# TYPE carbon_aware_cds_provider_error_total counter\n")
	_, _ = fmt.Fprintf(w, "carbon_aware_cds_provider_error_total{provider=%q,kind=%q} %d\n", name, "timeout", c.errTO.Load())
	_, _ = fmt.Fprintf(w, "carbon_aware_cds_provider_error_total{provider=%q,kind=%q} %d\n", name, "http", c.errHTTP.Load())
	_, _ = fmt.Fprintf(w, "carbon_aware_cds_provider_error_total{provider=%q,kind=%q} %d\n", name, "parse", c.errParse.Load())
	_, _ = fmt.Fprintf(w, "carbon_aware_cds_provider_error_total{provider=%q,kind=%q} %d\n", name, "invalid", c.errInv.Load())
	if mode == v1alpha1.ProviderModeReplay {
		_, _ = fmt.Fprintf(w, "# TYPE carbon_aware_cds_replay_model_time_seconds gauge\ncarbon_aware_cds_replay_model_time_seconds %d\n", now.Unix())
	}
	_, _ = fmt.Fprintf(w, "# TYPE carbon_aware_cds_quality gauge\n")
	_, _ = fmt.Fprintf(w, "carbon_aware_cds_quality{quality=%q} %d\n", "fresh", fresh)
	_, _ = fmt.Fprintf(w, "carbon_aware_cds_quality{quality=%q} %d\n", "stale", staleQ)
}
