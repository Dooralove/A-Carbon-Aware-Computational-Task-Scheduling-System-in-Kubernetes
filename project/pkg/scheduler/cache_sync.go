package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	// DefaultCacheSyncIntervalSeconds is the worker period used when args are
	// not available (tests) and the value requested for T5.2 unit tests.
	// Production ticks use PluginArgs.RefreshIntervalSeconds (GO-12; stand canon 30s).
	DefaultCacheSyncIntervalSeconds int64 = 10
)

// ZoneCarbonState is one zone inside LocalCarbonCache.
// After the snapshot is published the value is treated as immutable (ARCH-15).
type ZoneCarbonState struct {
	Zone                 string
	CurrentCIGCO2ePerKWh float64
	Forecast             []v1alpha1.ForecastInterval
	LastUpdated          time.Time
	IsValid              bool
	Record               v1alpha1.CarbonRecord
}

// carbonSnapshot is the immutable plugin-side view published by atomic.Pointer.
type carbonSnapshot struct {
	fetchedAt time.Time
	quality   v1alpha1.DataQuality
	zones     map[string]*ZoneCarbonState
	api       v1alpha1.CarbonSnapshot
}

// LocalCarbonCache is the second carbon cache (process of kube-scheduler).
// Writers replace the whole snapshot; readers Load the pointer in O(1) (ARCH-06, ARCH-15).
type LocalCarbonCache struct {
	snap              atomic.Pointer[carbonSnapshot]
	clock             v1alpha1.Clock
	clockSkewSeconds  int64
	maxDataAgeSeconds int64
}

// NewLocalCarbonCache returns an empty cache. GetZoneState reports (nil, false)
// until the first successful Store — Fallback-ready (FF-EMPTY / G-07).
func NewLocalCarbonCache(clock v1alpha1.Clock, clockSkewSeconds, maxDataAgeSeconds int64) *LocalCarbonCache {
	if clock == nil {
		clock = v1alpha1.SystemClock{}
	}
	return &LocalCarbonCache{
		clock:             clock,
		clockSkewSeconds:  clockSkewSeconds,
		maxDataAgeSeconds: maxDataAgeSeconds,
	}
}

// GetZoneState returns a copy of the zone entry from the last published snapshot.
// It does not take a lock, wait, or perform I/O (hot-path safe, O(1) map lookup).
func (c *LocalCarbonCache) GetZoneState(zone string) (*ZoneCarbonState, bool) {
	if c == nil {
		return nil, false
	}
	snap := c.snap.Load()
	if snap == nil {
		return nil, false
	}
	z, ok := snap.zones[zone]
	if !ok || z == nil {
		return nil, false
	}
	cp := *z
	if z.Forecast != nil {
		cp.Forecast = append([]v1alpha1.ForecastInterval(nil), z.Forecast...)
	}
	return &cp, true
}

// CarbonSnapshot returns the last published API snapshot, or nil if none exists.
// The returned pointer is read-only: PreScore must copy values into CycleState.
func (c *LocalCarbonCache) CarbonSnapshot() *v1alpha1.CarbonSnapshot {
	if c == nil {
		return nil
	}
	snap := c.snap.Load()
	if snap == nil {
		return nil
	}
	return &snap.api
}

// SnapshotAge is age_snap = now - fetchedAt. ok is false while the cache is empty
// (gauge carbon_aware_snapshot_age_seconds is then left unpublished).
func (c *LocalCarbonCache) SnapshotAge(now time.Time) (time.Duration, bool) {
	if c == nil {
		return 0, false
	}
	snap := c.snap.Load()
	if snap == nil || snap.fetchedAt.IsZero() {
		return 0, false
	}
	if now.Before(snap.fetchedAt) {
		return 0, true
	}
	return now.Sub(snap.fetchedAt), true
}

