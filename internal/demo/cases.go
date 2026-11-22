package demo

import (
	"bytes"
	"fmt"

	"mediacore/index"
	"mediacore/ingest"
	"mediacore/internal/fixture"
	"mediacore/manifest"
	"mediacore/media"
	"mediacore/pipeline"
)

// finish verifies the manifest and fills the report's headline fields.
func (r *Runner) finish(dir string, m *manifest.Manifest, rep *Report) (*Report, int, error) {
	b, err := m.Marshal()
	if err != nil {
		return r.fail(rep, err)
	}
	if err := writeFile(dir, "manifest.json", b); err != nil {
		return r.fail(rep, err)
	}
	rep.Frames = m.Stats.FrameCount
	rep.Segments = m.Stats.SegmentCount
	rep.SilenceMS = m.Stats.SilenceConfirmedMS
	rep.DuplicateGroups = m.Stats.DuplicateGroups
	rep.TargetKBPS = m.Stats.TargetKBPS
	rep.ActualKBPS = m.Stats.ActualKBPS
	rep.DeviationPct = m.Stats.DeviationPct
	rep.AlignmentMS = m.Timing.AlignmentErrorMS
	rep.ManifestDigest = m.ManifestDigest
	rep.IndexDigest = m.IndexDigest
	rep.InputDigest = m.InputDigest
	rep.MissingFrames = m.Stats.MissingFrames
	rep.GapCount = m.Stats.GapCount
	vr := manifest.Verify(m, manifest.DefaultVerifyOptions())
	rep.Checks = vr.Recomputed
	rep.Failures = vr.Failures
	if !vr.OK {
		return rep, 1, nil
	}
	return rep, 0, nil
}

// clean runs the normal sample and asserts the acceptance summary.
func (r *Runner) clean(dir string, rep *Report) (*Report, int, error) {
	_, data, err := r.generate()
	if err != nil {
		return r.fail(rep, err)
	}
	if err := r.writeOutputs(dir, data); err != nil {
		return r.fail(rep, err)
	}
	idx, _, m, cache, err := r.process(dir, data)
	rep.IndexCache = &cache
	if err != nil {
		return r.fail(rep, err)
	}
	if m == nil {
		return r.incompleteScan(rep, idx)
	}
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("summary %s", m.Summary()),
		fmt.Sprintf("rates %s", m.RateLine()),
		fmt.Sprintf("alignment_error_ms=%d", m.Timing.AlignmentErrorMS),
		fmt.Sprintf("cache_state=%s rebuilt=%t", cache.State, cache.Rebuilt),
	)
	rep.Expected = map[string]string{
		"summary":   r.plan.ExpectedSummary(),
		"rate_line": "target_kbps=900 actual_kbps=870 deviation_pct=3.33",
	}
	if r.opts.Frames == 0 {
		want := "frames=1200 segments=12 silence_ms=2400 duplicate_groups=3"
		if m.Summary() != want {
			rep.Failures = append(rep.Failures, manifest.Failure{
				Category: media.CatManifestMismatch, Field: "summary",
				Expected: want, Actual: m.Summary(),
			})
			out, code, _ := r.finish(dir, m, rep)
			return out, code, nil
		}
		if rate := m.RateLine(); rate != "target_kbps=900 actual_kbps=870 deviation_pct=3.33" {
			rep.Failures = append(rep.Failures, manifest.Failure{
				Category: media.CatManifestMismatch, Field: "rate_line",
				Expected: "target_kbps=900 actual_kbps=870 deviation_pct=3.33", Actual: rate,
			})
		}
		if m.Timing.AlignmentErrorMS > 2 {
			rep.Failures = append(rep.Failures, manifest.Failure{
				Category: media.CatTimeDiscontinuity, Field: "alignment_error_ms",
				Expected: "<=2", Actual: fmt.Sprintf("%d", m.Timing.AlignmentErrorMS),
			})
		}
	}
	return r.finish(dir, m, rep)
}

