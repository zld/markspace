package pipeline

import (
	"path/filepath"

	"mediapack/ingest"
	"mediapack/model"
	"mediapack/report"
	"mediapack/sample"
	"mediapack/segment"
	"mediapack/stream"
	"mediapack/timeline"
)

// Run generates local input, processes it through the dependency graph and
// bounded queue, and writes/verifies the common-timeline manifest.
func Run(cfg Config) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return &Result{Status: "invalid_argument", OK: false, Command: "demo", Config: cfg}, err
	}
	res := &Result{Status: "ok", OK: true, Command: "demo", Config: cfg, OutputDir: cfg.OutputDir}
	recovery := stream.NewRecoveryManager()
	collector := report.NewCollector()
	generated := sample.Generate(cfg.Seed, cfg.BitrateCount)

	nodes := []timeline.Node{
		{ID: "audio", Kind: model.KindAudio},
		{ID: generated.Reps[0].Track, Kind: model.KindVideo},
		{ID: generated.Reps[1].Track, Kind: model.KindVideo},
		{ID: "subtitle", Kind: model.KindSubtitle, DependsOn: []model.TrackID{generated.Reps[0].Track}, WaitForKeyframe: true},
		{ID: "chapter", Kind: model.KindChapter, DependsOn: []model.TrackID{generated.Reps[0].Track}},
		{ID: "id3", Kind: model.KindID3, DependsOn: []model.TrackID{"audio"}},
		{ID: "emsg", Kind: model.KindEMSG, DependsOn: []model.TrackID{"audio", generated.Reps[0].Track}, WaitForKeyframe: true},
		{ID: "sei", Kind: model.KindSEI, DependsOn: []model.TrackID{generated.Reps[0].Track}, WaitForKeyframe: true},
	}
	graph, err := timeline.NewGraph(nodes)
	if err != nil {
		return res, err
	}
	res.GraphOrder = graph.TopoOrder()

	if _, err := passThroughQueue(generated, cfg, collector); err != nil {
		return res, err
	}

	mp4Stage := prepareMP4Stage(generated, cfg)
	for _, w := range mp4Stage.Warnings {
		recovery.AddWarning(w)
	}
	res.Corruption = mp4Stage.Info
	if mp4Stage.Err != nil {
		tr := recovery.HandleMediaError(mp4Stage.Err)
		if !mp4Stage.Err.Recoverable {
			res.Status = "track_stopped_unsafe"
			res.OK = false
			res.Recovery = recovery.Tracks()
			_ = tr
			return res, mp4Stage.Err
		}
	}
	if cfg.Corrupt == "missing-keyframe" && mp4Stage.Info != nil {
		recovery.AddWarning(model.Warning{
			Track: generated.Reps[0].Track, Offset: mp4Stage.Info.BoxOffset,
			Category: model.CategoryMissingKeyframe,
			Message:  "6s keyframe removed; common boundary delayed to next keyframe",
		})
	}

	rtpStream := sample.BuildRTPStream(48, cfg.Reorder, cfg.Losses)
	window := cfg.Reorder
	if window < 1 {
		window = 1
	}
	reorder := ingest.NewRTPReorder("rtp/av", window, 0)
	for _, p := range rtpStream.Wire {
		if _, err := reorder.Push(p); err != nil {
			return res, err
		}
	}
	reorder.Finish()
	rtpStats := reorder.Stats()
	recovery.AddRTPStats(rtpStats)

	metadata, err := ingest.ParseMetadata("timed-metadata", generated.Metadata)
	if err != nil {
		return res, err
	}
	for _, w := range metadata.Warnings {
		recovery.AddWarning(w)
	}
	dedup := timeline.NewDedup()
	events := make([]model.Event, 0, len(metadata.Events))
	for _, ev := range metadata.Events {
		ev.Track = trackForKind(ev.Kind)
		if dedup.Add(ev) {
			events = append(events, ev)
		}
	}

	duration := generated.DurationNanos
	keyframes := append([]int64(nil), generated.Keyframes...)
	if len(mp4Stage.Samples) > 0 {
		keyframes = keyframes[:0]
		for _, smp := range mp4Stage.Samples {
			if smp.Keyframe {
				keyframes = append(keyframes, smp.PTSNanos)
			}
		}
		if mp4Stage.Err != nil && mp4Stage.Err.Recoverable && mp4Stage.Err.Category == model.CategoryTruncatedBox {
			last := mp4Stage.Samples[len(mp4Stage.Samples)-1]
			duration = last.PTSNanos + last.DurationNanos
		}
	}
	boundaries, intervals, err := segment.PlanBoundaries(segment.PlanConfig{
		SegmentDurationNanos: 3_000_000_000,
		DurationNanos:        duration,
		Keyframes:            keyframes,
		Track:                generated.Reps[0].Track,
	})
	if err != nil {
		return res, err
	}

	gate := timeline.NewKeyframeGate()
	for _, b := range boundaries {
		gate.MarkReady(generated.Reps[0].Track, b.Nanos)
	}
	// Schedule sealing checks in dependency-topological order. A subtitle or
	// timed-metadata node that waits for a video keyframe cannot pass a boundary
	// until that exact keyframe has been observed.
	for _, id := range graph.TopoOrder() {
		node, _ := graph.Node(id)
		if !node.WaitForKeyframe {
			continue
		}
		keyDeps := make([]model.TrackID, 0, len(node.DependsOn))
		for _, dep := range node.DependsOn {
			if depNode, ok := graph.Node(dep); ok && depNode.Kind == model.KindVideo {
				keyDeps = append(keyDeps, dep)
			}
		}
		if len(keyDeps) == 0 {
			continue
		}
		for _, b := range boundaries {
			if err := gate.RequireSeal(id, b.Nanos, keyDeps...); err != nil {
				return res, err
			}
		}
	}

	videoPTS := make([]int64, 0, len(generated.Video[0]))
	for _, u := range generated.Video[0] {
		videoPTS = append(videoPTS, u.PTSNanos)
	}
	audioPTS := make([]int64, 0, len(generated.Audio))
	for _, u := range generated.Audio {
		audioPTS = append(audioPTS, u.PTSNanos)
	}
	syncReport := timeline.MaxAVOffset(videoPTS, audioPTS)

	repManifests, files, err := buildSegmentFiles(generated, intervals)
	if err != nil {
		return res, err
	}
	manifestEvents := buildManifestEvents(events, intervals, repManifests)
	trackManifests := buildTrackManifests(graph)
	manifest := &segment.Manifest{
		Version: segment.ManifestVersion, Seed: cfg.Seed,
		Timebase:      timeline.CommonTimebase().String(),
		DurationNanos: duration, SegmentTargetNanos: 3_000_000_000,
		Boundaries: boundaries, Tracks: trackManifests,
		Representations: repManifests, Events: manifestEvents,
	}
	resumeInfo, err := writeOrResume(cfg.OutputDir, cfg, manifest, files)
	if err != nil {
		res.Resume = resumeInfo
		res.Recovery = recovery.Tracks()
		res.Warnings = recovery.Warnings()
		res.Status = "unsafe_resume"
		res.OK = false
		return res, err
	}
	res.Resume = resumeInfo
	res.ManifestPath = filepath.Join(cfg.OutputDir, "manifest.json")
	res.ManifestSHA256 = manifest.ManifestSHA256

	counts := make([]int, 0, len(repManifests))
	totalSegments := 0
	for _, rm := range repManifests {
		counts = append(counts, len(rm.Segments))
		totalSegments += len(rm.Segments)
	}
	delayed := 0
	for _, b := range boundaries {
		if b.Delayed {
			delayed++
		}
	}
	collector.Update(func(m *report.Metrics) {
		m.AVSyncErrorNanos = syncReport.MaxOffsetNanos
		m.MatchedAVPairs = syncReport.Matched
		m.LossRecoveryRate = rtpStats.LossRecoveryRate
		m.ReorderRecoveryRate = rtpStats.ReorderRecoveryRate
		m.RecoveryRate = rtpStats.RecoveryRate
		m.UnrecoveredPackets = rtpStats.UnrecoveredPackets
		m.DuplicatePackets = rtpStats.DuplicatePackets
		m.SegmentCount = totalSegments
		m.SegmentCountPerRepresentation = counts
		m.RepresentationCount = len(repManifests)
		m.EventCount = len(manifestEvents)
		m.BoundaryCount = len(boundaries)
		m.DelayedBoundaryCount = delayed
	})
	res.Metrics = collector.Snapshot()
	res.Warnings = recovery.Warnings()
	res.Recovery = recovery.Tracks()
	if len(res.Warnings) > 0 {
		res.Status = "ok_with_warnings"
	}
	return res, nil
}

func trackForKind(kind model.UnitKind) model.TrackID {
	switch kind {
	case model.KindSubtitle:
		return "subtitle"
	case model.KindChapter:
		return "chapter"
	case model.KindID3:
		return "id3"
	case model.KindEMSG:
		return "emsg"
	case model.KindSEI:
		return "sei"
	default:
		return model.TrackID(kind)
	}
}

func buildTrackManifests(g *timeline.Graph) []segment.TrackManifest {
	out := make([]segment.TrackManifest, 0)
	for _, id := range g.TopoOrder() {
		n, _ := g.Node(id)
		out = append(out, segment.TrackManifest{ID: id, Kind: n.Kind, Timebase: timeline.CommonTimebase().String()})
	}
	return out
}
