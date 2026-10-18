//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Chaos 1: Carbon Data Service is stopped while pods are being submitted.
// Pods keep scheduling on the last-good snapshot, then on G-08 (Score=50 for
// all nodes) once the snapshot is older than maxDataAgeSeconds. A scheduler
// restart while CDS is still down starts cold (FF-CRASH → reason=empty).
func TestChaosCDSDownDuringScheduling(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "chaos-cds-down")
	t.Cleanup(func() { scaleCDS(t, 1) })

	before := scrape(t)
	var mu sync.Mutex
	var names []string
	submit := func(prefix string, i int) {
		n := fmt.Sprintf("%s-%02d", prefix, i)
		createPod(t, newPod(ns, n, carbonOpts()))
		mu.Lock()
		names = append(names, n)
		mu.Unlock()
	}

	// 20 pods at 2/s; CDS is killed after the 4th one.
	for i := 0; i < 20; i++ {
		submit("burst", i)
		if i == 3 {
			patchCDSReplicas(t, 0) // returns immediately; pods keep arriving
		}
		time.Sleep(500 * time.Millisecond)
	}
	scaleCDS(t, 0) // confirm the CDS pod is gone
	burst := waitRunning(t, ns, names, 2*time.Minute)
	t.Logf("burst placement while CDS stops (last-good snapshot): %v", placement(burst))
	if z := placement(burst); z["north"] != len(burst) {
		t.Errorf("snapshot within maxDataAge must keep carbon ranking (all north), got %v", z)
	}

	maxAge := float64(args.MaxDataAgeSeconds)
	age := waitSnapshotAge(t, seconds(args.MaxDataAgeSeconds)*3, "dead", func(a float64) bool { return a > maxAge })
	t.Logf("snapshot age %.0fs > maxDataAge %.0fs: G-08 expected", age, maxAge)

	deadBefore := scrape(t).value(mFallback, map[string]string{"reason": "dead"})
	var dead []string
	for i := 0; i < 5; i++ {
		n := fmt.Sprintf("dead-%02d", i)
		createPod(t, newPod(ns, n, carbonOpts()))
		dead = append(dead, n)
	}
	deadPods := waitRunning(t, ns, dead, 2*time.Minute)
	after := scrape(t)
	t.Logf("placement under G-08: %v", placement(deadPods))
	if d := after.value(mFallback, map[string]string{"reason": "dead"}) - deadBefore; d < 5 {
		t.Errorf("fallback{reason=dead} grew by %v, want ≥5", d)
	}
	if d := after.value(mFallback, nil) - before.value(mFallback, nil); d < 5 {
		t.Errorf("fallback counter grew by %v over the scenario, want ≥5", d)
	}
	if !strings.Contains(schedulerLogs(t, 2*time.Minute), "cache sync tick failed") {
		t.Error("scheduler log has no 'cache sync tick failed' while CDS is down")
	}

	t.Run("scheduler restart while CDS is down", func(t *testing.T) {
		restartDeployment(t, carbonScheduler)
		var cold []string
		for i := 0; i < 3; i++ {
			n := fmt.Sprintf("cold-%02d", i)
			createPod(t, newPod(ns, n, carbonOpts()))
			cold = append(cold, n)
		}
		waitRunning(t, ns, cold, 2*time.Minute)
		if v := scrape(t).value(mFallback, map[string]string{"reason": "empty"}); v < 3 {
			t.Errorf("fallback{reason=empty} on the restarted scheduler = %v, want ≥3", v)
		}
	})
}

