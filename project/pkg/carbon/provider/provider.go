// Package provider holds Carbon Data Service adapters (SCOPE-IN-03).
//
// The locked compile-time contract is v1alpha1.CarbonProvider (Current/Forecast
// with explicit `at`). CarbonProvider in this package extends it with the T4.2
// convenience methods GetCurrentIntensity / GetForecast, which use the adapter clock.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/config"
)

// CarbonProvider is v1alpha1.CarbonProvider plus the T4.2 query shape.
type CarbonProvider interface {
	v1alpha1.CarbonProvider
	GetCurrentIntensity(ctx context.Context, zone string) (*v1alpha1.CarbonRecord, error)
	GetForecast(ctx context.Context, zone string, duration time.Duration) ([]v1alpha1.ForecastInterval, error)
}

// Options are constructor inputs shared by live, replay and mock adapters.
type Options struct {
	LiveAPIKey        string
	ReplayDatasetPath string
	HTTPTimeout       time.Duration
	HTTPClient        *http.Client
	BaseURL           string
	MaxAttempts       int
	RetryBackoff      time.Duration
	Clock             v1alpha1.Clock
	TimeMultiplier    float64
	ReplayLoop        string
	ReplayLookup      string
	SourceZones       map[string]string
}

// NewProvider selects an adapter from process configuration (T4.2 factory).
func NewProvider(cfg *config.Config) (CarbonProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	return New(cfg.ProviderType, Options{
		LiveAPIKey:        cfg.LiveAPIKey,
		ReplayDatasetPath: cfg.ReplayDatasetPath,
		HTTPTimeout:       cfg.HTTPTimeout,
		TimeMultiplier:    cfg.ReplayTimeMultiplier,
		ReplayLoop:        cfg.ReplayLoop,
		ReplayLookup:      cfg.ReplayLookup,
	})
}

// ClockOf returns the adapter clock when the provider exposes one.
func ClockOf(p v1alpha1.CarbonProvider) v1alpha1.Clock {
	type hasClock interface {
		Clock() v1alpha1.Clock
	}
	if p == nil {
		return v1alpha1.SystemClock{}
	}
	if c, ok := p.(hasClock); ok && c.Clock() != nil {
		return c.Clock()
	}
	return v1alpha1.SystemClock{}
}

// New returns a CarbonProvider for the requested mode.
func New(mode v1alpha1.ProviderMode, opts Options) (CarbonProvider, error) {
	switch mode {
	case v1alpha1.ProviderModeMock:
		if opts.Clock != nil {
			return NewMockProviderWithClock(opts.Clock), nil
		}
		return NewMockProvider(), nil
	case v1alpha1.ProviderModeReplay:
		return newHistorical(opts)
	case v1alpha1.ProviderModeLive:
		return newElectricityMaps(opts)
	default:
		return nil, fmt.Errorf("unknown provider mode %q", mode)
	}
}