// UpdateFromService pulls /zones plus current and forecast for every zone and
// atomically replaces the snapshot. Any HTTP/timeout/non-200 error leaves the
// previous snapshot in place (FF-INV-12, FF-CDS).
func (c *LocalCarbonCache) UpdateFromService(ctx context.Context, client *CarbonServiceClient) error {
	if c == nil {
		return fmt.Errorf("local carbon cache is nil: %w", v1alpha1.ErrEmptyCache)
	}
	if client == nil {
		return fmt.Errorf("carbon service client is nil: %w", ErrCDSNotOK)
	}

	list, err := client.ListZones(ctx)
	if err != nil {
		return err
	}

	now := c.clock.Now().UTC()
	next := make(map[string]*ZoneCarbonState, len(list.Items))
	quality := v1alpha1.QualityFresh
	invalid := 0

	for _, item := range list.Items {
		state, skip, fetchErr := c.fetchZone(ctx, client, item, now)
		if fetchErr != nil {
			carbonAwareInvalidRecordTotalAdd(invalid)
			return fetchErr
		}
		if skip {
			invalid++
			continue
		}
		next[state.Zone] = state
		if state.Record.Quality == v1alpha1.QualityStale || item.Quality == v1alpha1.QualityStale {
			quality = v1alpha1.QualityStale
		}
	}
	carbonAwareInvalidRecordTotalAdd(invalid)

	if len(next) == 0 {
		return fmt.Errorf("no valid carbon zones in tick: %w", v1alpha1.ErrEmptyCache)
	}

	c.publish(next, now, quality)
	return nil
}

func carbonAwareInvalidRecordTotalAdd(n int) {
	if n <= 0 {
		return
	}
	registerCacheMetrics()
	carbonAwareInvalidRecordTotal.WithLabelValues("worker").Add(float64(n))
}

func (c *LocalCarbonCache) fetchZone(
	ctx context.Context,
	client *CarbonServiceClient,
	item v1alpha1.ZoneProfile,
	now time.Time,
) (*ZoneCarbonState, bool, error) {
	if err := item.Validate(); err != nil {
		return nil, true, nil
	}
	zone := string(item.Zone)
	rec, err := client.Current(ctx, zone)
	if err != nil {
		return nil, false, err
	}
	fc, err := client.Forecast(ctx, zone)
	if err != nil {
		return nil, false, err
	}
	state, ok := buildZoneState(rec, fc, now, c.clockSkewSeconds)
	if !ok {
		return nil, true, nil
	}
	return state, false, nil
}

func buildZoneState(rec v1alpha1.CarbonRecord, fc v1alpha1.CarbonForecast, now time.Time, clockSkewSeconds int64) (*ZoneCarbonState, bool) {
	if rec.Unit == "" {
		rec.Unit = v1alpha1.IntensityUnitGCO2ePerKWh
	}
	if rec.EmissionFactorType == "" {
		rec.EmissionFactorType = v1alpha1.EmissionLifecycle
	}
	if !rec.IsUsableForScoring() {
		return nil, false
	}
	if v1alpha1.TimestampFromFuture(rec.Timestamp.Time, now, clockSkewSeconds) {
		return nil, false
	}
	if err := fc.Validate(); err != nil && len(fc.Intervals) > 0 {
		return nil, false
	}
	return &ZoneCarbonState{
		Zone:                 string(rec.Zone),
		CurrentCIGCO2ePerKWh: rec.CarbonIntensityGCO2ePerKWh,
		Forecast:             fc.Intervals,
		LastUpdated:          now,
		IsValid:              true,
		Record:               rec,
	}, true
}

func (c *LocalCarbonCache) publish(zones map[string]*ZoneCarbonState, fetchedAt time.Time, quality v1alpha1.DataQuality) {
	apiZones := make(map[v1alpha1.ZoneID]v1alpha1.ZoneCarbonState, len(zones))
	for id, z := range zones {
		apiZones[v1alpha1.ZoneID(id)] = v1alpha1.ZoneCarbonState{
			Current:  z.Record,
			Forecast: z.Forecast,
		}
	}
	snap := &carbonSnapshot{
		fetchedAt: fetchedAt,
		quality:   quality,
		zones:     zones,
		api: v1alpha1.CarbonSnapshot{
			FetchedAt: v1alpha1.RFC3339Time{Time: fetchedAt},
			Quality:   quality,
			Zones:     apiZones,
		},
	}
	c.snap.Store(snap)
}

// replaceAPI publishes an immutable snapshot without HTTP. Tests seed PreScore this way.
func (c *LocalCarbonCache) replaceAPI(api v1alpha1.CarbonSnapshot) {
	if c == nil {
		return
	}
	zones := make(map[string]*ZoneCarbonState, len(api.Zones))
	for id, z := range api.Zones {
		rec := z.Current
		zones[string(id)] = &ZoneCarbonState{
			Zone:                 string(id),
			CurrentCIGCO2ePerKWh: rec.CarbonIntensityGCO2ePerKWh,
			Forecast:             z.Forecast,
			LastUpdated:          api.FetchedAt.Time,
			IsValid:              true,
			Record:               rec,
		}
	}
	c.publish(zones, api.FetchedAt.Time, api.Quality)
}
