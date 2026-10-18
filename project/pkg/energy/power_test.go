package energy

import "testing"

func TestNodeAndPodPowerOracle14_1(t *testing.T) {
	const idle, max = 40.0, 100.0
	u := CPUUtilization(500, 1000)
	if u != 0.5 {
		t.Fatalf("u=%v, want 0.5", u)
	}
	if p := NodePowerWatts(idle, max, u); p != 70 {
		t.Fatalf("P(0.5)=%v, want 70", p)
	}
	uMinus := CPUUtilization(500, 1000)
	uPlus := CPUUtilization(500+300, 1000)
	pPod := PodMarginalPowerWatts(idle, max, uMinus, uPlus)
	if pPod != 18 {
		t.Fatalf("P_pod=%v, want 18", pPod)
	}
}

func TestClipAndG02(t *testing.T) {
	if CPUUtilization(1200, 1000) != 1 {
		t.Fatal("G-01: u>1 must clip to 1")
	}
	if CPUUtilization(100, 0) != 1 {
		t.Fatal("G-02: allocatable 0 → u=1")
	}
	uMinus := CPUUtilization(900, 1000)
	uPlus := CPUUtilization(900+300, 1000)
	if uPlus != 1 || uMinus != 0.9 {
		t.Fatalf("14.5: u-=%v u+=%v", uMinus, uPlus)
	}
	if p := PodMarginalPowerWatts(40, 100, uMinus, uPlus); p != 6 {
		t.Fatalf("14.5 P_pod=%v, want 6", p)
	}
	if ResourceScore(1, 1, 0) != 0 {
		t.Fatal("14.5 S_R=0 when U=1")
	}
}
