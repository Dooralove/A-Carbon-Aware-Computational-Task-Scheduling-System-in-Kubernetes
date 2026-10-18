package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/provider"
)

// Refresh pulls a complete zone map from the provider and publishes it atomically.
// On provider/validation failure the previous snapshot is kept (last-good) and
// marked stale. Partial maps are never Store'd (FF-INV-12).
func (c *Cache) Refresh(ctx context.Context) error {
	if c.provider == nil {
		return fmt.Errorf("cache: provider is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	at := c.clock.Now().UTC()
	zones, err := c.provider.ListZones(ctx)
	if err != nil {
		c.fail(err)
		return fmt.Errorf("list zones: %w", err)
	}
	if len(zones) == 0 {
		c.fail(v1alpha1.ErrEmptyCache)
		return fmt.Errorf("list zones: empty catalogue")
	}

	next := make(map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState, len(zones))
	for _, z := range zones {
		if err := z.Zone.Validate(); err != nil {
			c.fail(err)
			return fmt.Errorf("zone catalogue: %w", err)
		}
		rec, err := c.provider.Current(ctx, z.Zone, at)
		if err != nil {
			c.fail(err)
			return fmt.Errorf("current %s: %w", z.Zone, err)
		}
		if err := validateRecord(rec, at, c.skew); err != nil {
			c.fail(err)
			return fmt.Errorf("current %s: %w", z.Zone, err)
		}
		rec.FetchedAt = v1alpha1.RFC3339Time{Time: at}
		rec.FreshnessSeconds = 0
		if rec.Quality == "" {
			rec.Quality = v1alpha1.QualityFresh
		}

		fc, err := c.provider.Forecast(ctx, z.Zone, at, c.horizon)
		if err != nil && !errors.Is(err, v1alpha1.ErrEmptyForecast) {
			c.fail(err)
			return fmt.Errorf("forecast %s: %w", z.Zone, err)
		}
		clean := make([]v1alpha1.ForecastInterval, 0, len(fc))
		for i, iv := range fc {
			if err := iv.Validate(); err != nil {
				c.errInv.Add(1)
				c.logger.Warn("dropping forecast interval", "zone", z.Zone, "index", i, "err", err)
				continue
			}
			if iv.Unit == "" {
				iv.Unit = v1alpha1.IntensityUnitGCO2ePerKWh
			}
			clean = append(clean, iv)
		}
		next[z.Zone] = v1alpha1.ZoneCarbonState{Current: rec, Forecast: clean}
	}
	if len(next) == 0 {
		c.fail(v1alpha1.ErrInvalidCarbonIntensity)
		return fmt.Errorf("refresh: no valid zones")
	}
	if len(next) != len(zones) {
		c.fail(v1alpha1.ErrInvalidCarbonIntensity)
		return fmt.Errorf("refresh: refusing partial map (%d/%d zones)", len(next), len(zones))
	}

	c.Store(&v1alpha1.CarbonSnapshot{
		FetchedAt: v1alpha1.RFC3339Time{Time: at},
		Quality:   v1alpha1.QualityFresh,
		Zones:     next,
	})
	return nil
}

// Run performs an initial Refresh, then Loop until ctx is cancelled.
func (c *Cache) Run(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	err := c.Refresh(rctx)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Warn("initial carbon cache refresh failed", "err", err)
	}
	return c.Loop(ctx)
}

// Loop polls the provider on RefreshInterval. The first tick waits a full interval
// (call Refresh synchronously at process start so the API is not empty).
func (c *Cache) Loop(ctx context.Context) error {
	tick := time.NewTicker(c.refresh)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			rctx, cancel := context.WithTimeout(ctx, c.timeout)
			err := c.Refresh(rctx)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				c.logger.Warn("carbon cache refresh failed, keeping last-good", "err", err)
			}
		}
	}
}

func (c *Cache) fail(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	c.stale.Store(c.snap.Load() != nil)
	switch {
	case errors.Is(err, provider.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		c.errTO.Add(1)
	case errors.Is(err, provider.ErrInvalidResponse):
		c.errParse.Add(1)
	case errors.Is(err, v1alpha1.ErrInvalidCarbonIntensity),
		errors.Is(err, v1alpha1.ErrNotLifecycle),
		errors.Is(err, v1alpha1.ErrEmptyCache):
		c.errInv.Add(1)
	default:
		c.errHTTP.Add(1)
	}
}

func validateRecord(rec v1alpha1.CarbonRecord, now time.Time, skew time.Duration) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	if !rec.IsUsableForScoring() {
		return fmt.Errorf("record not usable for scoring: %w", v1alpha1.ErrInvalidCarbonIntensity)
	}
	skewSec := int64(skew / time.Second)
	if v1alpha1.TimestampFromFuture(rec.Timestamp.Time, now, skewSec) {
		return fmt.Errorf("timestamp %s is in the future: %w", rec.Timestamp.UTC().Format(time.RFC3339), v1alpha1.ErrInvalidCarbonIntensity)
	}
	return nil
}