// Chaos 2: the CDS process stays alive but stops answering (SIGSTOP on the
// kind node), so Worker ticks time out and the snapshot only ages. In-window staleness keeps the
// full score with a warning; past maxDataAgeSeconds carbon scoring is off.
func TestChaosStaleDataDisablesCarbonScoring(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "chaos-stale")
	pod := freezeCDS(t)
	t.Cleanup(func() { thawCDS(t, pod) })
	staleWarn, maxAge := float64(args.StaleWarnSeconds), float64(args.MaxDataAgeSeconds)

	waitSnapshotAge(t, seconds(args.MaxDataAgeSeconds), "stale-in-window", func(a float64) bool { return a > staleWarn+1 && a < maxAge-10 })
	before := scrape(t)
	stale := createPods(t, ns, "stale", 3, carbonOpts())
	stalePods := waitRunning(t, ns, stale, time.Minute)
	mid := scrape(t)
	if age := mid.value(mSnapshotAge, nil); age >= maxAge {
		t.Fatalf("snapshot reached %0.fs before stale pods were scored; scenario window missed", age)
	}
	if z := placement(stalePods); z["north"] != len(stale) {
		t.Errorf("stale-in-window must keep carbon ranking (all north), got %v", z)
	}
	if d := mid.value(mStaleInWindow, nil) - before.value(mStaleInWindow, nil); d < 3 {
		t.Errorf("stale_in_window_total grew by %v, want ≥3", d)
	}
	if d := mid.value(mFallback, map[string]string{"reason": "dead"}) - before.value(mFallback, map[string]string{"reason": "dead"}); d != 0 {
		t.Errorf("fallback{dead} grew by %v inside the window", d)
	}

	if now, err := cs.CoreV1().Pods(stackNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{}); err != nil ||
		now.UID != pod.UID || restarts(now) != restarts(pod) {
		t.Fatalf("CDS pod must stay the same process in this scenario (err=%v)", err)
	}

	waitSnapshotAge(t, seconds(args.MaxDataAgeSeconds)*3, "dead", func(a float64) bool { return a > maxAge })
	dead := createPods(t, ns, "dead", 6, carbonOpts())
	deadPods := waitRunning(t, ns, dead, 2*time.Minute)
	after := scrape(t)
	t.Logf("placement with carbon scoring disabled: %v", placement(deadPods))
	if d := after.value(mFallback, map[string]string{"reason": "dead"}) - mid.value(mFallback, map[string]string{"reason": "dead"}); d < 6 {
		t.Errorf("fallback{reason=dead} grew by %v, want ≥6", d)
	}

	// Recovery: endpoints back → fresh snapshot → carbon ranking returns.
	thawCDS(t, pod)
	waitSnapshotAge(t, seconds(args.MaxDataAgeSeconds)*2, "fresh again", func(a float64) bool { return a < staleWarn })
	rec := createPods(t, ns, "recovered", 3, carbonOpts())
	if z := placement(waitRunning(t, ns, rec, time.Minute)); z["north"] != 3 {
		t.Errorf("after recovery pods must go north again, got %v", z)
	}
}

// Chaos 3: north (the green zone) is filled to its CPU allocatable. The
// standard NodeResourcesFit filter must move new pods to west without
// overcommitting north, and a pod that fits nowhere stays Pending because of
// Filter, not because of CarbonAware (FF-RES, FF-INV-11).
func TestChaosNodeCPUOverload(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "chaos-cpu")
	ctx := context.Background()

	var north []string
	for n, z := range zoneOf {
		if z == "north" {
			north = append(north, n)
		}
	}
	const probeCPU = 200 // millicores
	for _, node := range north {
		free := freeCPU(t, node)
		fill := free - probeCPU/2 // leave less than one probe pod
		if fill <= 0 {
			continue
		}
		o := podOpts{scheduler: defaultScheduler, cpu: fmt.Sprintf("%dm", fill), nodeName: node}
		createPod(t, newPod(ns, "filler-"+node, o))
	}
	var fillers []string
	for _, node := range north {
		fillers = append(fillers, "filler-"+node)
	}
	waitRunning(t, ns, fillers, time.Minute)

	before := scrape(t)
	o := carbonOpts()
	o.cpu = fmt.Sprintf("%dm", probeCPU)
	probes := createPods(t, ns, "probe", 5, o)
	pods := waitRunning(t, ns, probes, 2*time.Minute)
	t.Logf("placement with north full: %v", placement(pods))
	if z := placement(pods); z["west"] != len(probes) {
		t.Errorf("north is full: all probes must go west, got %v", z)
	}
	for _, node := range north {
		if f := freeCPU(t, node); f < 0 {
			t.Errorf("node %s overcommitted by %dm", node, -f)
		}
	}

	// A pod larger than any node's free CPU.
	maxFree := int64(0)
	for node := range zoneOf {
		if f := freeCPU(t, node); f > maxFree {
			maxFree = f
		}
	}
	o.cpu = fmt.Sprintf("%dm", maxFree+1000)
	createPod(t, newPod(ns, "too-big", o))
	var msg string
	err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		evs, err := cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=too-big,reason=FailedScheduling"})
		if err != nil || len(evs.Items) == 0 {
			return false, nil
		}
		msg = evs.Items[0].Message
		return true, nil
	})
	if err != nil {
		t.Fatalf("no FailedScheduling event for oversized pod: %v", err)
	}
	if !strings.Contains(msg, "Insufficient cpu") {
		t.Errorf("FailedScheduling should come from NodeResourcesFit, got %q", msg)
	}
	p, _ := cs.CoreV1().Pods(ns).Get(ctx, "too-big", metav1.GetOptions{})
	if p.Spec.NodeName != "" {
		t.Errorf("oversized pod was bound to %s", p.Spec.NodeName)
	}
	after := scrape(t)
	if d := after.value(mFallback, nil) - before.value(mFallback, nil); d != 0 {
		t.Errorf("resource exhaustion must not count as carbon fallback (grew by %v)", d)
	}
}

