// Package cache is the in-process CDS snapshot (SCOPE-OUT-10, ARCH-15).
//
// The published CarbonSnapshot is immutable. Readers Load/Get an atomic pointer
// (no mutex on the GET path). Writers assemble a complete zone map, then Store.
package cache

import (
	"log/slog"
	"sync/atomic"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	// DefaultRefreshInterval is the CDS provider poll period (canon 30s).
	DefaultRefreshInterval = 30 * time.Second
	// DefaultRefreshTimeout bounds one Refresh tick (GO-09).
	DefaultRefreshTimeout = 2 * time.Second
	// DefaultMaxDataAge is G-08 / health degraded threshold (canon 300s).
	DefaultMaxDataAge = 300 * time.Second
	// DefaultStaleWarn is the SM-STALE warning threshold (canon 90s).
	DefaultStaleWarn = 90 * time.Second
	// DefaultClockSkew is FF-FUT allowance (canon 60s).
	DefaultClockSkew = 60 * time.Second
	// DefaultForecastHorizon is GET .../forecast default duration (7200s).
	DefaultForecastHorizon = time.Duration(v1alpha1.DefaultForecastHorizonSeconds) * time.Second
)

// Options configure a Cache and its background refresh worker.
type Options struct {
	Provider        v1alpha1.CarbonProvider
	Clock           v1alpha1.Clock
	Logger          *slog.Logger
	RefreshInterval time.Duration
	RefreshTimeout  time.Duration
	MaxDataAge      time.Duration
	StaleWarn       time.Duration
	ClockSkew       time.Duration
	ForecastHorizon time.Duration
}

// Cache holds the last-good carbon snapshot. Store(nil) is ignored (FF-INV-12).
type Cache struct {
	snap     atomic.Pointer[v1alpha1.CarbonSnapshot]
	stale    atomic.Bool
	errTO    atomic.Int64
	errHTTP  atomic.Int64
	errParse atomic.Int64
	errInv   atomic.Int64

	provider v1alpha1.CarbonProvider
	clock    v1alpha1.Clock
	logger   *slog.Logger
	refresh  time.Duration
	timeout  time.Duration
	maxAge   time.Duration
	staleW   time.Duration
	skew     time.Duration
	horizon  time.Duration
}

// New returns an empty cache with defaults (no provider → Refresh is a no-op error).
func New() *Cache {
	return NewWithOptions(Options{})
}

