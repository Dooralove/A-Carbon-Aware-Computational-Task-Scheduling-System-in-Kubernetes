package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

const (
	// maxCDSResponseBytes caps a single CDS JSON body (GO-09: bounded I/O).
	maxCDSResponseBytes = 1 << 20

	// DefaultHTTPTimeoutSeconds is the stand canon for Worker HTTP (API §3.3).
	DefaultHTTPTimeoutSeconds int64 = 2
)

var (
	// ErrCDSNotOK is returned when CDS answers with a non-200 status.
	ErrCDSNotOK = errors.New("carbon data service returned a non-success status")

	// ErrCDSResponseTooLarge is returned when a CDS body exceeds maxCDSResponseBytes.
	ErrCDSResponseTooLarge = errors.New("carbon data service response exceeds size limit")
)

// CarbonServiceClient is the only HTTP client in the kube-scheduler process (ARCH-02).
// It must not be used from PreScore/Score.
type CarbonServiceClient struct {
	baseURL string
	timeout time.Duration
	http    *http.Client
}

// NewCarbonServiceClient builds a REST client for Carbon Data Service.
// timeout bounds each request (GO-09). A non-positive timeout uses DefaultHTTPTimeoutSeconds.
func NewCarbonServiceClient(baseURL string, timeout time.Duration) *CarbonServiceClient {
	if timeout <= 0 {
		timeout = time.Duration(DefaultHTTPTimeoutSeconds) * time.Second
	}
	return &CarbonServiceClient{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		timeout: timeout,
		http:    &http.Client{Timeout: timeout},
	}
}

// ListZones performs GET /api/v1/zones.
func (c *CarbonServiceClient) ListZones(ctx context.Context) (v1alpha1.ZoneProfileList, error) {
	var out v1alpha1.ZoneProfileList
	if err := c.getJSON(ctx, v1alpha1.PathZones, &out); err != nil {
		return v1alpha1.ZoneProfileList{}, err
	}
	return out, nil
}

// Current performs GET /api/v1/zones/{zone}/current.
func (c *CarbonServiceClient) Current(ctx context.Context, zone string) (v1alpha1.CarbonRecord, error) {
	var out v1alpha1.CarbonRecord
	if err := c.getJSON(ctx, strings.ReplaceAll(v1alpha1.PathCurrent, "{zone}", url.PathEscape(zone)), &out); err != nil {
		return v1alpha1.CarbonRecord{}, err
	}
	return out, nil
}

// Forecast performs GET /api/v1/zones/{zone}/forecast?duration=.
func (c *CarbonServiceClient) Forecast(ctx context.Context, zone string) (v1alpha1.CarbonForecast, error) {
	path := strings.ReplaceAll(v1alpha1.PathForecast, "{zone}", url.PathEscape(zone))
	q := url.Values{}
	q.Set(v1alpha1.QueryDuration, v1alpha1.FormatDurationSeconds(v1alpha1.DefaultForecastHorizonSeconds))
	var out v1alpha1.CarbonForecast
	if err := c.getJSON(ctx, path+"?"+q.Encode(), &out); err != nil {
		return v1alpha1.CarbonForecast{}, err
	}
	return out, nil
}

func (c *CarbonServiceClient) getJSON(ctx context.Context, path string, dest any) error {
	if c == nil || c.http == nil {
		return fmt.Errorf("carbon service client is nil: %w", ErrCDSNotOK)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	full := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return fmt.Errorf("new request %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCDSResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read GET %s: %w", path, err)
	}
	if len(body) > maxCDSResponseBytes {
		return fmt.Errorf("GET %s: %w", path, ErrCDSResponseTooLarge)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d: %w", path, resp.StatusCode, ErrCDSNotOK)
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("decode GET %s: %w", path, err)
	}
	return nil
}
