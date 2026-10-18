package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	defaultEMBaseURL    = "https://api.electricitymap.org/v3"
	defaultHTTPTimeout  = 5 * time.Second
	defaultMaxAttempts  = 3
	defaultRetryBackoff = 50 * time.Millisecond
	authTokenHeader     = "auth-token"
	latestPath          = "/carbon-intensity/latest"
	forecastPath        = "/carbon-intensity/forecast"
)

// ElectricityMapsProvider is the live HTTPS adapter (SCOPE-EDGE-06, demo only).
type ElectricityMapsProvider struct {
	apiKey     string
	baseURL    string
	client     *http.Client
	clock      v1alpha1.Clock
	attempts   int
	backoff    time.Duration
	sourceZone map[string]string
}

type emLatest struct {
	Zone                string   `json:"zone"`
	CarbonIntensity     *float64 `json:"carbonIntensity"`
	Datetime            string   `json:"datetime"`
	UpdatedAt           string   `json:"updatedAt"`
	EmissionFactorType  string   `json:"emissionFactorType"`
	FlowTraced          *bool    `json:"flowTraced"`
	IsEstimated         bool     `json:"isEstimated"`
	EstimationMethod    *string  `json:"estimationMethod"`
	TemporalGranularity string   `json:"temporalGranularity"`
}

type emForecast struct {
	Zone      string         `json:"zone"`
	UpdatedAt string         `json:"updatedAt"`
	Forecast  []emForecastPt `json:"forecast"`
}

type emForecastPt struct {
	CarbonIntensity *float64 `json:"carbonIntensity"`
	Datetime        string   `json:"datetime"`
}

// NewElectricityMapsProvider constructs a live adapter. apiKey is never logged.
func NewElectricityMapsProvider(apiKey string) (*ElectricityMapsProvider, error) {
	return newElectricityMaps(Options{LiveAPIKey: apiKey})
}

func newElectricityMaps(opts Options) (*ElectricityMapsProvider, error) {
	if strings.TrimSpace(opts.LiveAPIKey) == "" {
		return nil, ErrMissingAPIKey
	}
	timeout := opts.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	backoff := opts.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = defaultEMBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else if client.Timeout == 0 {
		cp := *client
		cp.Timeout = timeout
		client = &cp
	}
	clock := opts.Clock
	if clock == nil {
		clock = v1alpha1.SystemClock{}
	}
	return &ElectricityMapsProvider{
		apiKey:     opts.LiveAPIKey,
		baseURL:    base,
		client:     client,
		clock:      clock,
		attempts:   attempts,
		backoff:    backoff,
		sourceZone: opts.SourceZones,
	}, nil
}

// HasAPIKey reports whether a live key was supplied without exposing it.
func (p *ElectricityMapsProvider) HasAPIKey() bool {
	return p.apiKey != ""
}

// Name implements v1alpha1.CarbonProvider.
func (p *ElectricityMapsProvider) Name() string { return "electricity-maps" }

// Mode implements v1alpha1.CarbonProvider.
func (p *ElectricityMapsProvider) Mode() v1alpha1.ProviderMode { return v1alpha1.ProviderModeLive }

// Clock returns the adapter clock used as `at` for Current/Forecast.
func (p *ElectricityMapsProvider) Clock() v1alpha1.Clock { return p.clock }

// ListZones implements v1alpha1.CarbonProvider.
func (p *ElectricityMapsProvider) ListZones(ctx context.Context) ([]v1alpha1.ZoneProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(p.sourceZone) == 0 {
		return []v1alpha1.ZoneProfile{}, nil
	}
	out := make([]v1alpha1.ZoneProfile, 0, len(p.sourceZone))
	for logical, src := range p.sourceZone {
		out = append(out, v1alpha1.ZoneProfile{
			APIVersion:          v1alpha1.GroupVersion,
			Kind:                v1alpha1.KindZoneProfile,
			Zone:                v1alpha1.ZoneID(logical),
			SourceZone:          src,
			Quality:             v1alpha1.QualityFresh,
			Source:              v1alpha1.SourceElectricityMaps,
			TemporalGranularity: v1alpha1.Granularity60Minutes,
			IntervalSeconds:     3600,
			HasCurrent:          true,
			HasForecast:         true,
		})
	}
	return out, nil
}