// Chaos 4: carbon intensity jumps (north 100 → 900, west 400 → 100) while
// pods are running. Running pods are not moved or restarted; new pods follow
// the new profile.
func TestChaosCarbonIntensitySpike(t *testing.T) {
	ensureBaseline(t)
	ns := testNamespace(t, "chaos-spike")
	t.Cleanup(func() { setCDSDataset(t, baselineDataset) })

	running := createPods(t, ns, "running", 4, carbonOpts())
	old := waitRunning(t, ns, running, 2*time.Minute)
	if z := placement(old); z["north"] != len(running) {
		t.Fatalf("pre-spike pods must be on north, got %v", z)
	}

	before := scrape(t)
	setCDSDataset(t, spikeDataset)
	waitSchedulerSees(t, "north", 900)
	green, ci := greenestZone(t)
	t.Logf("after spike CDS serves %v → expected zone %s", ci, green)
	if green != "west" {
		t.Fatalf("spike dataset must make west greenest, got %s", green)
	}

	fresh := createPods(t, ns, "new", 6, carbonOpts())
	newPods := waitRunning(t, ns, fresh, 2*time.Minute)
	t.Logf("post-spike placement: %v", placement(newPods))
	if z := placement(newPods); z["west"] != len(fresh) {
		t.Errorf("new pods must follow the spike to west, got %v", z)
	}

	for _, n := range running {
		now, err := cs.CoreV1().Pods(ns).Get(context.Background(), n, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("running pod %s: %v", n, err)
		}
		was := old[n]
		if now.UID != was.UID || now.Spec.NodeName != was.Spec.NodeName || now.Status.Phase != corev1.PodRunning ||
			now.DeletionTimestamp != nil || restarts(now) != 0 {
			t.Errorf("running pod %s changed: uid %s→%s node %s→%s phase=%s restarts=%d",
				n, was.UID, now.UID, was.Spec.NodeName, now.Spec.NodeName, now.Status.Phase, restarts(now))
		}
	}
	if d := scrape(t).value(mFallback, nil) - before.value(mFallback, nil); d != 0 {
		t.Errorf("a CI spike is fresh data, not a fallback (grew by %v)", d)
	}
}

// freeCPU is allocatable minus the requests of non-terminal pods on node (millicores).
func freeCPU(t *testing.T, node string) int64 {
	t.Helper()
	ctx := context.Background()
	n, err := cs.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	alloc := n.Status.Allocatable[corev1.ResourceCPU]
	pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		t.Fatal(err)
	}
	used := resource.NewMilliQuantity(0, resource.DecimalSI)
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, c := range p.Spec.Containers {
			if r, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				used.Add(r)
			}
		}
	}
	return alloc.MilliValue() - used.MilliValue()
}

func restarts(p *corev1.Pod) int32 {
	var n int32
	for _, s := range p.Status.ContainerStatuses {
		n += s.RestartCount
	}
	return n
}
