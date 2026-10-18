package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

// Name is the Scheduler Framework plugin name registered in KubeSchedulerConfiguration.
const Name = v1alpha1.CarbonAwarePluginName

// Compile-time contract: CarbonAware is a scoring plugin (ARCH-04, SCOPE-OUT-16).
// PostBind only records decision metrics (ADR-003); it never affects placement.
var (
	_ fwk.Plugin          = (*CarbonAwarePlugin)(nil)
	_ fwk.PreScorePlugin  = (*CarbonAwarePlugin)(nil)
	_ fwk.ScorePlugin     = (*CarbonAwarePlugin)(nil)
	_ fwk.ScoreExtensions = (*CarbonAwarePlugin)(nil)
	_ fwk.PostBindPlugin  = (*CarbonAwarePlugin)(nil)
)

// CarbonAwarePlugin ranks feasible nodes by expected carbon footprint.
// HTTP lives only in the Cache Sync Worker. PreScore writes CycleState; Score is O(1).
type CarbonAwarePlugin struct {
	args   PluginArgs
	handle fwk.Handle
	cache  *LocalCarbonCache
	client *CarbonServiceClient
	clock  v1alpha1.Clock

	staleWarnMu        sync.Mutex
	lastStaleFetchedAt time.Time
	lastStaleReason    string
}

// New constructs CarbonAwarePlugin from pluginConfig.args. Invalid args fail closed (G-03, G-18):
// the scheduler process must not start with an illegal scoring configuration.
func New(ctx context.Context, obj runtime.Object, handle fwk.Handle) (fwk.Plugin, error) {
	args, err := decodePluginArgs(obj)
	if err != nil {
		return nil, err
	}
	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("CarbonAware plugin args: %w", err)
	}

	clock := v1alpha1.SystemClock{}
	plugin := &CarbonAwarePlugin{
		args:   *args,
		handle: handle,
		cache:  NewLocalCarbonCache(clock, args.ClockSkewSeconds, args.MaxDataAgeSeconds),
		client: NewCarbonServiceClient(args.CarbonServiceURL, time.Duration(args.HTTPTimeoutSeconds)*time.Second),
		clock:  clock,
	}
	registerCacheMetrics()
	go plugin.startCacheSyncWorker(ctx)

	logger := klog.FromContext(ctx)
	logger.Info("CarbonAware plugin initialized",
		"scoringMode", args.ScoringMode,
		"carbonWeight", args.CarbonWeight,
		"resourceWeight", args.ResourceWeight,
		"performanceWeight", args.PerformanceWeight,
		"maxDataAgeSeconds", args.MaxDataAgeSeconds,
		"carbonServiceURL", args.CarbonServiceURL,
	)
	return plugin, nil
}

// NewForTests constructs CarbonAware without starting the Cache Sync Worker.
// handle may be a Framework FakeHandle; nil is allowed because PreScore/Score
// never call Handle (snapshot is injected via SeedSnapshot).
func NewForTests(args PluginArgs, clock v1alpha1.Clock, handle fwk.Handle) (*CarbonAwarePlugin, error) {
	if err := args.Validate(); err != nil {
		return nil, fmt.Errorf("CarbonAware plugin args: %w", err)
	}
	if clock == nil {
		clock = v1alpha1.SystemClock{}
	}
	return &CarbonAwarePlugin{
		args:   args,
		handle: handle,
		cache:  NewLocalCarbonCache(clock, args.ClockSkewSeconds, args.MaxDataAgeSeconds),
		client: NewCarbonServiceClient(args.CarbonServiceURL, time.Duration(args.HTTPTimeoutSeconds)*time.Second),
		clock:  clock,
	}, nil
}

// SeedSnapshot publishes an immutable carbon snapshot the way a successful
// Worker tick would. Production scoring never calls this; unit tests do
// (DOD-TEST-01: no live HTTP on the scoring path).
func (p *CarbonAwarePlugin) SeedSnapshot(snap v1alpha1.CarbonSnapshot) {
	if p == nil || p.cache == nil {
		return
	}
	p.cache.replaceAPI(snap)
}

// Name returns the plugin name used in the framework registry and scheduler config.
func (p *CarbonAwarePlugin) Name() string {
	return Name
}

// Snapshot returns the last Worker-published carbon snapshot, or nil if none has been stored.
func (p *CarbonAwarePlugin) Snapshot() *v1alpha1.CarbonSnapshot {
	if p == nil || p.cache == nil {
		return nil
	}
	return p.cache.CarbonSnapshot()
}

func (p *CarbonAwarePlugin) now() time.Time {
	if p == nil || p.clock == nil {
		return time.Now().UTC()
	}
	return p.clock.Now().UTC()
}

// statusSuccess is the nil *Status, which the framework reads as Success
// (Status.Code on nil). fwk.NewStatus(fwk.Success) would heap-allocate on every
// Score and NormalizeScore call; Gate 9 requires those hot paths to be
// allocation-free.
func statusSuccess() *fwk.Status {
	return nil
}

func statusSLA() *fwk.Status {
	return fwk.NewStatus(fwk.Error, v1alpha1.SLAPreconditionMessage)
}
