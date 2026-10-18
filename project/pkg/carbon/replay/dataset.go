package replay

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
)

// Dataset is a versioned ReplayDataset (experiments/datasets/schemas/replay_dataset.schema.json).
type Dataset struct {
	Path               string
	SchemaVersion      string
	DatasetID          string
	Source             v1alpha1.CarbonSource
	EmissionFactorType v1alpha1.EmissionFactorType
	FlowTraced         bool
	Granularity        v1alpha1.TemporalGranularity
	Interval           time.Duration
	ModelStart         time.Time
	ModelEnd           time.Time
	ForecastMode       v1alpha1.ForecastMode
	ForecastHorizon    time.Duration
	Zones              []v1alpha1.ZoneID
	SourceZones        map[v1alpha1.ZoneID]string
	series             map[v1alpha1.ZoneID][]sample
	forecasts          map[v1alpha1.ZoneID][]forecastSnap
}

type sample struct {
	Timestamp                  time.Time
	CarbonIntensityGCO2ePerKWh float64
	IsEstimated                bool
}

type forecastPoint struct {
	Timestamp                  time.Time
	CarbonIntensityGCO2ePerKWh float64
	Horizon                    time.Duration
}

type forecastSnap struct {
	IssuedAt time.Time
	Points   []forecastPoint
}

type rawDataset struct {
	SchemaVersion          string                `json:"schemaVersion"`
	DatasetID              string                `json:"datasetId"`
	Source                 string                `json:"source"`
	Unit                   string                `json:"unit"`
	EmissionFactorType     string                `json:"emissionFactorType"`
	FlowTraced             *bool                 `json:"flowTraced"`
	TemporalGranularity    string                `json:"temporalGranularity"`
	IntervalSeconds        int64                 `json:"intervalSeconds"`
	ModelStart             string                `json:"modelStart"`
	ModelEnd               string                `json:"modelEnd"`
	ForecastMode           string                `json:"forecastMode"`
	ForecastHorizonSeconds int64                 `json:"forecastHorizonSeconds"`
	Zones                  []rawZone             `json:"zones"`
	Series                 []rawSample           `json:"series"`
	ForecastSnapshots      []rawForecastSnapshot `json:"forecastSnapshots"`
}

type rawZone struct {
	Zone       string `json:"zone"`
	SourceZone string `json:"sourceZone"`
}

type rawSample struct {
	Zone            string  `json:"zone"`
	Timestamp       string  `json:"timestamp"`
	CarbonIntensity float64 `json:"carbonIntensity"`
	IsEstimated     bool    `json:"isEstimated"`
}

type rawForecastSnapshot struct {
	Zone     string          `json:"zone"`
	IssuedAt string          `json:"issuedAt"`
	Forecast []rawForecastPt `json:"forecast"`
}

type rawForecastPt struct {
	Timestamp       string  `json:"timestamp"`
	CarbonIntensity float64 `json:"carbonIntensity"`
	HorizonSeconds  int64   `json:"horizonSeconds"`
}

// LoadFile reads a Replay JSON or CSV dataset from path.
func LoadFile(path string) (*Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read replay dataset %q: %w", path, err)
	}
	ds, err := parseDataset(data, strings.ToLower(filepath.Ext(path)))
	if err != nil {
		return nil, fmt.Errorf("parse replay dataset %q: %w", path, err)
	}
	ds.Path = path
	return ds, nil
}

func parseDataset(data []byte, ext string) (*Dataset, error) {
	switch ext {
	case ".csv":
		return parseCSV(data)
	default:
		return parseJSON(data)
	}
}