// Current implements v1alpha1.CarbonProvider. at is used as fetchedAt; the observation
// timestamp comes from the API datetime. Adapters must not call time.Now() internally.
func (p *ElectricityMapsProvider) Current(ctx context.Context, zone v1alpha1.ZoneID, at time.Time) (v1alpha1.CarbonRecord, error) {
	if err := ctx.Err(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	if err := zone.Validate(); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	if at.IsZero() {
		at = p.clock.Now()
	}
	q := url.Values{}
	q.Set("zone", p.upstreamZone(zone))
	q.Set("emissionFactorType", "lifecycle")
	q.Set("flowTraced", "true")
	body, err := p.get(ctx, latestPath, q)
	if err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	var raw emLatest
	if err := json.Unmarshal(body, &raw); err != nil {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("%w: latest json: %v", ErrInvalidResponse, err)
	}
	return mapLatest(zone, raw, at)
}

// Forecast implements v1alpha1.CarbonProvider.
func (p *ElectricityMapsProvider) Forecast(ctx context.Context, zone v1alpha1.ZoneID, at time.Time, horizon time.Duration) ([]v1alpha1.ForecastInterval, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := zone.Validate(); err != nil {
		return nil, err
	}
	if at.IsZero() {
		at = p.clock.Now()
	}
	if horizon <= 0 {
		horizon = time.Duration(v1alpha1.DefaultForecastHorizonSeconds) * time.Second
	}
	q := url.Values{}
	q.Set("zone", p.upstreamZone(zone))
	body, err := p.get(ctx, forecastPath, q)
	if err != nil {
		return nil, err
	}
	var raw emForecast
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: forecast json: %v", ErrInvalidResponse, err)
	}
	until := at.Add(horizon)
	out := make([]v1alpha1.ForecastInterval, 0, len(raw.Forecast))
	for _, pt := range raw.Forecast {
		iv, err := mapForecastPoint(pt)
		if err != nil {
			return nil, err
		}
		if iv.Timestamp.Before(at) || !iv.Timestamp.Before(until) {
			continue
		}
		out = append(out, iv)
	}
	return out, nil
}

