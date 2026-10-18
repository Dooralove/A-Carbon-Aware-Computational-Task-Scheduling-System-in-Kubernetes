package scheduler

import (
	"context"
	"math"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"

	"carbon-aware-k8s/api/v1alpha1"
	pkgcarbon "carbon-aware-k8s/pkg/carbon"
	"carbon-aware-k8s/pkg/energy"
	wlruntime "carbon-aware-k8s/pkg/runtime"
)

const (
	markOK nodeMark = iota
	markUnknown
	markInvalid
)

type nodeMark int

type nodeWork struct {
	name        string
	zone        string
	mark        nodeMark
	cfGCO2e     float64
	ci          float64
	sR          float64
	sP          float64
	pluginScore float64
	hasScore    bool
}

// PreScore is the single per-Pod scoring entry (ARCH-05, ARCH-06).
// Order: parse → opt-in → Ť / SLA → one snapshot.Load → G-08 or (2.19)/(2.23).
func (p *CarbonAwarePlugin) PreScore(ctx context.Context, state fwk.CycleState, pod *v1.Pod, nodes []fwk.NodeInfo) *fwk.Status {
	start := time.Now()
	defer func() { recordPreScoreDuration(time.Since(start).Seconds()) }()

	profile, err := workloadFromPod(pod)
	if err != nil {
		recordSLARejection()
		return statusSLA()
	}

	if !profile.Enabled {
		recordOptOut()
		writeNeutralAll(state, nodes, pod, v1alpha1.FallbackOptOut, p.args.ScoringMode, true, false)
		return statusSuccess()
	}

	hatT, err := wlruntime.ResolveHatTSeconds(profile, p.args.RuntimeClassSeconds, p.args.DefaultWorkloadClass)
	if err != nil {
		recordSLARejection()
		return statusSLA()
	}
	if wlruntime.HardSLAViolated(hatT, profile) {
		recordSLARejection()
		return statusSLA()
	}

	recordScoringAttempt(string(p.args.ScoringMode))

	now := p.now()
	snap := p.Snapshot()
	mode, reason := classifySnapshot(snap, now, p.args.MaxDataAgeSeconds, p.args.StaleWarnSeconds)
	if mode == v1alpha1.FallbackEmpty || mode == v1alpha1.FallbackDead {
		recordFallback(reason)
		writeNeutralAll(state, nodes, pod, mode, p.args.ScoringMode, false, true)
		return statusSuccess()
	}

	wC, wR, wP, err := p.weights(profile)
	if err != nil {
		recordSLARejection()
		return statusSLA()
	}

	works := p.evaluateNodes(now, snap, pod, nodes, hatT, profile, wC, wR, wP)
	missing, invalid, scoredOK := 0, 0, 0
	for i := range works {
		switch works[i].mark {
		case markUnknown:
			missing++
		case markInvalid:
			invalid++
		default:
			if works[i].hasScore {
				scoredOK++
			}
		}
	}
	if scoredOK == 0 {
		recordFallback("all_invalid")
		writeNeutralAll(state, nodes, pod, v1alpha1.FallbackAllInvalid, p.args.ScoringMode, false, true)
		return statusSuccess()
	}

	payload := assembleCycleState(works, pod, p.args.ScoringMode, snap, missing, invalid)
	if mode == v1alpha1.FallbackStale {
		recordStaleInWindow()
		p.warnStale(ctx, snap, reason)
		payload.FallbackMode = v1alpha1.FallbackStale
		payload.DataStale = true
	}
	writeCycleState(state, payload)
	recordUnknownZoneNodes(missing)
	return statusSuccess()
}