func parseJSON(data []byte) (*Dataset, error) {
	var raw rawDataset
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: json: %v", ErrInvalidDataset, err)
	}
	if raw.SchemaVersion != "" && raw.SchemaVersion != "1.0.0" {
		return nil, fmt.Errorf("%w: schemaVersion %q", ErrInvalidDataset, raw.SchemaVersion)
	}
	if raw.Unit != "" && raw.Unit != v1alpha1.IntensityUnitGCO2ePerKWh {
		return nil, fmt.Errorf("%w: unit %q", ErrInvalidDataset, raw.Unit)
	}
	start, err := parseUTC(raw.ModelStart)
	if err != nil {
		return nil, fmt.Errorf("%w: modelStart: %v", ErrInvalidDataset, err)
	}
	end, err := parseUTC(raw.ModelEnd)
	if err != nil {
		return nil, fmt.Errorf("%w: modelEnd: %v", ErrInvalidDataset, err)
	}
	if !start.Before(end) {
		return nil, fmt.Errorf("%w: modelStart must be before modelEnd", ErrInvalidDataset)
	}
	interval := time.Duration(raw.IntervalSeconds) * time.Second
	if interval <= 0 {
		g := v1alpha1.TemporalGranularity(raw.TemporalGranularity)
		if sec := g.IntervalSeconds(); sec > 0 {
			interval = time.Duration(sec) * time.Second
			raw.IntervalSeconds = sec
		}
	}
	if interval <= 0 {
		return nil, fmt.Errorf("%w: intervalSeconds is required", ErrInvalidDataset)
	}
	gran := v1alpha1.TemporalGranularity(raw.TemporalGranularity)
	if gran == "" {
		gran = granularityOf(raw.IntervalSeconds)
	}
	flow := true
	if raw.FlowTraced != nil {
		flow = *raw.FlowTraced
	}
	ds := &Dataset{
		SchemaVersion:      raw.SchemaVersion,
		DatasetID:          raw.DatasetID,
		Source:             mapSource(raw.Source),
		EmissionFactorType: v1alpha1.EmissionFactorType(raw.EmissionFactorType),
		FlowTraced:         flow,
		Granularity:        gran,
		Interval:           interval,
		ModelStart:         start,
		ModelEnd:           end,
		ForecastMode:       v1alpha1.ForecastMode(raw.ForecastMode),
		ForecastHorizon:    time.Duration(raw.ForecastHorizonSeconds) * time.Second,
		SourceZones:        make(map[v1alpha1.ZoneID]string),
		series:             make(map[v1alpha1.ZoneID][]sample),
		forecasts:          make(map[v1alpha1.ZoneID][]forecastSnap),
	}
	if ds.EmissionFactorType == "" {
		ds.EmissionFactorType = v1alpha1.EmissionLifecycle
	}
	if ds.ForecastMode == "" {
		ds.ForecastMode = v1alpha1.ForecastModeNone
	}
	for _, z := range raw.Zones {
		id := v1alpha1.ZoneID(z.Zone)
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDataset, err)
		}
		ds.Zones = append(ds.Zones, id)
		if z.SourceZone != "" {
			ds.SourceZones[id] = z.SourceZone
		}
	}
	for _, s := range raw.Series {
		if err := v1alpha1.ValidateCarbonIntensity(s.CarbonIntensity); err != nil {
			return nil, fmt.Errorf("%w: series: %v", ErrInvalidDataset, err)
		}
		ts, err := parseUTC(s.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("%w: series timestamp: %v", ErrInvalidDataset, err)
		}
		id := v1alpha1.ZoneID(s.Zone)
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDataset, err)
		}
		ds.series[id] = append(ds.series[id], sample{
			Timestamp:                  ts,
			CarbonIntensityGCO2ePerKWh: s.CarbonIntensity,
			IsEstimated:                s.IsEstimated,
		})
	}
	for _, snap := range raw.ForecastSnapshots {
		id := v1alpha1.ZoneID(snap.Zone)
		issued, err := parseUTC(snap.IssuedAt)
		if err != nil {
			return nil, fmt.Errorf("%w: issuedAt: %v", ErrInvalidDataset, err)
		}
		fs := forecastSnap{IssuedAt: issued}
		for _, p := range snap.Forecast {
			if err := v1alpha1.ValidateCarbonIntensity(p.CarbonIntensity); err != nil {
				return nil, fmt.Errorf("%w: forecast: %v", ErrInvalidDataset, err)
			}
			ts, err := parseUTC(p.Timestamp)
			if err != nil {
				return nil, fmt.Errorf("%w: forecast timestamp: %v", ErrInvalidDataset, err)
			}
			fs.Points = append(fs.Points, forecastPoint{
				Timestamp:                  ts,
				CarbonIntensityGCO2ePerKWh: p.CarbonIntensity,
				Horizon:                    time.Duration(p.HorizonSeconds) * time.Second,
			})
		}
		ds.forecasts[id] = append(ds.forecasts[id], fs)
	}
	if err := ds.index(); err != nil {
		return nil, err
	}
	if len(ds.Zones) == 0 {
		for z := range ds.series {
			ds.Zones = append(ds.Zones, z)
		}
		sort.Slice(ds.Zones, func(i, j int) bool { return ds.Zones[i] < ds.Zones[j] })
	}
	return ds, nil
}

