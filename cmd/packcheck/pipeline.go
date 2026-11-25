package main

import (
	"fmt"
	"os"
	"strings"

	"packcheck/internal/clock"
	"packcheck/internal/container"
	"packcheck/internal/model"
	"packcheck/internal/recovery"
	"packcheck/internal/report"
	"packcheck/internal/segment"
	"packcheck/internal/stream"
	"packcheck/internal/subtitle"
)

func run(args []string) int {
	code, out := runCaptured(args...)
	if out != "" {
		fmt.Print(out)
	}
	return code
}

// runCaptured executes the pipeline and returns the exit code together with
// the rendered report. Error messages go to stderr and yield an empty report.
func runCaptured(args ...string) (int, string) {
	opt, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "packcheck: %v\n", err)
		return report.ExitRuntime, ""
	}
	rate, err := clock.ParseRate(opt.fpsToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "packcheck: %v\n", err)
		return report.ExitRuntime, ""
	}

	res := &report.Result{}
	allIssues := &model.IssueLog{}

	// --- 1. Subtitle stream (SRT source with offset-annotated cues). ---
	srtDoc, corruptOffset := buildSubtitleFixture(opt)
	subRes, perr := subtitle.ParseSRT(strings.NewReader(srtDoc), rate, cutPoints())
	if perr != nil {
		fmt.Fprintf(os.Stderr, "packcheck: subtitle parse failure: %v\n", perr)
		return report.ExitRuntime, ""
	}
	for _, i := range subRes.Issues.All() {
		allIssues.Add(i)
	}
	if opt.corrupt == "subtitle-offset" {
		// Surface the injected source offset explicitly for shell checks.
		allIssues.Add(model.Issue{
			Code: model.CodeCueAtCutPoint, Severity: model.SeverityWarning,
			Layer: model.LayerSubtitle, ByteOffset: corruptOffset,
			Message: "injected corruption: cue offset relocated onto cut point",
		})
	}

	// --- 2. Per-layer containers and RTP fragment streams. ---
	specs := layerSpecs()
	res.LayerCount = len(specs)
	fan := stream.NewFanOut(opt.queueCap, slowTimeout, names(specs)...)
	fanIn := stream.NewFanIn()
	recMgr := recovery.NewManager()
	type layerRuntime struct {
		spec        layerSpec
		mp4         []byte
		mp4Issues   model.IssueLog
		pkts        []segment.Packet
		segIssues   model.IssueLog
		healthy     bool
		outputBytes int64
	}
	runtimes := make(map[string]*layerRuntime)
	var totalInput, totalOutput int64

	for _, sp := range specs {
		rt := &layerRuntime{spec: sp}
		buildOpt := container.BuildOptions{
			Timescale: sp.timescale, Samples: sp.samples, SegmentFrames: sp.segFrames,
		}
		if opt.truncate[sp.name] {
			buildOpt.Truncate = true
		}
		raw, berr := container.Build(buildOpt)
		if berr != nil {
			fmt.Fprintf(os.Stderr, "packcheck: container build failure: %v\n", berr)
			return report.ExitRuntime, ""
		}
		rt.mp4 = raw
		c, perr := container.Parse(bytesReader(raw))
		if perr != nil {
			fmt.Fprintf(os.Stderr, "packcheck: container parse failure: %v\n", perr)
			return report.ExitRuntime, ""
		}
		vr := container.Validate(c, 1)
		rt.mp4Issues = vr.Issues
		for _, i := range vr.Issues.All() {
			i.Representation = sp.name
			allIssues.Add(i)
		}

		fullPkts, _ := segment.BuildPackets(sp.packetCount, 0, sp.rtpStep, ssrcFor(sp.name))
		pkts := fullPkts
		if opt.truncate[sp.name] {
			// Drop the final fragment so boundary validation detects drift.
			pkts = fullPkts[:len(fullPkts)-1]
		}
		rt.pkts = pkts
		br := segment.ValidateRTP(pkts, rate)
		segment.CompareToDeclared(br, c)
		rt.segIssues = br.Issues
		for _, i := range br.Issues.All() {
			i.Representation = sp.name
			allIssues.Add(i)
		}
		rt.healthy = !vr.Issues.HasError() && !br.Issues.HasError()
		// Input is the full expected byte stream; output is what this layer
		// actually produced, so truncation shows up as a budget delta.
		fullLayerBytes := int64(len(raw)) + packetBytes(fullPkts)
		rt.outputBytes = int64(len(raw)) + packetBytes(pkts)
		totalInput += fullLayerBytes
		totalOutput += rt.outputBytes
		runtimes[sp.name] = rt

		recMgr.Register(sp.name, rt.healthy)
		fanIn.Submit(stream.LayerOutput{
			Representation: sp.name, Packets: pkts, Healthy: rt.healthy,
		})
	}

	// --- 3. Bounded fan-out; isolate the declared slow consumer. ---
	drainers := launchDrainers(fan, specs, opt.slowLayer)
	for i := 0; i < 12; i++ {
		fan.Send(stream.Item{
			Sequence: i, Timestamp: uint32(i) * 3600, ByteOffset: int64(i * 12),
		})
	}
	fan.CloseAll()
	drainers.wait()
	for _, i := range fan.Issues().All() {
		allIssues.Add(i)
	}
	for _, d := range fan.Dropped() {
		res.Anomalies = append(res.Anomalies, report.AnomalyInterval{
			Representation: d.Representation, Code: model.CodeSlowConsumer,
			ByteOffset: d.EndOffset, StartTS: d.StartTS, EndTS: d.EndTS,
		})
	}
	res.PeakQueueDepth = maxPeak(fan)

	// --- 4. Shared-clock convergence and independent recovery. ---
	sigs, err := fanIn.Signatures()
	if err != nil {
		fmt.Fprintf(os.Stderr, "packcheck: signature failure: %v\n", err)
		return report.ExitRuntime, ""
	}
	healthySigs := healthyOnly(sigs)
	// The shared clock is the healthy layers' full timestamp grid: a failed
	// layer recomputes its boundaries against it rather than against its own
	// truncated fragments.
	var shared []uint32
	if len(healthySigs) > 0 {
		shared = healthySigs[0].Timestamps
	}
	for _, st := range recMgr.States() {
		if st.Before == "failed" {
			recMgr.Recover(st.Representation, shared, healthySigs)
		}
	}
	aligned, mismatches := segment.CrossCheck(sigsWithRecovery(recMgr, sigs))
	for _, mm := range mismatches {
		allIssues.Add(mm)
	}
	res.AlignedBoundaries = aligned
	refCount := 0
	if len(healthySigs) > 0 {
		refCount = len(healthySigs[0].Timestamps)
	}
	for _, sg := range sigs {
		var first, last uint32
		if n := len(sg.Timestamps); n > 0 {
			first, last = sg.Timestamps[0], sg.Timestamps[n-1]
		}
		res.CrossChecks = append(res.CrossChecks, report.CrossCheckValue{
			Representation: sg.Representation, Healthy: sg.Healthy,
			ObservedCount: len(sg.Timestamps), ReferenceCount: refCount,
			Matched:        sg.Healthy && len(sg.Timestamps) == refCount,
			FirstTimestamp: first, LastTimestamp: last,
		})
	}

	// --- 5. Rollback manifest: complete validated layers only. ---
	eligible := recMgr.Eligible()
	var entries []recovery.ManifestEntry
	for _, name := range eligible {
		rt := runtimes[name]
		entries = append(entries, recovery.ManifestEntry{
			Representation: name, Bytes: rt.outputBytes,
			BoundaryCount: len(rt.pkts), Verdict: verdictOf(recMgr, name),
		})
	}
	manifest := recovery.BuildManifest(entries)
	res.ManifestText, res.ManifestDigest = manifest.Summary()

	// --- 6. Per-layer conclusions and anomaly intervals. ---
	for _, sp := range specs {
		rt := runtimes[sp.name]
		inManifest := containsName(eligible, sp.name)
		res.Conclusions = append(res.Conclusions, report.LayerConclusion{
			Representation: sp.name,
			ContainerOK:    !rt.mp4Issues.HasError(),
			SubtitleOK:     !subRes.Issues.HasError(),
			// Boundary health covers both the declared sidx constraints and
			// the reconstructed RTP fragment boundaries.
			BoundaryOK: !rt.mp4Issues.HasError() && !rt.segIssues.HasError(),
			Verdict:    string(verdictOf(recMgr, sp.name)),
			InManifest: inManifest,
		})
		// Anomaly intervals carry the layer's observed RTP span so the
		// abnormal start/end times are explicit in the report.
		var firstTS, lastTS uint32
		if ts, terr := segment.PacketTimestamps(rt.pkts); terr == nil && len(ts) > 0 {
			firstTS, lastTS = ts[0], ts[len(ts)-1]
		}
		for _, i := range append(rt.mp4Issues.All(), rt.segIssues.All()...) {
			res.Anomalies = append(res.Anomalies, report.AnomalyInterval{
				Representation: sp.name, Code: i.Code, ByteOffset: i.ByteOffset,
				StartTS: firstTS, EndTS: lastTS,
			})
		}
	}
	if opt.corrupt == "subtitle-offset" {
		res.Anomalies = append(res.Anomalies, report.AnomalyInterval{
			Representation: "subtitle", Code: model.CodeCueAtCutPoint,
			ByteOffset: corruptOffset,
			StartTS:    uint32(cutPointSeconds()) * segment.ClockRate,
			EndTS:      uint32((cutPointSeconds() + 1)) * segment.ClockRate,
		})
	}

	// --- 7. Budget, threshold and final rendering. ---
	res.InputBytes = totalInput
	res.OutputBytes = totalOutput
	res.BudgetDeltaBytes = report.BudgetDelta(totalInput, totalOutput)
	res.EvaluateBudget(opt.budgetBytes)
	res.Issues = allIssues.All()

	if opt.outDir != "" {
		if err := writeArtifacts(opt.outDir, res); err != nil {
			fmt.Fprintf(os.Stderr, "packcheck: %v\n", err)
			return report.ExitRuntime, ""
		}
	}
	return res.ExitCode(), res.Render()
}