// burstLoss drops a contiguous packet run and shows the explicit gaps, the
// affected segment boundary and the reduced byte total.
func (r *Runner) burstLoss(dir string, rep *Report) (*Report, int, error) {
	frames, _, err := r.generate()
	if err != nil {
		return r.fail(rep, err)
	}
	start := len(frames) * 3 / 10
	count := 50
	packets, err := fixture.ApplyPacketFaults(frames, fixture.PacketPayload, fixture.PacketFaults{
		BurstLossStart: start,
		BurstLossCount: count,
	})
	if err != nil {
		return r.fail(rep, err)
	}
	res, err := ingest.ProcessPacketStream(packets, ingest.DefaultReorderConfig())
	if err != nil {
		return r.fail(rep, err)
	}
	rep.Duplicates = res.Duplicates
	rep.Late = res.Late
	rep.MissingFrames = res.MissingFrames()
	rep.GapCount = len(res.Gaps)
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("ingest gaps=%d missing_frames=%d duplicates=%d late=%d",
			len(res.Gaps), res.MissingFrames(), res.Duplicates, res.Late))
	// Rebuild the frame stream from delivered packet sequence numbers so the
	// loss is visible to segmentation and bitrate statistics.
	seen := make(map[uint32]bool, len(res.Packets))
	for _, p := range res.Packets {
		seen[uint32(p.Seq)] = true
	}
	survivors := make([]*media.Frame, 0, len(frames))
	for _, f := range frames {
		if seen[f.Seq] {
			survivors = append(survivors, f)
		}
	}
	data, err := fixture.EncodeFrames(survivors)
	if err != nil {
		return r.fail(rep, err)
	}
	if err := r.writeOutputs(dir, data); err != nil {
		return r.fail(rep, err)
	}
	idx, _, m, cache, err := r.process(dir, data)
	rep.IndexCache = &cache
	if err != nil {
		return r.fail(rep, err)
	}
	if m == nil {
		return r.incompleteScan(rep, idx)
	}
	rep.Frames = m.Stats.FrameCount
	rep.MissingFrames = m.Stats.MissingFrames
	rep.GapCount = m.Stats.GapCount
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("segments=%d gap_closed=%d", m.Stats.SegmentCount, countGapClosed(m)),
		fmt.Sprintf("summary %s", m.Summary()),
		fmt.Sprintf("rates %s", m.RateLine()))
	return r.finish(dir, m, rep)
}

func countGapClosed(m *manifest.Manifest) int {
	n := 0
	for _, s := range m.Segments {
		if s.ClosedBy == "gap" {
			n++
		}
	}
	return n
}

// corrupt flips a payload byte, which breaks the record hash. The scan stops,
// the offset is reported, and rerunning after repair rebuilds the index.
func (r *Runner) corrupt(dir string, rep *Report) (*Report, int, error) {
	_, clean, err := r.generate()
	if err != nil {
		return r.fail(rep, err)
	}
	recSize := fixture.RecordSizeFor(r.fopts)
	damagedFrame := 200
	damaged, off := fixture.ApplyFrameDamage(clean, recSize, fixture.Damage{ChecksumFrame: damagedFrame})
	rep.DamageOffset = off
	if err := r.writeOutputs(dir, damaged); err != nil {
		return r.fail(rep, err)
	}
	idx, err := index.Scan(bytes.NewReader(damaged), index.DefaultOptions())
	if err != nil {
		rep.Failures = nil
		if e, ok := media.AsError(err); ok {
			rep.Failures = append(rep.Failures, manifest.Failure{
				Category: e.Category, Field: string(e.Stage),
				Expected: "valid checksum", Actual: e.Message,
				Detail: fmt.Sprintf("offset=%d suggestion=%s", e.FrameOffset, e.Suggestion),
			})
			rep.DamageOffset = e.FrameOffset
		}
		locate := "unknown"
		if _, ok := idx.Resolve(rep.DamageOffset); ok {
			locate = "indexed"
		}
		rep.Notes = append(rep.Notes,
			fmt.Sprintf("corrupt_offset=%d valid_frames_before_damage=%d damaged_slot=%s",
				rep.DamageOffset, idx.EntryCount, locate),
			"index_rebuild_reason=checksum_mismatch")
		// Keep the damaged material under damaged/ so it can still be
		// inspected and (correctly) fails verification, then repair and rerun.
		r.publishDamaged(dir, rep, damaged, idx, "checksum_mismatch")
		return r.repairAndVerify(dir, rep, clean)
	}
	return nil, 2, media.New(media.CatUnknown, media.StageCLI, "corruption was not detected")
}

