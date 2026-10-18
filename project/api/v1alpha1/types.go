// Package v1alpha1 is the carbon-aware.io/v1alpha1 contract surface.
//
// Нормативный канон полей, единиц и ключей: docs/ssot/03_api_contracts_ssot.md.
// Формулы (2.1)–(2.23) не дублируются: docs/ssot/02_math_model_ssot.md.
package v1alpha1

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// GroupName is the Kubernetes-style API group.
	GroupName = "carbon-aware.io"

	// Version is the API version (VER-05). Raising it to v1beta1 requires an ADR.
	Version = "v1alpha1"

	// GroupVersion is GroupName + "/" + Version.
	GroupVersion = GroupName + "/" + Version

	// CarbonAwarePluginName is the Scheduler Framework plugin name.
	CarbonAwarePluginName = "CarbonAware"

	// CarbonCycleStateKey is the CycleState key written in PreScore and read in Score.
	CarbonCycleStateKey = GroupName + "/CarbonAware"
)

// Kind names for Kubernetes-style TypeMeta on REST envelopes.
const (
	KindCarbonRecord             = "CarbonRecord"
	KindCarbonForecast           = "CarbonForecast"
	KindZoneProfile              = "ZoneProfile"
	KindZoneProfileList          = "ZoneProfileList"
	KindHealthStatus             = "HealthStatus"
	KindStatus                   = "Status"
	KindNodeEnergyProfile        = "NodeEnergyProfile"
	KindWorkloadProfile          = "WorkloadProfile"
	KindSchedulingDecisionRecord = "SchedulingDecisionRecord"
	KindCarbonAwarePluginArgs    = "CarbonAwarePluginArgs"
)

// Canonical REST paths of the Carbon Data Service (SCOPE-IN-02, ARCH §5.4).
const (
	PathHealth   = "/api/v1/health"
	PathHealthz  = "/api/v1/healthz"
	PathZones    = "/api/v1/zones"
	PathCurrent  = "/api/v1/zones/{zone}/current"
	PathForecast = "/api/v1/zones/{zone}/forecast"
	PathMetrics  = "/metrics"
)

// QueryDuration is the forecast query parameter name.
const QueryDuration = "duration"

// Intensity and scoring domain constants (02_math_model_ssot.md §13, G-10).
const (
	// IntensityUnitGCO2ePerKWh is the only legal carbon-intensity unit on the wire.
	IntensityUnitGCO2ePerKWh = "gCO2e/kWh"

	// CarbonIntensityMinGCO2ePerKWh is the inclusive lower bound of a valid CI (G-10).
	CarbonIntensityMinGCO2ePerKWh = 0.0

	// CarbonIntensityMaxGCO2ePerKWh is the inclusive upper bound of a valid CI (G-10).
	CarbonIntensityMaxGCO2ePerKWh = 2000.0

	// DefaultForecastHorizonSeconds is the default ?duration= for GET .../forecast (2 h).
	DefaultForecastHorizonSeconds int64 = 7200

	// MaxForecastHorizonSeconds is the maximum accepted forecast horizon (24 h).
	MaxForecastHorizonSeconds int64 = 86400

	// MaxForecastIntervals is 24 h of 5-minute points.
	MaxForecastIntervals = 288

	// NeutralPluginScore is Score_neutral for G-08 on the framework scale [0,100].
	NeutralPluginScore int64 = 50

	// MaxPluginScore is the inclusive upper bound of Score after (2.21).
	MaxPluginScore int64 = 100

	// ScoreFixedPointScale stores rnd(100*Score) in CycleState (model §9.2).
	ScoreFixedPointScale int64 = 100

	// EpsilonWeight is ε_w for the simplex check (2.20) / G-18.
	EpsilonWeight = 1e-9

	// DefaultStaleWarnSeconds is the SM-STALE warning threshold (T3.4 §3).
	DefaultStaleWarnSeconds int64 = 90

	// DefaultClockSkewSeconds is the future-timestamp allowance for G-10 / FF-FUT.
	DefaultClockSkewSeconds int64 = 60

	// SLAPreconditionMessage is the PreScore Error message for (2.13) / G-12 / G-20.
	SLAPreconditionMessage = "SLA precondition failed"
)

// Pod / Node metadata keys. Each key has exactly one legal surface (label or annotation).
const (
	LabelEnabled       = GroupName + "/enabled"
	LabelWorkloadClass = GroupName + "/workload-class"
	LabelZone          = GroupName + "/zone"
	LabelPowerProfile  = GroupName + "/power-profile"

	AnnotationExpectedDuration = GroupName + "/expected-duration"
	AnnotationSLADeadline      = GroupName + "/sla-deadline"
	AnnotationCarbonWeight     = GroupName + "/carbon-weight"
)

// Legal label/annotation values.
const (
	EnabledTrue  = "true"
	EnabledFalse = "false"

	PowerProfileStandard = "standard"
)

var (
	// ErrUnknownZone is returned when a zone id is not in the CDS snapshot.
	ErrUnknownZone = errors.New("unknown carbon zone")

	// ErrStaleData is returned when carbon data exceeded maxDataAgeSeconds (G-08).
	ErrStaleData = errors.New("carbon data is stale")

	// ErrInvalidCarbonIntensity is returned when CI is outside [0, 2000] gCO2e/kWh, NaN or Inf (G-10).
	ErrInvalidCarbonIntensity = errors.New("carbon intensity out of domain")

	// ErrInvalidDuration is returned when a Kubernetes-style duration cannot be parsed.
	ErrInvalidDuration = errors.New("invalid duration")

	// ErrInvalidAnnotation is returned when a carbon-aware.io/* value is malformed.
	ErrInvalidAnnotation = errors.New("invalid carbon-aware annotation")

	// ErrEmptyCache is returned when the CDS in-memory snapshot has never been published.
	ErrEmptyCache = errors.New("carbon cache is empty")

	// ErrNotLifecycle is returned when emissionFactorType is not lifecycle.
	ErrNotLifecycle = errors.New("emission factor type is not lifecycle")

	// ErrSLAPrecondition is the sentinel behind PreScore Error "SLA precondition failed".
	ErrSLAPrecondition = errors.New(SLAPreconditionMessage)

	// ErrUnknownWorkloadClass is returned when the class is unknown and no default is configured (G-20).
	ErrUnknownWorkloadClass = errors.New("unknown workload class")

	// ErrInvalidPluginArgs is returned when CarbonAwarePluginArgs violate (2.2a) or (2.20).
	ErrInvalidPluginArgs = errors.New("invalid CarbonAware plugin args")

	// ErrEmptyForecast is returned by providers that have no forecast; callers apply (2.11).
	ErrEmptyForecast = errors.New("forecast is empty")
)

var zoneIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// RFC3339Time is a UTC timestamp serialized as RFC 3339 with second precision
// and a trailing "Z" (example: "2026-09-04T12:00:00Z"). This is the only REST
// time format (API-TIME-01). Unix seconds are derived internally, never on the wire.
type RFC3339Time struct {
	time.Time
}

// MarshalJSON implements json.Marshaler.
func (t RFC3339Time) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	stamp := t.UTC().Truncate(time.Second).Format(time.RFC3339)
	return strconv.AppendQuote(nil, stamp), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *RFC3339Time) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		t.Time = time.Time{}
		return nil
	}
	s, err := strconv.Unquote(string(data))
	if err != nil {
		return fmt.Errorf("RFC3339Time: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return fmt.Errorf("RFC3339Time: %w", err)
		}
	}
	t.Time = parsed.UTC()
	return nil
}

// UnixSeconds returns the UTC Unix timestamp in seconds (math-model runtime unit).
func (t RFC3339Time) UnixSeconds() int64 {
	if t.Time.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

// ZoneID is a logical energy zone. It MUST match the node label carbon-aware.io/zone
// and the DNS-1123 label grammar (not a cloud region).
type ZoneID string

// Validate reports whether z is a non-empty DNS-1123 label of at most 63 characters.
func (z ZoneID) Validate() error {
	s := string(z)
	if s == "" {
		return fmt.Errorf("zone id is empty: %w", ErrUnknownZone)
	}
	if len(s) > 63 || !zoneIDPattern.MatchString(s) {
		return fmt.Errorf("zone id %q is not a DNS-1123 label: %w", s, ErrUnknownZone)
	}
	return nil
}

// IntensityUnit is the carbon-intensity unit. Only gCO2e/kWh is legal.
type IntensityUnit string

// DataQuality is the provenance/freshness class of a CarbonRecord (T1.1 §4.5).
type DataQuality string

const (
	QualityFresh     DataQuality = "fresh"
	QualityStale     DataQuality = "stale"
	QualityEstimated DataQuality = "estimated"
	QualityInvalid   DataQuality = "invalid"
	QualityMissing   DataQuality = "missing"
)

// IsUsableForScoring reports whether quality may enter (2.10) / (2.23).
func (q DataQuality) IsUsableForScoring() bool {
	switch q {
	case QualityFresh, QualityStale, QualityEstimated:
		return true
	default:
		return false
	}
}

// CarbonSource identifies the adapter that produced the record (SCOPE-IN-03).
type CarbonSource string

const (
	SourceElectricityMaps   CarbonSource = "electricity-maps"
	SourceNESO              CarbonSource = "neso"
	SourceHistoricalDataset CarbonSource = "historical-dataset"
	SourceSynthetic         CarbonSource = "synthetic"
	SourceMock              CarbonSource = "mock"
)

// EmissionFactorType is the CI accounting convention. Scoring accepts lifecycle only.
type EmissionFactorType string

const (
	EmissionLifecycle EmissionFactorType = "lifecycle"
	EmissionDirect    EmissionFactorType = "direct"
)

// TemporalGranularity is the width of one CI grid interval.
type TemporalGranularity string

const (
	Granularity5Minutes  TemporalGranularity = "5_minutes"
	Granularity15Minutes TemporalGranularity = "15_minutes"
	Granularity30Minutes TemporalGranularity = "30_minutes"
	Granularity60Minutes TemporalGranularity = "60_minutes"
)

// IntervalSeconds returns the canonical width in seconds, or 0 if unknown.
func (g TemporalGranularity) IntervalSeconds() int64 {
	switch g {
	case Granularity5Minutes:
		return 300
	case Granularity15Minutes:
		return 900
	case Granularity30Minutes:
		return 1800
	case Granularity60Minutes:
		return 3600
	default:
		return 0
	}
}

// ForecastMode describes how CDS populated the forecast array.
// Persistence filling of future actuals is forbidden (model §6.5): mode "persistence"
// MUST be accompanied by an empty interval list; the plugin applies (2.11).
type ForecastMode string

const (
	ForecastModeNone        ForecastMode = "none"
	ForecastModePersistence ForecastMode = "persistence"
	ForecastModeRecorded    ForecastMode = "recorded"
)

// ProviderMode is the CarbonProvider operating mode (Live vs Replay vs Mock).
type ProviderMode string

const (
	ProviderModeLive   ProviderMode = "live"
	ProviderModeReplay ProviderMode = "replay"
	ProviderModeMock   ProviderMode = "mock"
)

// ServingStatus is the CDS health aggregate.
type ServingStatus string

const (
	ServingOK          ServingStatus = "ok"
	ServingDegraded    ServingStatus = "degraded"
	ServingUnavailable ServingStatus = "unavailable"
)

// WorkloadClass is the Pod label carbon-aware.io/workload-class.
type WorkloadClass string

const (
	WorkloadClassBatch            WorkloadClass = "batch"
	WorkloadClassFlexible         WorkloadClass = "flexible"
	WorkloadClassLatencySensitive WorkloadClass = "latency-sensitive"
)

// IsKnown reports whether c is one of the three legal classes.
func (c WorkloadClass) IsKnown() bool {
	switch c {
	case WorkloadClassBatch, WorkloadClassFlexible, WorkloadClassLatencySensitive:
		return true
	default:
		return false
	}
}

// ScoringMode selects the ranking formula. expected-cf is (2.19); current-ci is (2.23).
type ScoringMode string

const (
	ScoringModeExpectedCF ScoringMode = "expected-cf"
	ScoringModeCurrentCI  ScoringMode = "current-ci"
)

// FallbackMode is the PreScore snapshot classification stored in CycleState (T3.4 §7.5).
type FallbackMode string

const (
	FallbackFresh      FallbackMode = "fresh"
	FallbackStale      FallbackMode = "stale"
	FallbackDead       FallbackMode = "dead"
	FallbackEmpty      FallbackMode = "empty"
	FallbackAllInvalid FallbackMode = "all_invalid"
	FallbackOptOut     FallbackMode = "opt_out"
)

// FrameworkStatusCode is the Scheduler Framework Status code CarbonAware may emit.
type FrameworkStatusCode string

const (
	FrameworkSuccess FrameworkStatusCode = "Success"
	FrameworkError   FrameworkStatusCode = "Error"
)

// StatusReason is a Kubernetes-style Status.reason for CDS errors.
type StatusReason string

const (
	ReasonNotFound           StatusReason = "NotFound"
	ReasonBadRequest         StatusReason = "BadRequest"
	ReasonServiceUnavailable StatusReason = "ServiceUnavailable"
)

// CarbonRecord is the canonical current carbon-intensity observation for one zone.
// Wire unit of CarbonIntensityGCO2ePerKWh is always gCO2e/kWh (DOD-DOC-05, GO-11).
// Timestamp is the left-closed start of the observation interval, not the HTTP time.
type CarbonRecord struct {
	APIVersion string `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty" yaml:"kind,omitempty"`

	Zone                       ZoneID              `json:"zone" yaml:"zone"`
	Timestamp                  RFC3339Time         `json:"timestamp" yaml:"timestamp"`
	CarbonIntensityGCO2ePerKWh float64             `json:"carbonIntensity" yaml:"carbonIntensity"`
	Unit                       IntensityUnit       `json:"unit" yaml:"unit"`
	Source                     CarbonSource        `json:"source" yaml:"source"`
	SourceZone                 string              `json:"sourceZone,omitempty" yaml:"sourceZone,omitempty"`
	EmissionFactorType         EmissionFactorType  `json:"emissionFactorType" yaml:"emissionFactorType"`
	TemporalGranularity        TemporalGranularity `json:"temporalGranularity,omitempty" yaml:"temporalGranularity,omitempty"`
	IntervalSeconds            int64               `json:"intervalSeconds,omitempty" yaml:"intervalSeconds,omitempty"`
	Quality                    DataQuality         `json:"quality" yaml:"quality"`
	FreshnessSeconds           int64               `json:"freshnessSeconds" yaml:"freshnessSeconds"`
	CarbonFreePercentage       *float64            `json:"carbonFreePercentage,omitempty" yaml:"carbonFreePercentage,omitempty"`
	IsEstimated                bool                `json:"isEstimated,omitempty" yaml:"isEstimated,omitempty"`
	EstimationMethod           *string             `json:"estimationMethod,omitempty" yaml:"estimationMethod,omitempty"`
	FlowTraced                 *bool               `json:"flowTraced,omitempty" yaml:"flowTraced,omitempty"`
	FetchedAt                  RFC3339Time         `json:"fetchedAt,omitempty" yaml:"fetchedAt,omitempty"`
	UpdatedAt                  RFC3339Time         `json:"updatedAt,omitempty" yaml:"updatedAt,omitempty"`
	ExpiresAt                  RFC3339Time         `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
}

// Validate checks the G-10 domain and mandatory provenance fields.
func (r CarbonRecord) Validate() error {
	if err := r.Zone.Validate(); err != nil {
		return err
	}
	if r.Timestamp.Time.IsZero() {
		return fmt.Errorf("timestamp is required: %w", ErrInvalidCarbonIntensity)
	}
	if r.Unit != "" && r.Unit != IntensityUnitGCO2ePerKWh {
		return fmt.Errorf("unit %q is not %s: %w", r.Unit, IntensityUnitGCO2ePerKWh, ErrInvalidCarbonIntensity)
	}
	if err := ValidateCarbonIntensity(r.CarbonIntensityGCO2ePerKWh); err != nil {
		return err
	}
	if r.EmissionFactorType == EmissionDirect {
		return fmt.Errorf("zone %s: %w", r.Zone, ErrNotLifecycle)
	}
	if r.EmissionFactorType != "" && r.EmissionFactorType != EmissionLifecycle {
		return fmt.Errorf("emissionFactorType %q is unknown: %w", r.EmissionFactorType, ErrNotLifecycle)
	}
	if r.IntervalSeconds < 0 {
		return fmt.Errorf("intervalSeconds must be >= 0: %w", ErrInvalidDuration)
	}
	if r.FreshnessSeconds < 0 {
		return fmt.Errorf("freshnessSeconds must be >= 0: %w", ErrStaleData)
	}
	return nil
}

// IsUsableForScoring reports whether the record may enter (2.10) / (2.23).
func (r CarbonRecord) IsUsableForScoring() bool {
	if r.Validate() != nil {
		return false
	}
	if r.Quality != "" && !r.Quality.IsUsableForScoring() {
		return false
	}
	return true
}

// ForecastInterval is one piecewise-constant CI segment on [Timestamp, ExpiresAt).
// Empty Forecast arrays mean "no external forecast"; the plugin applies persistence (2.11).
type ForecastInterval struct {
	Timestamp                  RFC3339Time   `json:"timestamp" yaml:"timestamp"`
	ExpiresAt                  RFC3339Time   `json:"expiresAt,omitempty" yaml:"expiresAt,omitempty"`
	DurationSeconds            int64         `json:"durationSeconds,omitempty" yaml:"durationSeconds,omitempty"`
	HorizonSeconds             int64         `json:"horizonSeconds,omitempty" yaml:"horizonSeconds,omitempty"`
	CarbonIntensityGCO2ePerKWh float64       `json:"carbonIntensity" yaml:"carbonIntensity"`
	Unit                       IntensityUnit `json:"unit,omitempty" yaml:"unit,omitempty"`
}

// Validate checks G-10 and non-negative durations. Δt_k <= 0 is rejected here so
// the calculator can drop the piece (G-15) without guessing.
func (i ForecastInterval) Validate() error {
	if i.Timestamp.Time.IsZero() {
		return fmt.Errorf("forecast interval timestamp is required: %w", ErrInvalidCarbonIntensity)
	}
	if err := ValidateCarbonIntensity(i.CarbonIntensityGCO2ePerKWh); err != nil {
		return err
	}
	if i.Unit != "" && i.Unit != IntensityUnitGCO2ePerKWh {
		return fmt.Errorf("unit %q is not %s: %w", i.Unit, IntensityUnitGCO2ePerKWh, ErrInvalidCarbonIntensity)
	}
	if i.DurationSeconds < 0 || i.HorizonSeconds < 0 {
		return fmt.Errorf("forecast interval duration is negative: %w", ErrInvalidDuration)
	}
	return nil
}

// CarbonForecast is the REST envelope of GET /api/v1/zones/{zone}/forecast.
type CarbonForecast struct {
	APIVersion          string              `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind                string              `json:"kind,omitempty" yaml:"kind,omitempty"`
	Zone                ZoneID              `json:"zone" yaml:"zone"`
	Unit                IntensityUnit       `json:"unit" yaml:"unit"`
	IssuedAt            RFC3339Time         `json:"issuedAt" yaml:"issuedAt"`
	Source              CarbonSource        `json:"source,omitempty" yaml:"source,omitempty"`
	EmissionFactorType  EmissionFactorType  `json:"emissionFactorType,omitempty" yaml:"emissionFactorType,omitempty"`
	TemporalGranularity TemporalGranularity `json:"temporalGranularity,omitempty" yaml:"temporalGranularity,omitempty"`
	ForecastMode        ForecastMode        `json:"forecastMode" yaml:"forecastMode"`
	Horizon             string              `json:"horizon,omitempty" yaml:"horizon,omitempty"`
	FreshnessSeconds    int64               `json:"freshnessSeconds,omitempty" yaml:"freshnessSeconds,omitempty"`
	Intervals           []ForecastInterval  `json:"forecast" yaml:"forecast"`
}

// Validate checks envelope invariants. An empty Intervals slice is legal (G-06).
func (f CarbonForecast) Validate() error {
	if err := f.Zone.Validate(); err != nil {
		return err
	}
	if f.Unit != "" && f.Unit != IntensityUnitGCO2ePerKWh {
		return fmt.Errorf("unit %q is not %s: %w", f.Unit, IntensityUnitGCO2ePerKWh, ErrInvalidCarbonIntensity)
	}
	if f.EmissionFactorType == EmissionDirect {
		return fmt.Errorf("zone %s: %w", f.Zone, ErrNotLifecycle)
	}
	if len(f.Intervals) > MaxForecastIntervals {
		return fmt.Errorf("forecast has %d intervals, max %d: %w", len(f.Intervals), MaxForecastIntervals, ErrInvalidDuration)
	}
	var prev time.Time
	for idx, interval := range f.Intervals {
		if err := interval.Validate(); err != nil {
			return fmt.Errorf("forecast[%d]: %w", idx, err)
		}
		if !prev.IsZero() && !interval.Timestamp.Time.After(prev) {
			return fmt.Errorf("forecast timestamps must be strictly increasing: %w", ErrInvalidDuration)
		}
		prev = interval.Timestamp.Time
	}
	if (f.ForecastMode == ForecastModeNone || f.ForecastMode == ForecastModePersistence) && len(f.Intervals) > 0 {
		return fmt.Errorf("forecastMode %q must have an empty forecast array (plugin applies persistence): %w", f.ForecastMode, ErrInvalidDuration)
	}
	return nil
}

// ZoneProfile is the catalogue entry returned by GET /api/v1/zones.
type ZoneProfile struct {
	APIVersion          string              `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind                string              `json:"kind,omitempty" yaml:"kind,omitempty"`
	Zone                ZoneID              `json:"zone" yaml:"zone"`
	SourceZone          string              `json:"sourceZone,omitempty" yaml:"sourceZone,omitempty"`
	Quality             DataQuality         `json:"quality" yaml:"quality"`
	Source              CarbonSource        `json:"source,omitempty" yaml:"source,omitempty"`
	TemporalGranularity TemporalGranularity `json:"temporalGranularity,omitempty" yaml:"temporalGranularity,omitempty"`
	IntervalSeconds     int64               `json:"intervalSeconds,omitempty" yaml:"intervalSeconds,omitempty"`
	HasCurrent          bool                `json:"hasCurrent" yaml:"hasCurrent"`
	HasForecast         bool                `json:"hasForecast" yaml:"hasForecast"`
	FreshnessSeconds    int64               `json:"freshnessSeconds" yaml:"freshnessSeconds"`
}

// Validate checks the zone id and freshness.
func (z ZoneProfile) Validate() error {
	if err := z.Zone.Validate(); err != nil {
		return err
	}
	if z.FreshnessSeconds < 0 {
		return fmt.Errorf("freshnessSeconds must be >= 0: %w", ErrStaleData)
	}
	return nil
}

// ZoneProfileList is the REST envelope of GET /api/v1/zones.
type ZoneProfileList struct {
	APIVersion        string        `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind              string        `json:"kind,omitempty" yaml:"kind,omitempty"`
	SnapshotFetchedAt RFC3339Time   `json:"snapshotFetchedAt,omitempty" yaml:"snapshotFetchedAt,omitempty"`
	Items             []ZoneProfile `json:"items" yaml:"items"`
}

// HealthStatus is the body of GET /api/v1/health and GET /api/v1/healthz.
type HealthStatus struct {
	APIVersion        string        `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind              string        `json:"kind,omitempty" yaml:"kind,omitempty"`
	Status            ServingStatus `json:"status" yaml:"status"`
	Mode              ProviderMode  `json:"mode" yaml:"mode"`
	Ready             bool          `json:"ready" yaml:"ready"`
	DataAgeSeconds    int64         `json:"dataAgeSeconds" yaml:"dataAgeSeconds"`
	MaxDataAgeSeconds int64         `json:"maxDataAgeSeconds" yaml:"maxDataAgeSeconds"`
	ZonesReady        int           `json:"zonesReady" yaml:"zonesReady"`
	ZonesTotal        int           `json:"zonesTotal" yaml:"zonesTotal"`
	ModelTime         *RFC3339Time  `json:"modelTime,omitempty" yaml:"modelTime,omitempty"`
	Message           string        `json:"message,omitempty" yaml:"message,omitempty"`
}

// HTTPStatusCode returns 200 for ok/degraded and 503 for unavailable.
func (h HealthStatus) HTTPStatusCode() int {
	if h.Status == ServingUnavailable || !h.Ready {
		return 503
	}
	return 200
}

// Status is a Kubernetes-style error body for CDS REST failures.
type Status struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind" yaml:"kind"`
	Status     string         `json:"status" yaml:"status"`
	Message    string         `json:"message" yaml:"message"`
	Reason     StatusReason   `json:"reason" yaml:"reason"`
	Code       int            `json:"code" yaml:"code"`
	Details    *StatusDetails `json:"details,omitempty" yaml:"details,omitempty"`
}

// StatusDetails names the offending resource, if any.
type StatusDetails struct {
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`
}

// NewStatus constructs a Failure Status envelope.
func NewStatus(code int, reason StatusReason, message string) Status {
	return Status{
		APIVersion: "v1",
		Kind:       KindStatus,
		Status:     "Failure",
		Message:    message,
		Reason:     reason,
		Code:       code,
	}
}

// NodeEnergyProfile is the energy and identity view of one node used by EnergyModel.
type NodeEnergyProfile struct {
	APIVersion               string  `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind                     string  `json:"kind,omitempty" yaml:"kind,omitempty"`
	NodeName                 string  `json:"nodeName" yaml:"nodeName"`
	Zone                     ZoneID  `json:"zone,omitempty" yaml:"zone,omitempty"`
	PowerProfile             string  `json:"powerProfile,omitempty" yaml:"powerProfile,omitempty"`
	PowerIdleWatts           float64 `json:"powerIdleWatts" yaml:"powerIdleWatts"`
	PowerMaxWatts            float64 `json:"powerMaxWatts" yaml:"powerMaxWatts"`
	CPUAllocatableMillicores int64   `json:"cpuAllocatableMillicores" yaml:"cpuAllocatableMillicores"`
	MemoryAllocatableBytes   int64   `json:"memoryAllocatableBytes" yaml:"memoryAllocatableBytes"`
	CPURequestedMillicores   int64   `json:"cpuRequestedMillicores,omitempty" yaml:"cpuRequestedMillicores,omitempty"`
	MemoryRequestedBytes     int64   `json:"memoryRequestedBytes,omitempty" yaml:"memoryRequestedBytes,omitempty"`
	HasZone                  bool    `json:"hasZone" yaml:"hasZone"`
}

// Validate checks domain (2.2a) on the power envelope. A missing zone is legal (G-09).
func (p NodeEnergyProfile) Validate() error {
	if p.NodeName == "" {
		return fmt.Errorf("nodeName is required: %w", ErrInvalidPluginArgs)
	}
	if err := validatePowerWatts(p.PowerIdleWatts, p.PowerMaxWatts); err != nil {
		return err
	}
	if p.HasZone {
		if err := p.Zone.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// WorkloadProfile is the carbon-aware view of a Pod after labels/annotations are parsed.
type WorkloadProfile struct {
	APIVersion              string        `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind                    string        `json:"kind,omitempty" yaml:"kind,omitempty"`
	Namespace               string        `json:"namespace" yaml:"namespace"`
	Name                    string        `json:"name" yaml:"name"`
	UID                     string        `json:"uid,omitempty" yaml:"uid,omitempty"`
	Enabled                 bool          `json:"enabled" yaml:"enabled"`
	Class                   WorkloadClass `json:"workloadClass,omitempty" yaml:"workloadClass,omitempty"`
	CPURequestMillicores    int64         `json:"cpuRequestMillicores" yaml:"cpuRequestMillicores"`
	MemoryRequestBytes      int64         `json:"memoryRequestBytes" yaml:"memoryRequestBytes"`
	ExpectedDurationSeconds *int64        `json:"expectedDurationSeconds,omitempty" yaml:"expectedDurationSeconds,omitempty"`
	SLADeadlineSeconds      *int64        `json:"slaDeadlineSeconds,omitempty" yaml:"slaDeadlineSeconds,omitempty"`
	CarbonWeight            *float64      `json:"carbonWeight,omitempty" yaml:"carbonWeight,omitempty"`
}

// HasFiniteSLA reports whether a relative deadline annotation was set (G-13 otherwise).
func (p WorkloadProfile) HasFiniteSLA() bool {
	return p.SLADeadlineSeconds != nil
}

// NewWorkloadProfile parses carbon-aware.io Pod labels and annotations.
// CPU/memory requests are filled by the caller from PodSpec, not from annotations.
func NewWorkloadProfile(namespace, name, uid string, labels, annotations map[string]string) (WorkloadProfile, error) {
	profile := WorkloadProfile{
		APIVersion: GroupVersion,
		Kind:       KindWorkloadProfile,
		Namespace:  namespace,
		Name:       name,
		UID:        uid,
	}
	enabled, err := parseEnabled(labels)
	if err != nil {
		return WorkloadProfile{}, err
	}
	profile.Enabled = enabled

	if raw, ok := labels[LabelWorkloadClass]; ok && raw != "" {
		class := WorkloadClass(raw)
		if !class.IsKnown() {
			return WorkloadProfile{}, fmt.Errorf("%s=%q: %w", LabelWorkloadClass, raw, ErrUnknownWorkloadClass)
		}
		profile.Class = class
	}

	if raw, ok := annotations[AnnotationExpectedDuration]; ok && raw != "" {
		seconds, err := ParseDurationSeconds(raw)
		if err != nil {
			return WorkloadProfile{}, fmt.Errorf("%s: %w", AnnotationExpectedDuration, err)
		}
		profile.ExpectedDurationSeconds = &seconds
	}

	if raw, ok := annotations[AnnotationSLADeadline]; ok && raw != "" {
		seconds, err := ParseDurationSeconds(raw)
		if err != nil {
			return WorkloadProfile{}, fmt.Errorf("%s: %w", AnnotationSLADeadline, err)
		}
		profile.SLADeadlineSeconds = &seconds
	}

	if raw, ok := annotations[AnnotationCarbonWeight]; ok && raw != "" {
		weight, err := ParseCarbonWeight(raw)
		if err != nil {
			return WorkloadProfile{}, fmt.Errorf("%s: %w", AnnotationCarbonWeight, err)
		}
		profile.CarbonWeight = &weight
	}

	if _, ok := annotations[LabelEnabled]; ok {
		return WorkloadProfile{}, fmt.Errorf("%s must be a label, not an annotation: %w", LabelEnabled, ErrInvalidAnnotation)
	}
	if _, ok := annotations[LabelWorkloadClass]; ok {
		return WorkloadProfile{}, fmt.Errorf("%s must be a label, not an annotation: %w", LabelWorkloadClass, ErrInvalidAnnotation)
	}
	return profile, nil
}

// ParseNodeEnergyIdentity extracts zone and power-profile from Node labels (G-09).
func ParseNodeEnergyIdentity(nodeName string, labels map[string]string) NodeEnergyProfile {
	profile := NodeEnergyProfile{
		APIVersion:   GroupVersion,
		Kind:         KindNodeEnergyProfile,
		NodeName:     nodeName,
		PowerProfile: PowerProfileStandard,
	}
	if labels == nil {
		return profile
	}
	if raw, ok := labels[LabelZone]; ok && raw != "" {
		z := ZoneID(raw)
		if z.Validate() == nil {
			profile.Zone = z
			profile.HasZone = true
		}
	}
	if raw, ok := labels[LabelPowerProfile]; ok && raw != "" {
		profile.PowerProfile = raw
	}
	return profile
}

// CandidateScore is one feasible node's audit snapshot inside SchedulingDecisionRecord.
type CandidateScore struct {
	NodeName             string  `json:"nodeName" yaml:"nodeName"`
	Zone                 ZoneID  `json:"zone,omitempty" yaml:"zone,omitempty"`
	HasZone              bool    `json:"hasZone" yaml:"hasZone"`
	EstimatedCarbonGCO2e float64 `json:"estimatedCarbonGCO2e" yaml:"estimatedCarbonGCO2e"`
	EstimatedEnergyKWh   float64 `json:"estimatedEnergyKWh" yaml:"estimatedEnergyKWh"`
	CarbonScore          float64 `json:"carbonScore" yaml:"carbonScore"`
	ResourceScore        float64 `json:"resourceScore" yaml:"resourceScore"`
	PerformanceScore     float64 `json:"performanceScore" yaml:"performanceScore"`
	PluginScore          float64 `json:"pluginScore" yaml:"pluginScore"`
	FrameworkScore       int64   `json:"frameworkScore" yaml:"frameworkScore"`
}

// SchedulingDecisionRecord is the audit/metrics projection of one scheduling attempt.
// It is not a CDS REST resource and MUST NOT be exposed as a "compute score" API.
type SchedulingDecisionRecord struct {
	APIVersion           string           `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind                 string           `json:"kind,omitempty" yaml:"kind,omitempty"`
	Timestamp            RFC3339Time      `json:"timestamp" yaml:"timestamp"`
	PodNamespace         string           `json:"podNamespace" yaml:"podNamespace"`
	PodName              string           `json:"podName" yaml:"podName"`
	PodUID               string           `json:"podUID,omitempty" yaml:"podUID,omitempty"`
	SchedulerName        string           `json:"schedulerName,omitempty" yaml:"schedulerName,omitempty"`
	ScoringMode          ScoringMode      `json:"scoringMode" yaml:"scoringMode"`
	SelectedNode         string           `json:"selectedNode,omitempty" yaml:"selectedNode,omitempty"`
	SelectedZone         ZoneID           `json:"selectedZone,omitempty" yaml:"selectedZone,omitempty"`
	CarbonScore          float64          `json:"carbonScore" yaml:"carbonScore"`
	ResourceScore        float64          `json:"resourceScore" yaml:"resourceScore"`
	PerformanceScore     float64          `json:"performanceScore" yaml:"performanceScore"`
	PluginScore          float64          `json:"pluginScore" yaml:"pluginScore"`
	FrameworkScore       int64            `json:"frameworkScore" yaml:"frameworkScore"`
	EstimatedEnergyKWh   float64          `json:"estimatedEnergyKWh" yaml:"estimatedEnergyKWh"`
	EstimatedCarbonGCO2e float64          `json:"estimatedCarbonGCO2e" yaml:"estimatedCarbonGCO2e"`
	DataQuality          DataQuality      `json:"dataQuality,omitempty" yaml:"dataQuality,omitempty"`
	FallbackMode         FallbackMode     `json:"fallbackMode,omitempty" yaml:"fallbackMode,omitempty"`
	FallbackApplied      bool             `json:"fallbackApplied" yaml:"fallbackApplied"`
	SLARejected          bool             `json:"slaRejected" yaml:"slaRejected"`
	OptInDisabled        bool             `json:"optInDisabled" yaml:"optInDisabled"`
	Candidates           []CandidateScore `json:"candidates,omitempty" yaml:"candidates,omitempty"`
}

// PowerProfileSpec is one named (P_idle, P_max) pair in plugin args.
type PowerProfileSpec struct {
	PowerIdleWatts float64 `json:"powerIdleWatts" yaml:"powerIdleWatts"`
	PowerMaxWatts  float64 `json:"powerMaxWatts" yaml:"powerMaxWatts"`
}

// CarbonAwarePluginArgs is pluginConfig.args for CarbonAware (KubeSchedulerConfiguration).
type CarbonAwarePluginArgs struct {
	APIVersion                   string                      `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind                         string                      `json:"kind,omitempty" yaml:"kind,omitempty"`
	ScoringMode                  ScoringMode                 `json:"scoringMode" yaml:"scoringMode"`
	CarbonWeight                 float64                     `json:"carbonWeight" yaml:"carbonWeight"`
	ResourceWeight               float64                     `json:"resourceWeight" yaml:"resourceWeight"`
	PerformanceWeight            float64                     `json:"performanceWeight" yaml:"performanceWeight"`
	Lambda                       float64                     `json:"lambda" yaml:"lambda"`
	PowerIdleWatts               float64                     `json:"powerIdleWatts" yaml:"powerIdleWatts"`
	PowerMaxWatts                float64                     `json:"powerMaxWatts" yaml:"powerMaxWatts"`
	PowerProfiles                map[string]PowerProfileSpec `json:"powerProfiles,omitempty" yaml:"powerProfiles,omitempty"`
	RuntimeClassSeconds          map[WorkloadClass]int64     `json:"runtimeClassSeconds,omitempty" yaml:"runtimeClassSeconds,omitempty"`
	DefaultWorkloadClass         WorkloadClass               `json:"defaultWorkloadClass,omitempty" yaml:"defaultWorkloadClass,omitempty"`
	MaxDataAgeSeconds            int64                       `json:"maxDataAgeSeconds" yaml:"maxDataAgeSeconds"`
	RefreshIntervalSeconds       int64                       `json:"refreshIntervalSeconds" yaml:"refreshIntervalSeconds"`
	HTTPTimeoutSeconds           int64                       `json:"httpTimeoutSeconds" yaml:"httpTimeoutSeconds"`
	StaleWarnSeconds             int64                       `json:"staleWarnSeconds" yaml:"staleWarnSeconds"`
	ClockSkewSeconds             int64                       `json:"clockSkewSeconds" yaml:"clockSkewSeconds"`
	CarbonServiceURL             string                      `json:"carbonServiceURL" yaml:"carbonServiceURL"`
	AllowPodCarbonWeightOverride bool                        `json:"allowPodCarbonWeightOverride" yaml:"allowPodCarbonWeightOverride"`
	EpsilonCFGCO2e               float64                     `json:"epsilonCFGCO2e,omitempty" yaml:"epsilonCFGCO2e,omitempty"`
	EpsilonCIGCO2ePerKWh         float64                     `json:"epsilonCIGCO2ePerKWh,omitempty" yaml:"epsilonCIGCO2ePerKWh,omitempty"`
}

// Validate checks G-03, G-18 and required operational fields. The plugin MUST
// fail closed at start when this returns an error (ARCH-10 / G-03 / G-18).
func (a CarbonAwarePluginArgs) Validate() error {
	switch a.ScoringMode {
	case ScoringModeExpectedCF, ScoringModeCurrentCI:
	default:
		return fmt.Errorf("scoringMode %q: %w", a.ScoringMode, ErrInvalidPluginArgs)
	}
	if err := validatePowerWatts(a.PowerIdleWatts, a.PowerMaxWatts); err != nil {
		return err
	}
	for name, spec := range a.PowerProfiles {
		if err := validatePowerWatts(spec.PowerIdleWatts, spec.PowerMaxWatts); err != nil {
			return fmt.Errorf("powerProfiles[%s]: %w", name, err)
		}
	}
	if a.Lambda < 0 || a.Lambda > 1 || math.IsNaN(a.Lambda) || math.IsInf(a.Lambda, 0) {
		return fmt.Errorf("lambda=%v is not in [0,1]: %w", a.Lambda, ErrInvalidPluginArgs)
	}
	if a.CarbonWeight < 0 || a.ResourceWeight < 0 || a.PerformanceWeight < 0 {
		return fmt.Errorf("negative score weight: %w", ErrInvalidPluginArgs)
	}
	sum := a.CarbonWeight + a.ResourceWeight + a.PerformanceWeight
	if math.Abs(sum-1) > EpsilonWeight {
		return fmt.Errorf("carbonWeight+resourceWeight+performanceWeight=%v (want 1): %w", sum, ErrInvalidPluginArgs)
	}
	if a.MaxDataAgeSeconds <= 0 || a.RefreshIntervalSeconds <= 0 || a.HTTPTimeoutSeconds <= 0 {
		return fmt.Errorf("timeouts must be positive seconds: %w", ErrInvalidPluginArgs)
	}
	if a.StaleWarnSeconds <= 0 || a.ClockSkewSeconds < 0 {
		return fmt.Errorf("staleWarnSeconds must be > 0 and clockSkewSeconds >= 0: %w", ErrInvalidPluginArgs)
	}
	if a.RefreshIntervalSeconds >= a.MaxDataAgeSeconds {
		return fmt.Errorf("refreshIntervalSeconds must be << maxDataAgeSeconds: %w", ErrInvalidPluginArgs)
	}
	if a.StaleWarnSeconds >= a.MaxDataAgeSeconds {
		return fmt.Errorf("staleWarnSeconds must be < maxDataAgeSeconds: %w", ErrInvalidPluginArgs)
	}
	if strings.TrimSpace(a.CarbonServiceURL) == "" {
		return fmt.Errorf("carbonServiceURL is required: %w", ErrInvalidPluginArgs)
	}
	if a.DefaultWorkloadClass != "" && !a.DefaultWorkloadClass.IsKnown() {
		return fmt.Errorf("defaultWorkloadClass %q: %w", a.DefaultWorkloadClass, ErrUnknownWorkloadClass)
	}
	for class, seconds := range a.RuntimeClassSeconds {
		if !class.IsKnown() {
			return fmt.Errorf("runtimeClassSeconds[%q]: %w", class, ErrUnknownWorkloadClass)
		}
		if seconds < 0 {
			return fmt.Errorf("runtimeClassSeconds[%s] is negative: %w", class, ErrInvalidDuration)
		}
	}
	return nil
}

// CarbonCycleState is the immutable PreScore payload stored in CycleState.
// After Write it MUST NOT be mutated. Clone returns the same pointer.
type CarbonCycleState struct {
	ScorePerNode      map[string]int64 `json:"scorePerNode" yaml:"scorePerNode"`
	FallbackMode      FallbackMode     `json:"fallbackMode" yaml:"fallbackMode"`
	DataStale         bool             `json:"dataStale" yaml:"dataStale"`
	ZonesMissing      int              `json:"zonesMissing" yaml:"zonesMissing"`
	InvalidZones      int              `json:"invalidZones" yaml:"invalidZones"`
	ScoringMode       ScoringMode      `json:"scoringMode" yaml:"scoringMode"`
	SLARejected       bool             `json:"slaRejected" yaml:"slaRejected"`
	OptInDisabled     bool             `json:"optInDisabled" yaml:"optInDisabled"`
	SnapshotFetchedAt RFC3339Time      `json:"snapshotFetchedAt,omitempty" yaml:"snapshotFetchedAt,omitempty"`
	PodUID            string           `json:"podUID,omitempty" yaml:"podUID,omitempty"`
}

// NeutralFixedPointScore is Score=50 stored as rnd(100*Score) = 5000.
func NeutralFixedPointScore() int64 {
	return NeutralPluginScore * ScoreFixedPointScale
}

// Clone implements the Scheduler Framework StateData contract: the value is
// immutable, so a shallow identity clone is race-free.
func (s *CarbonCycleState) Clone() *CarbonCycleState {
	return s
}

// ZoneCarbonState is one zone inside the plugin carbon snapshot.
type ZoneCarbonState struct {
	Current  CarbonRecord       `json:"current" yaml:"current"`
	Forecast []ForecastInterval `json:"forecast" yaml:"forecast"`
}

// CarbonSnapshot is the immutable in-memory snapshot published by Cache Sync Worker.
type CarbonSnapshot struct {
	FetchedAt RFC3339Time                `json:"fetchedAt" yaml:"fetchedAt"`
	Quality   DataQuality                `json:"quality" yaml:"quality"`
	Zones     map[ZoneID]ZoneCarbonState `json:"zones" yaml:"zones"`
}

// CarbonProvider is the narrow provider interface (GO-16). Live, Replay and Mock
// implement the same methods; the plugin never sees the difference on the REST wire.
type CarbonProvider interface {
	Name() string
	Mode() ProviderMode
	ListZones(ctx context.Context) ([]ZoneProfile, error)
	Current(ctx context.Context, zone ZoneID, at time.Time) (CarbonRecord, error)
	Forecast(ctx context.Context, zone ZoneID, at time.Time, horizon time.Duration) ([]ForecastInterval, error)
}

// EnergyModel is the pure power model (2.2)–(2.4). Implementations MUST NOT do I/O.
type EnergyModel interface {
	NodePowerWatts(profile NodeEnergyProfile, utilization float64) (float64, error)
	PodMarginalPowerWatts(profile NodeEnergyProfile, utilizationBefore, utilizationAfter float64) (float64, error)
}

// CarbonCalculator is the pure expected-footprint model (2.10s) plus persistence (2.11)
// when forecast is empty. Implementations MUST NOT do I/O and MUST NOT call CDS.
type CarbonCalculator interface {
	ExpectedFootprintGCO2e(podPowerWatts float64, horizonSeconds int64, current CarbonRecord, forecast []ForecastInterval) (float64, error)
}

// Clock is injectable time for CDS replay (model time) and plugin fetchedAt (DOD-TEST-01).
type Clock interface {
	Now() time.Time
}

// SystemClock is wall-clock time. It MUST NOT be used inside unit tests of scoring.
type SystemClock struct{}

// Now returns time.Now in UTC.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

// FakeClock is a race-safe deterministic clock for tests and replay.
type FakeClock struct {
	mu      sync.Mutex
	current time.Time
}

// NewFakeClock returns a FakeClock fixed at t (stored as UTC).
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{current: t.UTC()}
}

// Now implements Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// Set replaces the current instant.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = t.UTC()
}

// Advance moves the clock forward by d. Negative d is ignored.
func (c *FakeClock) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

// ParseDurationSeconds parses a Kubernetes-style duration used in query parameters
// and Pod annotations (examples: "300s", "1800s", "2h"). A bare integer is rejected:
// time.ParseDuration("300") is 300 nanoseconds and would silently destroy SLA.
func ParseDurationSeconds(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("duration is empty: %w", ErrInvalidDuration)
	}
	if !hasDurationUnit(value) {
		return 0, fmt.Errorf("duration %q must include a unit suffix (ns, us, ms, s, m, h): %w", value, ErrInvalidDuration)
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", value, ErrInvalidDuration)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q is negative: %w", value, ErrInvalidDuration)
	}
	if d%time.Second != 0 {
		return 0, fmt.Errorf("duration %q is not a whole number of seconds: %w", value, ErrInvalidDuration)
	}
	return int64(d / time.Second), nil
}

// FormatDurationSeconds renders seconds as a Kubernetes-style duration with an "s" suffix.
func FormatDurationSeconds(seconds int64) string {
	return strconv.FormatInt(seconds, 10) + "s"
}

// ParseCarbonWeight parses annotation carbon-aware.io/carbon-weight in [0, 1].
func ParseCarbonWeight(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("carbon-weight is empty: %w", ErrInvalidAnnotation)
	}
	if strings.ContainsAny(value, ",eE") {
		return 0, fmt.Errorf("carbon-weight %q must be a decimal in [0,1] without exponent: %w", value, ErrInvalidAnnotation)
	}
	w, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("carbon-weight %q: %w", value, ErrInvalidAnnotation)
	}
	if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 || w > 1 {
		return 0, fmt.Errorf("carbon-weight %q is not in [0,1]: %w", value, ErrInvalidAnnotation)
	}
	return w, nil
}

// TimestampFromFuture reports FF-FUT: observation timestamp is more than
// clockSkewSeconds ahead of clock now (G-10).
func TimestampFromFuture(timestamp, now time.Time, clockSkewSeconds int64) bool {
	if timestamp.IsZero() {
		return false
	}
	skew := time.Duration(clockSkewSeconds) * time.Second
	return timestamp.UTC().After(now.UTC().Add(skew))
}

// ValidateCarbonIntensity enforces G-10 on a scalar CI value (gCO2e/kWh).
func ValidateCarbonIntensity(ci float64) error {
	if math.IsNaN(ci) || math.IsInf(ci, 0) {
		return fmt.Errorf("carbon intensity is NaN or Inf: %w", ErrInvalidCarbonIntensity)
	}
	if ci < CarbonIntensityMinGCO2ePerKWh || ci > CarbonIntensityMaxGCO2ePerKWh {
		return fmt.Errorf("carbon intensity %v gCO2e/kWh is outside [%v, %v]: %w",
			ci, CarbonIntensityMinGCO2ePerKWh, CarbonIntensityMaxGCO2ePerKWh, ErrInvalidCarbonIntensity)
	}
	return nil
}

func parseEnabled(labels map[string]string) (bool, error) {
	if labels == nil {
		return false, nil
	}
	raw, ok := labels[LabelEnabled]
	if !ok || raw == "" {
		return false, nil
	}
	switch raw {
	case EnabledTrue:
		return true, nil
	case EnabledFalse:
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q is not %q or %q: %w",
			LabelEnabled, raw, EnabledTrue, EnabledFalse, ErrInvalidAnnotation)
	}
}

func hasDurationUnit(value string) bool {
	if len(value) == 0 {
		return false
	}
	last := value[len(value)-1]
	switch last {
	case 's', 'm', 'h':
		return true
	}
	return strings.HasSuffix(value, "ns") || strings.HasSuffix(value, "us") ||
		strings.HasSuffix(value, "µs") || strings.HasSuffix(value, "ms")
}

func validatePowerWatts(idle, max float64) error {
	if math.IsNaN(idle) || math.IsNaN(max) || math.IsInf(idle, 0) || math.IsInf(max, 0) {
		return fmt.Errorf("power watts are NaN or Inf: %w", ErrInvalidPluginArgs)
	}
	if idle < 0 || max < idle {
		return fmt.Errorf("require 0 <= powerIdleWatts <= powerMaxWatts (got idle=%v max=%v): %w",
			idle, max, ErrInvalidPluginArgs)
	}
	return nil
}
