package main

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestDurationModeHitsRequestedTime(t *testing.T) {
	cfg, err := parseFlags([]string{"--duration=1500ms", "--cpu=0.5", "--period=50ms"})
	if err != nil {
		t.Fatal(err)
	}
	res := run(context.Background(), cfg)
	if res.Status != statusCompleted || math.Abs(res.ActualDurationS-1.5) > 0.05 {
		t.Fatalf("status %s, actual %.3fs, want 1.5s", res.Status, res.ActualDurationS)
	}
	// Half a core for 1.5 s ≈ 0.75 CPU-s; the test host may be busy, so only
	// check that the load is neither idle nor a full core.
	if res.CPUUtilization < 0.5 || res.CPUUtilization > 1.5 {
		t.Fatalf("cpu utilization %.3f of the requested 0.5 core", res.CPUUtilization)
	}
}

func TestWorkModeCountsUnits(t *testing.T) {
	cfg, err := parseFlags([]string{"--work-units=5000", "--cpu=2", "--kernel=matmul"})
	if err != nil {
		t.Fatal(err)
	}
	res := run(context.Background(), cfg)
	if res.Mode != "work" || res.WorkUnits != 5000 || res.Workers != 2 {
		t.Fatalf("result %+v", res)
	}
}

func TestTerminationIsReported(t *testing.T) {
	cfg, _ := parseFlags([]string{"--duration=10s", "--cpu=0.1"})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res := run(ctx, cfg)
	if res.Status != statusTerminated || res.ActualDurationS > 1 {
		t.Fatalf("status %s after %.3fs", res.Status, res.ActualDurationS)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{{"--cpu=0"}, {"--kernel=x"}, {"--duration=0s"}, {"--period=1ms"}, {"--memory-mib=-1"}} {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
