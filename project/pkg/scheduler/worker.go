package scheduler

import (
	"context"
	"time"

	"k8s.io/klog/v2"
)

// startCacheSyncWorker pulls CDS on a ticker bound to the factory ctx (GO-08).
// It never runs on the Score/PreScore call stack (ARCH-01, ARCH-02, SCOPE-OUT-17).
func (p *CarbonAwarePlugin) startCacheSyncWorker(ctx context.Context) {
	interval := time.Duration(p.args.RefreshIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Duration(DefaultCacheSyncIntervalSeconds) * time.Second
	}

	logger := klog.FromContext(ctx)
	logger.V(2).Info("CarbonAware cache sync worker started",
		"refreshIntervalSeconds", int64(interval/time.Second),
		"httpTimeoutSeconds", p.args.HTTPTimeoutSeconds,
		"carbonServiceURL", p.args.CarbonServiceURL,
	)

	p.syncTick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.V(2).Info("CarbonAware cache sync worker stopped")
			return
		case <-ticker.C:
			p.syncTick(ctx)
		}
	}
}

func (p *CarbonAwarePlugin) syncTick(ctx context.Context) {
	if ctx.Err() != nil || p.cache == nil || p.client == nil {
		return
	}
	start := time.Now()
	err := p.cache.UpdateFromService(ctx, p.client)
	elapsed := time.Since(start).Seconds()
	stored := err == nil
	recordSyncTick(elapsed, stored, 0)
	if err != nil {
		klog.FromContext(ctx).V(2).Info("CarbonAware cache sync tick failed; keeping last snapshot",
			"err", err)
	}
	if age, ok := p.cache.SnapshotAge(p.clock.Now()); ok {
		recordSnapshotAgeSeconds(age.Seconds())
	}
}
