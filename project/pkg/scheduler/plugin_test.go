package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
)

func TestPluginImplementsOnlyAllowedExtensionPoints(t *testing.T) {
	var p any = &CarbonAwarePlugin{}
	if _, ok := p.(fwk.Plugin); !ok {
		t.Error("CarbonAwarePlugin must implement fwk.Plugin")
	}
	if _, ok := p.(fwk.PreScorePlugin); !ok {
		t.Error("CarbonAwarePlugin must implement fwk.PreScorePlugin")
	}
	if _, ok := p.(fwk.ScorePlugin); !ok {
		t.Error("CarbonAwarePlugin must implement fwk.ScorePlugin")
	}
	if _, ok := p.(fwk.ScoreExtensions); !ok {
		t.Error("CarbonAwarePlugin must implement fwk.ScoreExtensions")
	}
	if _, ok := p.(fwk.FilterPlugin); ok {
		t.Error("SCOPE-OUT-16: Filter is forbidden")
	}
	if _, ok := p.(fwk.PermitPlugin); ok {
		t.Error("SCOPE-OUT-16: Permit is forbidden")
	}
	if _, ok := p.(fwk.QueueSortPlugin); ok {
		t.Error("SCOPE-OUT-16: QueueSort is forbidden")
	}
	if _, ok := p.(fwk.ReservePlugin); ok {
		t.Error("SCOPE-OUT-16: Reserve requires a separate ADR")
	}
}

func TestName(t *testing.T) {
	p := &CarbonAwarePlugin{}
	if p.Name() != "CarbonAware" {
		t.Fatalf("Name()=%q, want CarbonAware", p.Name())
	}
	if Name != v1alpha1.CarbonAwarePluginName {
		t.Fatalf("Name constant=%q, want %q", Name, v1alpha1.CarbonAwarePluginName)
	}
}

func TestNewDecodesAndValidatesPluginArgs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	plugin, err := New(ctx, mustUnknownArgs(t, argsForCDS(srv.URL)), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, ok := plugin.(*CarbonAwarePlugin)
	if !ok {
		t.Fatalf("New returned %T", plugin)
	}
	if got.args.ScoringMode != v1alpha1.ScoringModeExpectedCF {
		t.Fatalf("scoringMode=%q", got.args.ScoringMode)
	}
	if got.args.CarbonServiceURL != srv.URL {
		t.Fatalf("carbonServiceURL=%q", got.args.CarbonServiceURL)
	}
}

func TestNewFailsClosedOnInvalidArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	args := validPluginArgs()
	args.CarbonWeight = 0.5
	_, err := New(ctx, mustUnknownArgs(t, args), nil)
	if err == nil {
		t.Fatal("New must fail closed when weights do not sum to 1 (G-18)")
	}
}

func TestDecodePluginArgsNilObject(t *testing.T) {
	args, err := decodePluginArgs(nil)
	if err != nil {
		t.Fatalf("nil object: %v", err)
	}
	if err := args.Validate(); err == nil {
		t.Fatal("zero-value args must fail Validate (fail-closed, G-03)")
	}
}

func TestNormalizeScoreMapsFixedPointToFrameworkScale(t *testing.T) {
	p := &CarbonAwarePlugin{}
	scores := fwk.NodeScoreList{
		{Name: "n1", Score: v1alpha1.NeutralFixedPointScore()},
		{Name: "n2", Score: 10000},
		{Name: "n3", Score: -50},
	}
	if status := p.NormalizeScore(context.Background(), nil, nil, scores); !status.IsSuccess() {
		t.Fatalf("NormalizeScore status=%v", status)
	}
	if scores[0].Score != v1alpha1.NeutralPluginScore {
		t.Fatalf("neutral: got %d, want %d", scores[0].Score, v1alpha1.NeutralPluginScore)
	}
	if scores[1].Score != v1alpha1.MaxPluginScore {
		t.Fatalf("max: got %d, want %d", scores[1].Score, v1alpha1.MaxPluginScore)
	}
	if scores[2].Score != 0 {
		t.Fatalf("clamp low: got %d, want 0", scores[2].Score)
	}
}

func validPluginArgs() PluginArgs {
	return PluginArgs{
		ScoringMode:            v1alpha1.ScoringModeExpectedCF,
		CarbonWeight:           0.6,
		ResourceWeight:         0.2,
		PerformanceWeight:      0.2,
		Lambda:                 1,
		PowerIdleWatts:         40,
		PowerMaxWatts:          100,
		MaxDataAgeSeconds:      300,
		StaleWarnSeconds:       90,
		ClockSkewSeconds:       60,
		RefreshIntervalSeconds: 30,
		HTTPTimeoutSeconds:     2,
		CarbonServiceURL:       "http://carbon-data-service.carbon-aware.svc:8080",
	}
}

func mustUnknownArgs(t *testing.T, args PluginArgs) *runtime.Unknown {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return &runtime.Unknown{Raw: raw, ContentType: runtime.ContentTypeJSON}
}
