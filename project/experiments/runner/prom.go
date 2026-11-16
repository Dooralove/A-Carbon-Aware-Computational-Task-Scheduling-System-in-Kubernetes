package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// promClient is a minimal Prometheus HTTP API v1 client (instant and range
// queries). The runner talks to the stand on localhost, so no proxy is used.
type promClient struct {
	base string
	http *http.Client
}

func newPromClient(base string) *promClient {
	return &promClient{
		base: base,
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}},
	}
}

type promSample struct {
	Labels map[string]string
	T      time.Time
	V      float64
}

type promSeries struct {
	Labels map[string]string
	Points []promPoint
}

type promPoint struct {
	T time.Time
	V float64
}

type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func (p *promClient) get(ctx context.Context, path string, q url.Values) (*promResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out promResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("prometheus %s: %s: %w", path, resp.Status, err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("prometheus %s: %s", path, out.Error)
	}
	return &out, nil
}

// Query runs an instant query at t.
func (p *promClient) Query(ctx context.Context, expr string, t time.Time) ([]promSample, error) {
	q := url.Values{"query": {expr}, "time": {formatPromTime(t)}}
	resp, err := p.get(ctx, "/api/v1/query", q)
	if err != nil {
		return nil, err
	}
	var out []promSample
	for _, r := range resp.Data.Result {
		if len(r.Value) != 2 {
			continue
		}
		ts, v, err := parsePromPair(r.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, promSample{Labels: r.Metric, T: ts, V: v})
	}
	return out, nil
}

// QueryScalar returns the single value of expr at t, or NaN if empty.
func (p *promClient) QueryScalar(ctx context.Context, expr string, t time.Time) (float64, error) {
	s, err := p.Query(ctx, expr, t)
	if err != nil {
		return math.NaN(), err
	}
	if len(s) == 0 {
		return math.NaN(), nil
	}
	return s[0].V, nil
}

// QueryRange runs a range query with the given step.
func (p *promClient) QueryRange(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]promSeries, error) {
	q := url.Values{
		"query": {expr},
		"start": {formatPromTime(start)},
		"end":   {formatPromTime(end)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}
	resp, err := p.get(ctx, "/api/v1/query_range", q)
	if err != nil {
		return nil, err
	}
	var out []promSeries
	for _, r := range resp.Data.Result {
		s := promSeries{Labels: r.Metric}
		for _, pair := range r.Values {
			ts, v, err := parsePromPair(pair)
			if err != nil {
				return nil, err
			}
			s.Points = append(s.Points, promPoint{T: ts, V: v})
		}
		out = append(out, s)
	}
	return out, nil
}

func parsePromPair(pair []any) (time.Time, float64, error) {
	if len(pair) != 2 {
		return time.Time{}, 0, fmt.Errorf("bad sample %v", pair)
	}
	tf, ok := pair[0].(float64)
	if !ok {
		return time.Time{}, 0, fmt.Errorf("bad timestamp %v", pair[0])
	}
	vs, ok := pair[1].(string)
	if !ok {
		return time.Time{}, 0, fmt.Errorf("bad value %v", pair[1])
	}
	v, err := strconv.ParseFloat(vs, 64)
	if err != nil {
		return time.Time{}, 0, err
	}
	sec, frac := math.Modf(tf)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC(), v, nil
}

func formatPromTime(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 3, 64)
}

// stepSeries is a piecewise-constant signal reconstructed from Prometheus
// samples: the value at t is the last sample at or before t. CDS publishes CI
// as a step function of model time, so this matches the replay (step lookup).
type stepSeries []promPoint

func newStepSeries(points []promPoint) stepSeries {
	s := append(stepSeries(nil), points...)
	sort.Slice(s, func(i, j int) bool { return s[i].T.Before(s[j].T) })
	return s
}

// At returns the value at t and false if t precedes the first sample.
func (s stepSeries) At(t time.Time) (float64, bool) {
	i := sort.Search(len(s), func(i int) bool { return s[i].T.After(t) }) - 1
	if i < 0 {
		return math.NaN(), false
	}
	return s[i].V, true
}

// Integrate returns ∫_a^b s(t) dt in value·seconds and whether every instant
// of [a, b] was covered by a sample.
func (s stepSeries) Integrate(a, b time.Time) (float64, bool) {
	if !b.After(a) {
		return 0, true
	}
	covered := len(s) > 0 && !s[0].T.After(a)
	var sum float64
	t := a
	i := sort.Search(len(s), func(i int) bool { return s[i].T.After(a) }) - 1
	for t.Before(b) {
		next := b
		if i+1 < len(s) && s[i+1].T.Before(b) {
			next = s[i+1].T
		}
		if i >= 0 {
			sum += s[i].V * next.Sub(t).Seconds()
		}
		t = next
		i++
	}
	return sum, covered
}
