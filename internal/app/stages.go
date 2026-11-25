package app

import (
	"context"
	"sort"

	"mediapipeline/internal/align"
	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
	"mediapipeline/internal/manifest"
	"mediapipeline/internal/provenance"
	"mediapipeline/internal/scan"
	"mediapipeline/internal/stream"
)

// stageScan streams fragments through the bounded queue. A truncated box
// stops the pipeline immediately; no checkpoint is advanced past this stage
// on any failure, so the last safe checkpoint (if any) remains untouched.
func (p *Pipeline) stageScan(ctx context.Context, summary *Summary) error {
	cp := p.resumeCheckpoint()

	// Init segments are small and read first to establish each clock.
	for _, st := range p.states {
		init, err := scan.ReadInitSegment(st.desc.Directory + "/init.mp4")
		if err != nil {
			return p.classify(err, summary)
		}
		st.init = init
		st.tracker = scan.NewSequenceTrackerWithDuration(st.desc.ID, st.desc.TickDuration)
		boundary, err := scan.ReadRTPStream(st.desc.Directory + "/" + fixture.RTPFileName)
		if err != nil {
			return p.classify(err, summary)
		}
		st.boundary = boundary
	}

	// Build the global work list in deterministic input-index order.
	var work []scanItem
	for repIdx, st := range p.states {
		names, err := listFragmentFiles(st.desc.Directory)
		if err != nil {
			return p.classify(err, summary)
		}
		for inputIdx, name := range names {
			work = append(work, scanItem{
				repIdx:  repIdx,
				path:    st.desc.Directory + "/" + name,
				relPath: st.desc.ID + "/" + name,
				index:   inputIdx,
			})
		}
	}

	// On resume, skip inputs already completed with a matching digest only
	// after re-validating them; everything else is recomputed from scratch.
	processed := map[string]*scan.Fragment{}
	proc := func(_ context.Context, item stream.Item) error {
		si := item.Payload.(scanItem)
		f, err := scan.ReadFragment(si.index, p.states[si.repIdx].desc.ID, si.path)
		if err != nil {
			return err
		}
		if cp != nil && cp.Reached(checkpoint.StageScan) && cp.VerifyInput(si.relPath, f.SHA256) {
			processed[si.relPath] = f
			return nil
		}
		processed[si.relPath] = f
		return nil
	}
	pl, err := stream.NewPipeline(p.cfg.QueueCapacity, 1, proc)
	if err != nil {
		return p.classify(err, summary)
	}
	stats, err := pl.Run(ctx, func(ctx context.Context, q *stream.BoundedQueue) error {
		for _, si := range work {
			if err := q.Enqueue(ctx, stream.Item{Index: si.index, Key: si.relPath, Payload: si}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return p.classify(err, summary)
	}
	summary.QueuePeak = stats.QueuePeak

	// Feed fragments to the per-representation sequence trackers in order.
	for _, st := range p.states {
		names, _ := listFragmentFiles(st.desc.Directory)
		for inputIdx, name := range names {
			rel := st.desc.ID + "/" + name
			f := processed[rel]
			if f == nil {
				return fault.New(fault.CodeTruncatedBox, "fragment vanished from scan: %s", rel)
			}
			f.InputIndex = inputIdx
			if err := st.tracker.Observe(f); err != nil {
				return p.classify(err, summary)
			}
			st.frags = append(st.frags, f)
		}
	}

	// Only now, with every input validated, is the scan checkpoint durable.
	// On a resume or repeat run an equal-or-later checkpoint may already
	// exist; checkpoints only ever move forward, so it is kept as-is.
	next := p.buildCheckpoint(cp, checkpoint.StageScan)
	next.CompletedFragments = sortedKeys(processed)
	if existing := p.existingCheckpoint(); existing != nil && existing.Reached(checkpoint.StageScan) {
		summary.CheckpointVersion = existing.Version
		summary.CheckpointStage = string(existing.Stage)
	} else if err := p.store.Save(next); err != nil {
		return p.classify(err, summary)
	} else {
		summary.CheckpointVersion = next.Version
		summary.CheckpointStage = string(checkpoint.StageScan)
	}
	return nil
}

// stageAlign converts scan reports to normalized nanosecond boundaries and
// records the alignment checkpoint before any manifest work begins.
func (p *Pipeline) stageAlign(ctx context.Context, summary *Summary) error {
	inputs := make([]align.Input, 0, len(p.states))
	gapCount, regressionCount := 0, 0
	for _, st := range p.states {
		report := st.tracker.Report()
		gapCount += len(report.Gaps)
		regressionCount += len(report.Regressions)
		inputs = append(inputs, align.Input{
			Representation: st.desc.ID,
			Timescale:      st.init.Timescale,
			Report:         report,
		})
	}
	alignment, err := align.Align(inputs)
	if err != nil {
		return p.classify(err, summary)
	}
	p.alignment = alignment
	summary.GapCount = gapCount
	summary.RegressionCount = regressionCount
	summary.AlignedSegments = alignment.AlignedSegmentCount()

	cp, _ := p.store.Load()
	next := p.buildCheckpoint(cp, checkpoint.StageAlign)
	if existing := p.existingCheckpoint(); existing != nil && existing.Reached(checkpoint.StageAlign) {
		summary.CheckpointVersion = existing.Version
		summary.CheckpointStage = string(existing.Stage)
	} else if err := p.store.Save(next); err != nil {
		return p.classify(err, summary)
	} else {
		summary.CheckpointVersion = next.Version
		summary.CheckpointStage = string(checkpoint.StageAlign)
	}
	return nil
}

// stageManifest builds, validates and atomically persists the manifest, then
// advances the checkpoint only after the file is durably on disk.
func (p *Pipeline) stageManifest(ctx context.Context, summary *Summary) error {
	reports := make([]*scan.TrackReport, 0, len(p.states))
	for _, st := range p.states {
		reports = append(reports, st.tracker.Report())
	}
	m := manifest.Build(ManifestVersion, p.alignment, reports, p.alignment.ToleranceNanos)
	if err := m.Validate(); err != nil {
		// Validation failure must not move the checkpoint past align.
		return p.classify(err, summary)
	}
	path := p.cfg.OutputDir + "/" + ManifestPath
	digest, _, err := m.WriteAtomic(path)
	if err != nil {
		return p.classify(err, summary)
	}
	summary.ManifestSHA256 = digest
	summary.ManifestValid = true
	summary.CommittedSegments = m.SegmentCount

	cp, _ := p.store.Load()
	next := p.buildCheckpoint(cp, checkpoint.StageManifest)
	next.ManifestVersion = m.ManifestVersion
	next.ManifestSHA256 = digest
	if existing := p.existingCheckpoint(); existing != nil && existing.Reached(checkpoint.StageManifest) {
		summary.CheckpointVersion = existing.Version
		summary.CheckpointStage = string(existing.Stage)
	} else if err := p.store.Save(next); err != nil {
		return p.classify(err, summary)
	} else {
		summary.CheckpointVersion = next.Version
		summary.CheckpointStage = string(checkpoint.StageManifest)
	}
	return nil
}

// stageComplete writes provenance and only then marks the run complete. On a
// retry whose prior trace exists, a non-empty field diff is reported without
// overwriting the previous successful artifacts.
func (p *Pipeline) stageComplete(ctx context.Context, summary *Summary) error {
	trace := provenance.New(ManifestVersion)
	for _, b := range p.alignment.Boundaries {
		entry := provenance.Entry{
			SegmentIndex: b.Index,
			StartNanos:   b.StartNanos,
			EndNanos:     b.EndNanos,
		}
		for _, mem := range b.Members {
			entry.Inputs = append(entry.Inputs, provenance.InputRef{
				InputPath: mem.InputPath,
				SHA256:    mem.SHA256,
				MFHD:      mem.MFHD,
				TFDT:      mem.TFDT,
			})
		}
		trace.Add(entry)
	}
	tracePath := p.cfg.OutputDir + "/" + TracePath
	if previous, err := provenance.ReadJSON(tracePath); err == nil {
		diffs := provenance.Diff(previous, trace)
		summary.ProvenanceDiff = len(diffs)
		if len(diffs) > 0 {
			summary.ConflictDiffs = diffs
			// Keep the prior artifact; surface the differing fields.
			summary.ProvenanceSHA256, _ = previous.SHA256()
			return nil
		}
		// Identical re-run: keep the existing trace, same digest.
		summary.ProvenanceSHA256, _ = previous.SHA256()
	} else {
		if err := trace.WriteJSON(tracePath); err != nil {
			return p.classify(err, summary)
		}
		summary.ProvenanceSHA256, _ = trace.SHA256()
	}

	cp, _ := p.store.Load()
	next := p.buildCheckpoint(cp, checkpoint.StageComplete)
	if existing := p.existingCheckpoint(); existing != nil && existing.Reached(checkpoint.StageComplete) {
		summary.CheckpointVersion = existing.Version
		summary.CheckpointStage = string(existing.Stage)
		return nil
	}
	if err := p.store.Save(next); err != nil {
		return p.classify(err, summary)
	}
	summary.CheckpointVersion = next.Version
	summary.CheckpointStage = string(checkpoint.StageComplete)
	return nil
}

func sortedKeys(m map[string]*scan.Fragment) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