// NewWithOptions returns a cache ready for Refresh/Run.
func NewWithOptions(opts Options) *Cache {
	clock := opts.Clock
	if clock == nil {
		clock = v1alpha1.SystemClock{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	refresh := opts.RefreshInterval
	if refresh <= 0 {
		refresh = DefaultRefreshInterval
	}
	timeout := opts.RefreshTimeout
	if timeout <= 0 {
		timeout = DefaultRefreshTimeout
	}
	maxAge := opts.MaxDataAge
	if maxAge <= 0 {
		maxAge = DefaultMaxDataAge
	}
	staleW := opts.StaleWarn
	if staleW <= 0 {
		staleW = DefaultStaleWarn
	}
	skew := opts.ClockSkew
	if skew == 0 {
		skew = DefaultClockSkew
	}
	horizon := opts.ForecastHorizon
	if horizon <= 0 {
		horizon = DefaultForecastHorizon
	}
	return &Cache{
		provider: opts.Provider,
		clock:    clock,
		logger:   logger,
		refresh:  refresh,
		timeout:  timeout,
		maxAge:   maxAge,
		staleW:   staleW,
		skew:     skew,
		horizon:  horizon,
	}
}

// Load returns the published snapshot, or ErrEmptyCache if none exists.
func (c *Cache) Load() (*v1alpha1.CarbonSnapshot, error) {
	snap := c.snap.Load()
	if snap == nil {
		return nil, v1alpha1.ErrEmptyCache
	}
	return snap, nil
}

// Store publishes snap. A nil snap is ignored so a failed refresh cannot wipe last-good.
func (c *Cache) Store(snap *v1alpha1.CarbonSnapshot) {
	if snap == nil {
		return
	}
	c.snap.Store(snap)
	c.stale.Store(false)
}

// Get returns a copy of the current CarbonRecord for zone and whether it exists.
// Freshness/quality are computed at read time; the stored snapshot is not mutated.
func (c *Cache) Get(zone string) (*v1alpha1.CarbonRecord, bool) {
	snap := c.snap.Load()
	if snap == nil {
		return nil, false
	}
	st, ok := snap.Zones[v1alpha1.ZoneID(zone)]
	if !ok {
		return nil, false
	}
	rec := applyFreshness(st.Current, c.clock.Now(), c.staleW, c.stale.Load())
	return &rec, true
}

// State returns a copy of the zone's current record and forecast slice.
func (c *Cache) State(zone v1alpha1.ZoneID) (v1alpha1.ZoneCarbonState, bool) {
	snap := c.snap.Load()
	if snap == nil {
		return v1alpha1.ZoneCarbonState{}, false
	}
	st, ok := snap.Zones[zone]
	if !ok {
		return v1alpha1.ZoneCarbonState{}, false
	}
	st.Current = applyFreshness(st.Current, c.clock.Now(), c.staleW, c.stale.Load())
	if st.Forecast != nil {
		st.Forecast = append([]v1alpha1.ForecastInterval(nil), st.Forecast...)
	}
	return st, true
}

// MaxDataAge returns the configured freshness limit.
func (c *Cache) MaxDataAge() time.Duration { return c.maxAge }

// Clock returns the cache clock (wall or replay model time).
func (c *Cache) Clock() v1alpha1.Clock { return c.clock }

// Stale reports whether the last refresh failed while last-good is still served.
func (c *Cache) Stale() bool { return c.stale.Load() }

func applyFreshness(rec v1alpha1.CarbonRecord, now time.Time, staleWarn time.Duration, refreshFailed bool) v1alpha1.CarbonRecord {
	rec = cloneRecord(rec)
	if rec.FetchedAt.IsZero() {
		rec.FreshnessSeconds = 0
		if refreshFailed {
			rec.Quality = v1alpha1.QualityStale
		}
		return rec
	}
	age := now.UTC().Sub(rec.FetchedAt.UTC())
	if age < 0 {
		age = 0
	}
	rec.FreshnessSeconds = int64(age / time.Second)
	if refreshFailed || age > staleWarn {
		if rec.Quality == v1alpha1.QualityFresh || rec.Quality == "" {
			rec.Quality = v1alpha1.QualityStale
		}
	}
	return rec
}

// SnapshotAge is now - FetchedAt. ok is false when no snapshot has been published.
func (c *Cache) SnapshotAge(now time.Time) (time.Duration, bool) {
	snap := c.snap.Load()
	if snap == nil || snap.FetchedAt.IsZero() {
		return 0, false
	}
	d := now.UTC().Sub(snap.FetchedAt.UTC())
	if d < 0 {
		d = 0
	}
	return d, true
}

func cloneRecord(rec v1alpha1.CarbonRecord) v1alpha1.CarbonRecord {
	if rec.CarbonFreePercentage != nil {
		v := *rec.CarbonFreePercentage
		rec.CarbonFreePercentage = &v
	}
	if rec.EstimationMethod != nil {
		v := *rec.EstimationMethod
		rec.EstimationMethod = &v
	}
	if rec.FlowTraced != nil {
		v := *rec.FlowTraced
		rec.FlowTraced = &v
	}
	return rec
}

// Health returns the GET /api/v1/healthz body. Empty cache is unavailable;
// last-good after a failed refresh is degraded with ready=true (never 500).
func (c *Cache) Health(mode v1alpha1.ProviderMode) v1alpha1.HealthStatus {
	now := c.clock.Now().UTC()
	body := v1alpha1.HealthStatus{
		APIVersion:        v1alpha1.GroupVersion,
		Kind:              v1alpha1.KindHealthStatus,
		Status:            v1alpha1.ServingUnavailable,
		Mode:              mode,
		Ready:             false,
		MaxDataAgeSeconds: int64(c.maxAge / time.Second),
		Message:           "carbon cache is empty",
	}
	if mode == v1alpha1.ProviderModeReplay {
		mt := v1alpha1.RFC3339Time{Time: now}
		body.ModelTime = &mt
	}
	snap := c.snap.Load()
	if snap == nil {
		return body
	}
	age, _ := c.SnapshotAge(now)
	failed := c.stale.Load()
	ready := 0
	for _, st := range snap.Zones {
		rec := applyFreshness(st.Current, now, c.staleW, failed)
		if rec.IsUsableForScoring() {
			ready++
		}
	}
	body.DataAgeSeconds = int64(age / time.Second)
	body.ZonesReady = ready
	body.ZonesTotal = len(snap.Zones)
	body.Ready = true
	body.Message = ""
	switch {
	case failed:
		body.Status = v1alpha1.ServingDegraded
		body.Message = "serving last-good snapshot after provider error"
	case age > c.staleW:
		body.Status = v1alpha1.ServingDegraded
	default:
		body.Status = v1alpha1.ServingOK
	}
	return body
}