func classifySnapshot(snap *v1alpha1.CarbonSnapshot, now time.Time, maxDataAgeSeconds, staleWarnSeconds int64) (v1alpha1.FallbackMode, string) {
	if snap == nil || len(snap.Zones) == 0 {
		return v1alpha1.FallbackEmpty, "empty"
	}
	age := now.Sub(snap.FetchedAt.Time)
	if maxDataAgeSeconds > 0 && age > time.Duration(maxDataAgeSeconds)*time.Second {
		return v1alpha1.FallbackDead, "dead"
	}
	if snap.Quality == v1alpha1.QualityStale || (staleWarnSeconds > 0 && age > time.Duration(staleWarnSeconds)*time.Second) {
		return v1alpha1.FallbackStale, "stale"
	}
	return v1alpha1.FallbackFresh, "fresh"
}

func writeNeutralAll(
	state fwk.CycleState,
	nodes []fwk.NodeInfo,
	pod *v1.Pod,
	mode v1alpha1.FallbackMode,
	scoringMode v1alpha1.ScoringMode,
	optOut, dataStale bool,
) {
	scores := make(map[string]int64, len(nodes))
	neutral := v1alpha1.NeutralFixedPointScore()
	for _, ni := range nodes {
		if name := nodeName(ni); name != "" {
			scores[name] = neutral
		}
	}
	uid := ""
	if pod != nil {
		uid = string(pod.UID)
	}
	writeCycleState(state, &v1alpha1.CarbonCycleState{
		ScorePerNode:  scores,
		FallbackMode:  mode,
		DataStale:     dataStale,
		ScoringMode:   scoringMode,
		OptInDisabled: optOut,
		PodUID:        uid,
	})
}

func (p *CarbonAwarePlugin) evaluateNodes(
	now time.Time,
	snap *v1alpha1.CarbonSnapshot,
	pod *v1.Pod,
	nodes []fwk.NodeInfo,
	hatT int64,
	profile v1alpha1.WorkloadProfile,
	wC, wR, wP float64,
) []nodeWork {
	sP := pkgcarbon.PerformanceScore(hatT, deadlineSeconds(profile), profile.HasFiniteSLA())
	works := make([]nodeWork, 0, len(nodes))
	currentCI := p.args.ScoringMode == v1alpha1.ScoringModeCurrentCI

	var cfMin, cfMax float64
	var ciMin, ciMax float64
	cfReady, ciReady := false, false

	for _, ni := range nodes {
		w := p.evaluateOne(now, snap, pod, ni, hatT, profile, sP, currentCI)
		if w.name == "" {
			continue
		}
		if w.mark == markOK {
			if currentCI {
				if !ciReady {
					ciMin, ciMax, ciReady = w.ci, w.ci, true
				} else {
					ciMin = math.Min(ciMin, w.ci)
					ciMax = math.Max(ciMax, w.ci)
				}
			} else {
				if !cfReady {
					cfMin, cfMax, cfReady = w.cfGCO2e, w.cfGCO2e, true
				} else {
					cfMin = math.Min(cfMin, w.cfGCO2e)
					cfMax = math.Max(cfMax, w.cfGCO2e)
				}
			}
		}
		works = append(works, w)
	}

	epsCF := p.args.EpsilonCFGCO2e
	epsCI := p.args.EpsilonCIGCO2ePerKWh
	for i := range works {
		w := &works[i]
		var sC float64
		switch {
		case w.mark != markOK:
			sC = pkgcarbon.NeutralScore
		case currentCI:
			sC = pkgcarbon.CurrentCIScore(w.ci, ciMin, ciMax, epsCI)
		default:
			sC = pkgcarbon.CarbonScore(w.cfGCO2e, cfMin, cfMax, epsCF)
		}
		if !pkgcarbon.SafePartial(sC) || !pkgcarbon.SafePartial(w.sR) || !pkgcarbon.SafePartial(w.sP) {
			w.mark = markInvalid
			sC = pkgcarbon.NeutralScore
			if !pkgcarbon.SafePartial(w.sR) {
				w.sR = pkgcarbon.NeutralScore
			}
			if !pkgcarbon.SafePartial(w.sP) {
				w.sP = pkgcarbon.NeutralScore
			}
		}
		score := pkgcarbon.WeightedScore(wC, wR, wP, sC, w.sR, w.sP)
		if !pkgcarbon.SafePartial(score) {
			w.mark = markInvalid
			score = 50
		}
		if w.mark == markOK && !currentCI {
			recordEstimatedCF(w.zone, w.cfGCO2e)
		}
		w.pluginScore = score
		w.hasScore = true
		works[i] = *w
	}
	return works
}