// GetCurrentIntensity is the T4.2 convenience form.
func (p *ElectricityMapsProvider) GetCurrentIntensity(ctx context.Context, zone string) (*v1alpha1.CarbonRecord, error) {
	rec, err := p.Current(ctx, v1alpha1.ZoneID(zone), p.clock.Now())
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetForecast is the T4.2 convenience form.
func (p *ElectricityMapsProvider) GetForecast(ctx context.Context, zone string, duration time.Duration) ([]v1alpha1.ForecastInterval, error) {
	return p.Forecast(ctx, v1alpha1.ZoneID(zone), p.clock.Now(), duration)
}

func (p *ElectricityMapsProvider) upstreamZone(zone v1alpha1.ZoneID) string {
	if src, ok := p.sourceZone[string(zone)]; ok && src != "" {
		return src
	}
	return string(zone)
}

func (p *ElectricityMapsProvider) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := p.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var last error
	for attempt := 1; attempt <= p.attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set(authTokenHeader, p.apiKey)
		req.Header.Set("Accept", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			last = wrapNet(err)
			if attempt < p.attempts && isTimeout(last) {
				if err := sleepBackoff(ctx, p.backoff, attempt); err != nil {
					return nil, err
				}
				continue
			}
			return nil, last
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%w: read body: %v", ErrInvalidResponse, readErr)
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("%w: upstream %s", v1alpha1.ErrUnknownZone, resp.Status)
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("%w: upstream rejected credentials", ErrInvalidResponse)
		case retryableStatus(resp.StatusCode) && attempt < p.attempts:
			last = fmt.Errorf("%w: upstream %s", ErrInvalidResponse, resp.Status)
			if err := sleepBackoff(ctx, p.backoff, attempt); err != nil {
				return nil, err
			}
			continue
		default:
			return nil, fmt.Errorf("%w: upstream %s", ErrInvalidResponse, resp.Status)
		}
	}
	if last == nil {
		last = ErrInvalidResponse
	}
	return nil, last
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func wrapNet(err error) error {
	if isTimeout(err) {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return err
}

func sleepBackoff(ctx context.Context, base time.Duration, attempt int) error {
	d := base * time.Duration(attempt)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func mapLatest(zone v1alpha1.ZoneID, raw emLatest, fetchedAt time.Time) (v1alpha1.CarbonRecord, error) {
	if raw.CarbonIntensity == nil {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("%w: missing carbonIntensity", ErrInvalidResponse)
	}
	if err := v1alpha1.ValidateCarbonIntensity(*raw.CarbonIntensity); err != nil {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	ts, err := parseEMTime(raw.Datetime)
	if err != nil {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("%w: datetime: %v", ErrInvalidResponse, err)
	}
	ef := v1alpha1.EmissionFactorType(raw.EmissionFactorType)
	if ef == "" {
		ef = v1alpha1.EmissionLifecycle
	}
	if ef == v1alpha1.EmissionDirect {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("%w", v1alpha1.ErrNotLifecycle)
	}
	gran, interval := mapEMGranularity(raw.TemporalGranularity)
	quality := v1alpha1.QualityFresh
	if raw.IsEstimated {
		quality = v1alpha1.QualityEstimated
	}
	rec := v1alpha1.CarbonRecord{
		APIVersion:                 v1alpha1.GroupVersion,
		Kind:                       v1alpha1.KindCarbonRecord,
		Zone:                       zone,
		Timestamp:                  v1alpha1.RFC3339Time{Time: ts},
		CarbonIntensityGCO2ePerKWh: *raw.CarbonIntensity,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
		Source:                     v1alpha1.SourceElectricityMaps,
		SourceZone:                 raw.Zone,
		EmissionFactorType:         ef,
		TemporalGranularity:        gran,
		IntervalSeconds:            interval,
		Quality:                    quality,
		FreshnessSeconds:           0,
		IsEstimated:                raw.IsEstimated,
		EstimationMethod:           raw.EstimationMethod,
		FlowTraced:                 raw.FlowTraced,
		FetchedAt:                  v1alpha1.RFC3339Time{Time: fetchedAt.UTC()},
		ExpiresAt:                  v1alpha1.RFC3339Time{Time: ts.Add(time.Duration(interval) * time.Second)},
	}
	if raw.UpdatedAt != "" {
		if u, err := parseEMTime(raw.UpdatedAt); err == nil {
			rec.UpdatedAt = v1alpha1.RFC3339Time{Time: u}
		}
	}
	if err := rec.Validate(); err != nil {
		return v1alpha1.CarbonRecord{}, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return rec, nil
}

func mapForecastPoint(pt emForecastPt) (v1alpha1.ForecastInterval, error) {
	if pt.CarbonIntensity == nil {
		return v1alpha1.ForecastInterval{}, fmt.Errorf("%w: forecast missing carbonIntensity", ErrInvalidResponse)
	}
	if err := v1alpha1.ValidateCarbonIntensity(*pt.CarbonIntensity); err != nil {
		return v1alpha1.ForecastInterval{}, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	ts, err := parseEMTime(pt.Datetime)
	if err != nil {
		return v1alpha1.ForecastInterval{}, fmt.Errorf("%w: forecast datetime: %v", ErrInvalidResponse, err)
	}
	return v1alpha1.ForecastInterval{
		Timestamp:                  v1alpha1.RFC3339Time{Time: ts},
		CarbonIntensityGCO2ePerKWh: *pt.CarbonIntensity,
		Unit:                       v1alpha1.IntensityUnitGCO2ePerKWh,
	}, nil
}

func mapEMGranularity(raw string) (v1alpha1.TemporalGranularity, int64) {
	switch raw {
	case "5_minutes":
		return v1alpha1.Granularity5Minutes, 300
	case "15_minutes":
		return v1alpha1.Granularity15Minutes, 900
	case "hourly", "", "60_minutes":
		return v1alpha1.Granularity60Minutes, 3600
	default:
		return v1alpha1.Granularity60Minutes, 3600
	}
}

func parseEMTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty datetime")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

var _ v1alpha1.CarbonProvider = (*ElectricityMapsProvider)(nil)
