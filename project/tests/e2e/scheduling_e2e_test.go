//go:build e2e

package e2e_test

import (
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Test 1: 10 opt-in pods on carbon-aware-scheduler land only on nodes of the
// zone with the lowest carbon intensity served by CDS.
func TestE2EBasicPlacementOnGreenestZone(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "placement")
	green, ci := greenestZone(t)
	t.Logf("CDS intensities %v → expected zone %s", ci, green)

	before := scrape(t)
	names := createPods(t, ns, "carbon", 10, carbonOpts())
	pods := waitRunning(t, ns, names, 2*time.Minute)
	after := scrape(t)

	byZone := placement(pods)
	t.Logf("placement by zone: %v", byZone)
	for _, n := range names {
		node := pods[n].Spec.NodeName
		if zoneOf[node] != green {
			t.Errorf("pod %s on %s (zone %q), want zone %s", n, node, zoneOf[node], green)
		}
	}
	if d := after.value(mAttempts, nil) - before.value(mAttempts, nil); d < 10 {
		t.Errorf("scoring_attempts_total grew by %v, want ≥10", d)
	}
	if d := after.value(mFallback, nil) - before.value(mFallback, nil); d != 0 {
		t.Errorf("fallback counter grew by %v on fresh data, want 0", d)
	}
}

// Test 2: pods for default-scheduler are not touched by the carbon logic,
// even when they carry the opt-in label; opt-out pods on the carbon profile
// are scored neutrally and counted as opt-out, not as fallback.
func TestE2ESchedulerIsolation(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "isolation")

	before := scrape(t)
	opts := carbonOpts()
	opts.scheduler = defaultScheduler
	names := createPods(t, ns, "default", 10, opts)
	pods := waitRunning(t, ns, names, 2*time.Minute)
	after := scrape(t)

	for _, n := range names {
		ev, err := scheduledEvent(context.Background(), ns, n)
		if err != nil {
			t.Fatal(err)
		}
		if r := eventReporter(ev); r != defaultScheduler {
			t.Errorf("pod %s scheduled by %q, want %s", n, r, defaultScheduler)
		}
	}
	for _, m := range [][]string{mAttempts, mOptOut, mFallback, mStaleInWindow} {
		if d := after.value(m, nil) - before.value(m, nil); d != 0 {
			t.Errorf("%s changed by %v for default-scheduler pods", m[0], d)
		}
	}
	byZone := placement(pods)
	t.Logf("default-scheduler placement by zone: %v", byZone)
	if len(byZone) < 2 {
		t.Errorf("default-scheduler put all 10 pods in %v: looks carbon-driven", sortedKeys(byZone))
	}

	t.Run("opt-out on carbon profile", func(t *testing.T) {
		before := scrape(t)
		o := carbonOpts()
		o.enabled = ptr.To(false)
		optOut := createPods(t, ns, "optout", 4, o)
		waitRunning(t, ns, optOut, 2*time.Minute)
		after := scrape(t)
		if d := after.value(mOptOut, nil) - before.value(mOptOut, nil); d < 4 {
			t.Errorf("opt_out_total grew by %v, want ≥4", d)
		}
		if d := after.value(mAttempts, nil) - before.value(mAttempts, nil); d != 0 {
			t.Errorf("scoring_attempts_total grew by %v for opt-out pods", d)
		}
		if d := after.value(mFallback, nil) - before.value(mFallback, nil); d != 0 {
			t.Errorf("fallback grew by %v for opt-out pods (FF-INV-13)", d)
		}
		for _, n := range optOut {
			ev, err := scheduledEvent(context.Background(), ns, n)
			if err != nil {
				t.Fatal(err)
			}
			if r := eventReporter(ev); r != carbonScheduler {
				t.Errorf("opt-out pod %s scheduled by %q, want %s", n, r, carbonScheduler)
			}
		}
	})
}

// Test 3: metadata and status of carbon-scheduled pods are filled correctly
// and the scheduler does not rewrite the workload's carbon-aware.io inputs.
func TestE2EPodMetadataAndStatus(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "metadata")
	green, _ := greenestZone(t)

	submitted := map[string]*corev1.Pod{}
	var names []string
	for i := 0; i < 3; i++ {
		p := newPod(ns, "meta-"+string(rune('a'+i)), carbonOpts())
		submitted[p.Name] = p
		names = append(names, p.Name)
		createPod(t, p)
	}
	pods := waitRunning(t, ns, names, 2*time.Minute)

	for _, n := range names {
		p, want := pods[n], submitted[n]
		if p.Spec.SchedulerName != carbonScheduler {
			t.Errorf("%s: schedulerName=%q", n, p.Spec.SchedulerName)
		}
		if zoneOf[p.Spec.NodeName] != green {
			t.Errorf("%s: node %s zone %q, want %s", n, p.Spec.NodeName, zoneOf[p.Spec.NodeName], green)
		}
		if !conditionTrue(p, corev1.PodScheduled) || !conditionTrue(p, corev1.PodReady) {
			t.Errorf("%s: conditions %+v", n, p.Status.Conditions)
		}
		if p.Status.HostIP == "" || p.Status.PodIP == "" || p.Status.StartTime == nil {
			t.Errorf("%s: status not filled: hostIP=%q podIP=%q start=%v", n, p.Status.HostIP, p.Status.PodIP, p.Status.StartTime)
		}
		node, err := cs.CoreV1().Nodes().Get(context.Background(), p.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasAddress(node, p.Status.HostIP) {
			t.Errorf("%s: hostIP %s is not an address of node %s", n, p.Status.HostIP, node.Name)
		}
		for k, v := range want.Labels {
			if p.Labels[k] != v {
				t.Errorf("%s: label %s=%q, submitted %q", n, k, p.Labels[k], v)
			}
		}
		for k, v := range want.Annotations {
			if p.Annotations[k] != v {
				t.Errorf("%s: annotation %s=%q, submitted %q", n, k, p.Annotations[k], v)
			}
		}
		extra := maps.Clone(p.Annotations)
		for k := range want.Annotations {
			delete(extra, k)
		}
		for k, v := range extra {
			if strings.HasPrefix(k, "carbon-aware.io/") {
				t.Logf("%s: scheduler-added annotation %s=%s", n, k, v)
			}
		}

		ev, err := scheduledEvent(context.Background(), ns, n)
		if err != nil {
			t.Fatal(err)
		}
		if r := eventReporter(ev); r != carbonScheduler {
			t.Errorf("%s: Scheduled event from %q, want %s", n, r, carbonScheduler)
		}
		if !strings.Contains(ev.Message, p.Spec.NodeName) {
			t.Errorf("%s: Scheduled event %q does not name node %s", n, ev.Message, p.Spec.NodeName)
		}
	}
}

func conditionTrue(p *corev1.Pod, ct corev1.PodConditionType) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == ct {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func hasAddress(n *corev1.Node, ip string) bool {
	for _, a := range n.Status.Addresses {
		if a.Address == ip {
			return true
		}
	}
	return false
}