func (p *CarbonAwarePlugin) evaluateOne(
	now time.Time,
	snap *v1alpha1.CarbonSnapshot,
	pod *v1.Pod,
	ni fwk.NodeInfo,
	hatT int64,
	profile v1alpha1.WorkloadProfile,
	sP float64,
	currentCI bool,
) nodeWork {
	name := nodeName(ni)
	if name == "" {
		return nodeWork{}
	}
	node := ni.Node()
	ident := v1alpha1.ParseNodeEnergyIdentity(name, nil)
	if node != nil {
		ident = v1alpha1.ParseNodeEnergyIdentity(name, node.Labels)
	}
	idle, maxW := p.powerWatts(ident.PowerProfile)
	cpuAlloc, memAlloc := allocatable(ni)
	cpuReq, memReq := requested(ni)
	uMinus := energy.CPUUtilization(cpuReq, cpuAlloc)
	uPlus := energy.CPUUtilization(cpuReq+profile.CPURequestMillicores, cpuAlloc)
	vPlus := energy.MemoryUtilization(memReq+profile.MemoryRequestBytes, memAlloc)
	sR := energy.ResourceScore(p.args.Lambda, uPlus, vPlus)
	w := nodeWork{name: name, zone: string(ident.Zone), sR: sR, sP: sP}

	if !ident.HasZone {
		w.mark = markUnknown
		return w
	}
	if snap == nil {
		w.mark = markUnknown
		return w
	}
	zst, ok := snap.Zones[ident.Zone]
	if !ok {
		w.mark = markUnknown
		return w
	}
	rec := zst.Current
	if !rec.IsUsableForScoring() || v1alpha1.TimestampFromFuture(rec.Timestamp.Time, now, p.args.ClockSkewSeconds) {
		w.mark = markInvalid
		return w
	}
	w.ci = rec.CarbonIntensityGCO2ePerKWh
	w.mark = markOK
	if currentCI {
		return w
	}
	pPod := energy.LinearPowerModel{PowerIdleWatts: idle, PowerMaxWatts: maxW}.PredictPodMarginal(uMinus, uPlus)
	cf, err := pkgcarbon.IntegrateExpectedFootprint(
		now,
		pPod,
		time.Duration(hatT)*time.Second,
		rec.CarbonIntensityGCO2ePerKWh,
		zst.Forecast,
	)
	if err != nil || !pkgcarbon.SafePartial(cf) {
		w.mark = markInvalid
		return w
	}
	w.cfGCO2e = cf
	return w
}

func assembleCycleState(
	works []nodeWork,
	pod *v1.Pod,
	mode v1alpha1.ScoringMode,
	snap *v1alpha1.CarbonSnapshot,
	missing, invalid int,
) *v1alpha1.CarbonCycleState {
	scores := make(map[string]int64, len(works))
	for _, w := range works {
		if !w.hasScore {
			scores[w.name] = v1alpha1.NeutralFixedPointScore()
			continue
		}
		scores[w.name] = pkgcarbon.FixedPointScore(w.pluginScore)
	}
	uid := ""
	if pod != nil {
		uid = string(pod.UID)
	}
	payload := &v1alpha1.CarbonCycleState{
		ScorePerNode:  scores,
		FallbackMode:  v1alpha1.FallbackFresh,
		ZonesMissing:  missing,
		InvalidZones:  invalid,
		ScoringMode:   mode,
		OptInDisabled: false,
		PodUID:        uid,
	}
	if snap != nil {
		payload.SnapshotFetchedAt = snap.FetchedAt
	}
	return payload
}

