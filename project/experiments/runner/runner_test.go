package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

func TestStepSeriesIntegrate(t *testing.T) {
	s := newStepSeries([]promPoint{{at(10), 400}, {at(0), 100}, {at(20), 50}})
	cases := []struct {
		a, b    float64
		want    float64
		covered bool
	}{
		{0, 10, 1000, true},
		{5, 15, 100*5 + 400*5, true},
		{0, 30, 1000 + 4000 + 500, true},
		{25, 30, 250, true},
		{-5, 5, 500, false}, // first 5 s precede the first sample
		{7, 7, 0, true},
	}
	for _, c := range cases {
		got, covered := s.Integrate(at(c.a), at(c.b))
		if math.Abs(got-c.want) > 1e-9 || covered != c.covered {
			t.Errorf("Integrate(%v,%v) = %v,%v; want %v,%v", c.a, c.b, got, covered, c.want, c.covered)
		}
	}
	if v, ok := s.At(at(10)); !ok || v != 400 {
		t.Errorf("At(10) = %v,%v", v, ok)
	}
	if _, ok := s.At(at(-1)); ok {
		t.Error("At before first sample reported ok")
	}
}

func TestHistogramQuantile(t *testing.T) {
	// 10 events: 4 in (0,0.01], 4 in (0.01,0.1], 2 in (0.1,+Inf].
	h := histogram{{0.01, 4}, {0.1, 8}, {math.Inf(1), 10}}
	if got := h.quantile(0.2); math.Abs(got-0.005) > 1e-12 {
		t.Errorf("p20 = %v, want 0.005", got)
	}
	if got := h.quantile(0.6); math.Abs(got-0.055) > 1e-12 {
		t.Errorf("p60 = %v, want 0.055", got)
	}
	if got := h.quantile(0.99); got != 0.1 {
		t.Errorf("p99 in +Inf bucket = %v, want highest finite bound 0.1", got)
	}
	if got := (histogram{{0.1, 0}, {math.Inf(1), 0}}).quantile(0.5); !math.IsNaN(got) {
		t.Errorf("empty histogram quantile = %v, want NaN", got)
	}
}

func TestQuantilesInterpolate(t *testing.T) {
	q := quantiles([]float64{4, 1, 3, 2}, 0, 0.5, 1)
	if q[0] != 1 || q[1] != 2.5 || q[2] != 4 {
		t.Fatalf("quantiles = %v", q)
	}
}

func TestBuildPlanPairsSeedsAndIDs(t *testing.T) {
	ws, _ := parseWeights("0,0.4")
	plan, err := buildPlan("weights", []string{"proposed"}, []string{"volatile"}, ws, nil, 2, 1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 4 {
		t.Fatalf("plan has %d runs, want 4", len(plan))
	}
	if plan[0].ID != "weights-volatile-proposed-wc0-r1" || plan[3].ID != "weights-volatile-proposed-wc0.4-r2" {
		t.Fatalf("ids %s .. %s", plan[0].ID, plan[3].ID)
	}
	if plan[0].Seed != plan[1].Seed || plan[1].Seed == plan[2].Seed {
		t.Fatal("seed must depend on the repetition only")
	}
	w := plan[1].EffectiveWeights()
	if math.Abs(w[0]-0.4) > 1e-12 || math.Abs(w[1]-0.3) > 1e-12 || math.Abs(w[2]-0.3) > 1e-12 {
		t.Fatalf("effective weights %v, want 0.4/0.3/0.3", *w)
	}
	if _, err := buildPlan("x", []string{"default"}, []string{"stable"}, ws, nil, 1, 1, 0); err == nil {
		t.Fatal("carbon weight accepted for the default scheduler")
	}
	if _, err := buildPlan("x", []string{"nope"}, []string{"stable"}, nil, nil, 1, 1, 0); err == nil {
		t.Fatal("unknown strategy accepted")
	}
	if (RunPlan{Strategy: strategies["default"]}).EffectiveWeights() != nil {
		t.Fatal("default scheduler has no plugin weights")
	}
}

func TestOpenCSVAppendsAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs_x.csv")
	w, err := openCSV(path, runHeader)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]string, len(runHeader))
	row[0] = "matrix-stable-default-r1"
	if err := w.WriteRows([][]string{row}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if w, err = openCSV(path, runHeader); err != nil {
		t.Fatal(err)
	}
	w.Close()
	ids, err := existingRunIDs(path)
	if err != nil || len(ids) != 1 || !ids["matrix-stable-default-r1"] {
		t.Fatalf("ids %v err %v", ids, err)
	}
	if _, err := openCSV(path, taskHeader); err == nil {
		t.Fatal("header mismatch not detected")
	}
	b, _ := os.ReadFile(path)
	if n := countLines(b); n != 2 {
		t.Fatalf("file has %d lines, want header + 1 row", n)
	}
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestTaskHeaderHasRequiredColumns(t *testing.T) {
	for _, c := range []string{"pod_id", "node", "zone", "duration", "energy_kwh", "carbon_gco2", "scheduled_at", "completed_at", "sla_violated", "w_c", "deadline_factor"} {
		if indexOf(taskHeader, c) < 0 {
			t.Errorf("column %q missing", c)
		}
	}
}