func parseCSV(data []byte) (*Dataset, error) {
	r := csv.NewReader(strings.NewReader(string(data)))
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: csv: %v", ErrInvalidDataset, err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("%w: csv has no data rows", ErrInvalidDataset)
	}
	header := make(map[string]int, len(rows[0]))
	for i, name := range rows[0] {
		header[strings.ToLower(strings.TrimSpace(name))] = i
	}
	zi, ok1 := header["zone"]
	ti, ok2 := header["timestamp"]
	ci, ok3 := header["carbonintensity"]
	if !ok1 || !ok2 || !ok3 {
		return nil, fmt.Errorf("%w: csv requires zone,timestamp,carbonIntensity columns", ErrInvalidDataset)
	}
	raw := rawDataset{
		SchemaVersion:       "1.0.0",
		DatasetID:           "csv_import_v1",
		Source:              "synthetic",
		Unit:                v1alpha1.IntensityUnitGCO2ePerKWh,
		EmissionFactorType:  string(v1alpha1.EmissionLifecycle),
		TemporalGranularity: string(v1alpha1.Granularity5Minutes),
		IntervalSeconds:     300,
		ForecastMode:        string(v1alpha1.ForecastModeNone),
	}
	seenZone := map[string]struct{}{}
	var minTS, maxTS time.Time
	for n, row := range rows[1:] {
		if len(row) <= ci || len(row) <= zi || len(row) <= ti {
			return nil, fmt.Errorf("%w: csv row %d is short", ErrInvalidDataset, n+2)
		}
		val, err := strconv.ParseFloat(strings.TrimSpace(row[ci]), 64)
		if err != nil {
			return nil, fmt.Errorf("%w: csv row %d intensity: %v", ErrInvalidDataset, n+2, err)
		}
		raw.Series = append(raw.Series, rawSample{
			Zone:            strings.TrimSpace(row[zi]),
			Timestamp:       strings.TrimSpace(row[ti]),
			CarbonIntensity: val,
		})
		seenZone[strings.TrimSpace(row[zi])] = struct{}{}
		ts, err := parseUTC(strings.TrimSpace(row[ti]))
		if err != nil {
			return nil, fmt.Errorf("%w: csv row %d timestamp: %v", ErrInvalidDataset, n+2, err)
		}
		if minTS.IsZero() || ts.Before(minTS) {
			minTS = ts
		}
		if maxTS.IsZero() || ts.After(maxTS) {
			maxTS = ts
		}
	}
	for z := range seenZone {
		raw.Zones = append(raw.Zones, rawZone{Zone: z})
	}
	sort.Slice(raw.Zones, func(i, j int) bool { return raw.Zones[i].Zone < raw.Zones[j].Zone })
	raw.ModelStart = minTS.UTC().Format(time.RFC3339)
	raw.ModelEnd = maxTS.Add(time.Duration(raw.IntervalSeconds) * time.Second).UTC().Format(time.RFC3339)
	body, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: csv normalize: %v", ErrInvalidDataset, err)
	}
	return parseJSON(body)
}

func (d *Dataset) index() error {
	for z, samples := range d.series {
		sort.Slice(samples, func(i, j int) bool { return samples[i].Timestamp.Before(samples[j].Timestamp) })
		for i := 1; i < len(samples); i++ {
			if !samples[i].Timestamp.After(samples[i-1].Timestamp) {
				return fmt.Errorf("%w: duplicate or unsorted timestamp for zone %s", ErrInvalidDataset, z)
			}
		}
		d.series[z] = samples
	}
	for z, snaps := range d.forecasts {
		sort.Slice(snaps, func(i, j int) bool { return snaps[i].IssuedAt.Before(snaps[j].IssuedAt) })
		d.forecasts[z] = snaps
	}
	return nil
}

func parseUTC(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func mapSource(s string) v1alpha1.CarbonSource {
	switch v1alpha1.CarbonSource(s) {
	case v1alpha1.SourceElectricityMaps, v1alpha1.SourceNESO, v1alpha1.SourceSynthetic, v1alpha1.SourceMock, v1alpha1.SourceHistoricalDataset:
		return v1alpha1.CarbonSource(s)
	default:
		return v1alpha1.SourceHistoricalDataset
	}
}

func granularityOf(seconds int64) v1alpha1.TemporalGranularity {
	switch seconds {
	case 300:
		return v1alpha1.Granularity5Minutes
	case 900:
		return v1alpha1.Granularity15Minutes
	case 1800:
		return v1alpha1.Granularity30Minutes
	case 3600:
		return v1alpha1.Granularity60Minutes
	default:
		return v1alpha1.Granularity5Minutes
	}
}