// truncated removes the tail of the last record.
func (r *Runner) truncated(dir string, rep *Report) (*Report, int, error) {
	_, clean, err := r.generate()
	if err != nil {
		return r.fail(rep, err)
	}
	damaged, off := fixture.ApplyFrameDamage(clean, fixture.RecordSizeFor(r.fopts), fixture.Damage{
		TruncateTail: 900,
	})
	rep.DamageOffset = off
	if err := r.writeOutputs(dir, damaged); err != nil {
		return r.fail(rep, err)
	}
	idx, err := index.Scan(bytes.NewReader(damaged), index.DefaultOptions())
	if err != nil {
		if e, ok := media.AsError(err); ok {
			rep.Failures = append(rep.Failures, manifest.Failure{
				Category: e.Category, Field: string(e.Stage),
				Expected: "complete final record", Actual: e.Message,
				Detail: fmt.Sprintf("offset=%d", e.FrameOffset),
			})
		}
		rep.Notes = append(rep.Notes,
			fmt.Sprintf("truncated_offset=%d valid_frames=%d", rep.DamageOffset, idx.EntryCount),
			"index_rebuild_reason=truncated_frame")
		r.publishDamaged(dir, rep, damaged, idx, "truncated_frame")
		return r.repairAndVerify(dir, rep, clean)
	}
	return nil, 2, media.New(media.CatUnknown, media.StageCLI, "truncation was not detected")
}

// wrap crosses the 32-bit RTP timestamp boundary.
func (r *Runner) wrap(dir string, rep *Report) (*Report, int, error) {
	fopts := r.fopts
	fopts.WrapTimestamps = true
	frames, err := r.plan.BuildFrames(fopts)
	if err != nil {
		return r.fail(rep, err)
	}
	data, err := fixture.EncodeFrames(frames)
	if err != nil {
		return r.fail(rep, err)
	}
	if err := r.writeOutputs(dir, data); err != nil {
		return r.fail(rep, err)
	}
	idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
	if err != nil {
		return r.fail(rep, err)
	}
	wrapped := 0
	for _, e := range idx.Entries {
		if e.Wrapped {
			wrapped++
		}
	}
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(data, media.DefaultLimits()), pipeline.DefaultConfig(media.DefaultTimescale))
	if err != nil {
		return r.fail(rep, err)
	}
	prev := int64(-1)
	monotonic := true
	for _, e := range idx.Entries {
		if e.Ticks < prev {
			monotonic = false
		}
		prev = e.Ticks
	}
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("wrap_events=%d reordered=%t monotonic=%t ticks_end=%d",
			wrapped, idx.Reordered, monotonic, idx.EndTicks),
		fmt.Sprintf("segments=%d frames=%d", len(res.Segments), res.FrameCount))
	if wrapped == 0 || idx.Reordered || !monotonic {
		return r.fail(rep, media.New(media.CatTimeDiscontinuity, media.StageScan,
			"clock wraparound was not handled monotonically"))
	}
	// Continue through the same verify path as a clean run.
	_, _, m, cache, err := r.process(dir, data)
	rep.IndexCache = &cache
	if err != nil {
		return r.fail(rep, err)
	}
	return r.finish(dir, m, rep)
}

// digestShift changes a frame's content after an index was cached, which must
// invalidate the cached index and rebuild it.
func (r *Runner) digestShift(dir string, rep *Report) (*Report, int, error) {
	_, clean, err := r.generate()
	if err != nil {
		return r.fail(rep, err)
	}
	// Build and cache the pristine index, then prove it is reused unchanged.
	base, cacheRep := index.EnsureCache(cachePath(dir), digestOf(clean), func() (*index.Index, error) {
		return index.Scan(bytes.NewReader(clean), index.DefaultOptions())
	})
	if base == nil {
		return r.fail(rep, media.New(media.CatUnknown, media.StageIndex, "initial scan failed: %s", cacheRep.Reason))
	}
	if _, hitRep := index.LoadCache(cachePath(dir), digestOf(clean)); hitRep.State != index.CacheHit {
		return r.fail(rep, media.New(media.CatIndexInput, media.StageIndex,
			"pristine cache was not reusable: %s", hitRep.Reason))
	}
	rep.Notes = append(rep.Notes, "prechange_cache_state=hit")
	// Now the input changes: one frame's pixels differ and the record is
	// re-checksummed, so the scanner accepts it and only the input digest
	// changes. This is the "camera content changed" case, not byte damage.
	recSize := fixture.RecordSizeFor(r.fopts)
	rewritten, off, err := reshapeFrame(clean, recSize, 10)
	if err != nil {
		return r.fail(rep, err)
	}
	rep.DamageOffset = off
	if err := r.writeOutputs(dir, rewritten); err != nil {
		return r.fail(rep, err)
	}
	idx, res, m, cache, err := r.process(dir, rewritten)
	rep.IndexCache = &cache
	if err != nil {
		return r.fail(rep, err)
	}
	if m == nil {
		return r.incompleteScan(rep, idx)
	}
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("cache_state=%s rebuilt=%t reason=%s", cache.State, cache.Rebuilt, cache.Reason),
		fmt.Sprintf("index_digest_before=%s", shortDigest(base.IndexDigest)),
		fmt.Sprintf("index_digest_after=%s", shortDigest(idx.IndexDigest)),
		fmt.Sprintf("frames=%d segments=%d", res.FrameCount, len(res.Segments)))
	if !cache.Rebuilt || cache.State == index.CacheHit {
		return r.fail(rep, media.New(media.CatIndexInput, media.StageIndex,
			"digest change did not invalidate the cache"))
	}
	if base.IndexDigest == idx.IndexDigest {
		return r.fail(rep, media.New(media.CatIndexInput, media.StageIndex,
			"index digest did not change after content edit"))
	}
	return r.finish(dir, m, rep)
}

