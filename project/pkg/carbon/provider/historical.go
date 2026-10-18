package provider

import (
	"carbon-aware-k8s/pkg/carbon/replay"
)

// HistoricalDatasetProvider is the Replay-backed adapter (REPRO-01).
type HistoricalDatasetProvider struct {
	*replay.Engine
}

// NewHistoricalDatasetProvider loads a JSON/CSV Replay dataset.
func NewHistoricalDatasetProvider(datasetPath string) (*HistoricalDatasetProvider, error) {
	return newHistorical(Options{ReplayDatasetPath: datasetPath})
}

func newHistorical(opts Options) (*HistoricalDatasetProvider, error) {
	eng, err := replay.Open(opts.ReplayDatasetPath, replay.Options{
		Clock:      opts.Clock,
		Multiplier: opts.TimeMultiplier,
		Loop:       replay.LoopMode(opts.ReplayLoop),
		Lookup:     replay.LookupMode(opts.ReplayLookup),
	})
	if err != nil {
		return nil, err
	}
	return &HistoricalDatasetProvider{Engine: eng}, nil
}

// DatasetPath returns the configured dataset file path.
func (p *HistoricalDatasetProvider) DatasetPath() string {
	if p.Engine == nil {
		return ""
	}
	return p.Path()
}