func (p *CarbonAwarePlugin) powerWatts(profileName string) (idle, max float64) {
	idle, max = p.args.PowerIdleWatts, p.args.PowerMaxWatts
	if profileName == "" || p.args.PowerProfiles == nil {
		return
	}
	if spec, ok := p.args.PowerProfiles[profileName]; ok {
		return spec.PowerIdleWatts, spec.PowerMaxWatts
	}
	return
}

func (p *CarbonAwarePlugin) weights(profile v1alpha1.WorkloadProfile) (wC, wR, wP float64, err error) {
	if p.args.ScoringMode == v1alpha1.ScoringModeCurrentCI {
		return 1, 0, 0, nil
	}
	wC, wR, wP = p.args.CarbonWeight, p.args.ResourceWeight, p.args.PerformanceWeight
	if !p.args.AllowPodCarbonWeightOverride || profile.CarbonWeight == nil {
		return wC, wR, wP, nil
	}
	wC2 := *profile.CarbonWeight
	rest := 1 - wC2
	sumRP := wR + wP
	if sumRP == 0 {
		if math.Abs(wC2-1) > v1alpha1.EpsilonWeight {
			return 0, 0, 0, v1alpha1.ErrSLAPrecondition
		}
		return 1, 0, 0, nil
	}
	scale := rest / sumRP
	return wC2, wR * scale, wP * scale, nil
}

func (p *CarbonAwarePlugin) warnStale(ctx context.Context, snap *v1alpha1.CarbonSnapshot, reason string) {
	fetched := time.Time{}
	if snap != nil {
		fetched = snap.FetchedAt.Time
	}
	p.staleWarnMu.Lock()
	skip := p.lastStaleFetchedAt.Equal(fetched) && p.lastStaleReason == reason
	if !skip {
		p.lastStaleFetchedAt = fetched
		p.lastStaleReason = reason
	}
	p.staleWarnMu.Unlock()
	if skip {
		return
	}
	klog.FromContext(ctx).Info("CarbonAware snapshot is stale-in-window; scoring continues",
		"reason", reason,
		"fetchedAt", fetched,
	)
}

func workloadFromPod(pod *v1.Pod) (v1alpha1.WorkloadProfile, error) {
	if pod == nil {
		return v1alpha1.WorkloadProfile{Enabled: false}, nil
	}
	profile, err := v1alpha1.NewWorkloadProfile(pod.Namespace, pod.Name, string(pod.UID), pod.Labels, pod.Annotations)
	if err != nil {
		return v1alpha1.WorkloadProfile{}, err
	}
	cpu, mem := podResourceRequests(pod)
	profile.CPURequestMillicores = cpu
	profile.MemoryRequestBytes = mem
	return profile, nil
}

func podResourceRequests(pod *v1.Pod) (cpuMilli, memBytes int64) {
	if pod == nil {
		return 0, 0
	}
	for _, c := range pod.Spec.Containers {
		cpuMilli += c.Resources.Requests.Cpu().MilliValue()
		memBytes += c.Resources.Requests.Memory().Value()
	}
	return cpuMilli, memBytes
}

func allocatable(ni fwk.NodeInfo) (cpuMilli, memBytes int64) {
	if ni == nil {
		return 0, 0
	}
	r := ni.GetAllocatable()
	if r == nil {
		return 0, 0
	}
	return r.GetMilliCPU(), r.GetMemory()
}

func requested(ni fwk.NodeInfo) (cpuMilli, memBytes int64) {
	if ni == nil {
		return 0, 0
	}
	r := ni.GetRequested()
	if r == nil {
		return 0, 0
	}
	return r.GetMilliCPU(), r.GetMemory()
}

func deadlineSeconds(profile v1alpha1.WorkloadProfile) int64 {
	if profile.SLADeadlineSeconds == nil {
		return 0
	}
	return *profile.SLADeadlineSeconds
}