// packets demonstrates the full ingest path: duplicates, reordering, a burst
// loss and late arrivals, with the resulting explicit gaps.
func (r *Runner) packets(dir string, rep *Report) (*Report, int, error) {
	// The reorder window is bounded by the 16-bit sequence space; a compact
	// sample keeps every sequence distinct so loss is unambiguous.
	frames, _, err := r.generate()
	if err != nil {
		return r.fail(rep, err)
	}
	packets, err := fixture.ApplyPacketFaults(frames, fixture.PacketPayload, fixture.PacketFaults{
		DuplicateEvery: 50,
		SwapEvery:      7,
		BurstLossStart: len(frames) * 2 / 5,
		BurstLossCount: 30,
		LateCount:      2,
	})
	if err != nil {
		return r.fail(rep, err)
	}
	res, err := ingest.ProcessPacketStream(packets, ingest.DefaultReorderConfig())
	if err != nil {
		return r.fail(rep, err)
	}
	rep.Duplicates = res.Duplicates
	rep.Late = res.Late
	rep.MissingFrames = res.MissingFrames()
	rep.GapCount = len(res.Gaps)
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("packets_ordered=%d duplicate_packets=%d late_packets=%d",
			len(res.Packets), res.Duplicates, res.Late),
		fmt.Sprintf("gaps=%d missing_frames=%d", len(res.Gaps), res.MissingFrames()))
	if res.Duplicates == 0 || len(res.Gaps) == 0 || res.Late == 0 {
		return r.fail(rep, media.New(media.CatUnknown, media.StageIngest,
			"expected duplicates, gaps and late packets, got %d/%d/%d",
			res.Duplicates, len(res.Gaps), res.Late))
	}
	return rep, 0, nil
}

// repairAndVerify replaces the damaged file with clean material and proves the
// rerun produces a complete, verifying result.
func (r *Runner) repairAndVerify(dir string, rep *Report, clean []byte) (*Report, int, error) {
	// The cached index was written from damaged bytes; its digest cannot match
	// the repaired input, so EnsureCache must rebuild.
	if err := r.writeOutputs(dir, clean); err != nil {
		return r.fail(rep, err)
	}
	idx, res, m, cache, err := r.process(dir, clean)
	rep.IndexCache = &cache
	if err != nil {
		return r.fail(rep, err)
	}
	if m == nil {
		return r.incompleteScan(rep, idx)
	}
	rep.Recovered = true
	rep.MissingFrames = 0
	rep.GapCount = 0
	rep.Notes = append(rep.Notes,
		fmt.Sprintf("recovery cache_state=%s rebuilt=%t", cache.State, cache.Rebuilt),
		fmt.Sprintf("recovered_frames=%d recovered_segments=%d", res.FrameCount, len(res.Segments)),
		fmt.Sprintf("recovered_summary %s", m.Summary()))
	if !cache.Rebuilt && cache.State != index.CacheMiss {
		// A stale cache that was not rebuilt would be a correctness bug.
		rep.Failures = append(rep.Failures, manifest.Failure{
			Category: media.CatIndexInput, Field: "recovery",
			Expected: "index rebuilt from repaired input", Actual: string(cache.State),
		})
	}
	// Report the original damage but verify the repaired manifest cleanly.
	damageFailures := rep.Failures
	out, code, err := r.finish(dir, m, rep)
	if code == 0 {
		rep.Failures = damageFailures
	}
	return out, code, err
}

func (r *Runner) incompleteScan(rep *Report, idx *index.Index) (*Report, int, error) {
	if idx != nil {
		rep.Frames = idx.EntryCount
		rep.IndexDigest = idx.IndexDigest
		rep.InputDigest = idx.InputDigest
	}
	return rep, 1, nil
}
