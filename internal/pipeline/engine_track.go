package pipeline

import (
	"strconv"

	"mediavault/internal/archive"
	"mediavault/internal/fingerprint"
	"mediavault/internal/ingest"
	"mediavault/internal/repair"
	"mediavault/internal/report"
	"mediavault/internal/timeline"
)

// processTrack fingerprints every sample of one parsed track, groups them
// into segments and maps the results onto archive manifest rows. The
// boundary-jump fault exercises the repairer through an RTP side-channel.
func (e *Engine) processTrack(pt *ingest.ParsedTrack, rep *repair.BoundaryRepairer, b *report.Builder) ([]archive.ManifestRow, *timeline.StageError) {
	id := pt.ID
	norm := timeline.NewTrackNormalizer(id, timeline.ClockRef{Source: id.Source, Clock: id.Index}, pt.Timescale, 0)
	rep.Register(id, norm)

	// When the boundary-jump fault is active, synthesize a clock reset on
	// the MP4 video timeline and repair it through the shared repairer.
	// The RTP track is reserved for the 32-bit wrap path and is not jumped.
	if e.cfg.Fault == FaultBoundaryJump && id.Kind == timeline.KindVideo && id.Source == "demo" {
		if err := e.injectBoundaryJump(pt, norm, rep, b); err != nil {
			return nil, err
		}
	}

	// Group samples into small deterministic segments (4 samples each).
	const segSize = 4
	var rows []archive.ManifestRow
	knownContent := map[string]bool{}
	knownTimeline := map[string]bool{}
	for start := 0; start < len(pt.Samples); start += segSize {
		end := start + segSize
		if end > len(pt.Samples) {
			end = len(pt.Samples)
		}
		seg := fingerprint.OfSegment(id.String(), pt.Samples[start:end])
		fingerprint.MarkDuplicate(&seg, knownContent, knownTimeline)
		knownContent[seg.ContentKey] = true
		knownTimeline[seg.TimelineKey] = true
		b.SetSegmentCount(b.Build().SegmentCount + 1)

		for _, fp := range seg.Samples {
			stageKey := fingerprint.StableKey(timeline.StageFingerprint, fp.FragmentSHA,
				id.String()+":"+strconv.FormatUint(fp.Order, 10))
			rows = append(rows, archive.ManifestRow{
				StageKey:         stageKey,
				Track:            fp.Track,
				Order:            fp.Order,
				FragmentSHA:      fp.FragmentSHA,
				FrameSHA:         fp.FrameSHA,
				SegmentSHA:       seg.SegmentSHA,
				PixelMean:        fp.PixelMean,
				PixelVariance:    fp.PixelVar,
				PeakDBFS:         fp.PeakDBFS,
				AlignmentNs:      fp.AlignmentNs,
				DTSNs:            fp.DTSNs,
				ContentDupe:      seg.ContentDupe,
				TimelineDistinct: seg.TimelineDistinct,
			})
		}
	}
	return rows, nil
}

// injectBoundaryJump feeds the track's samples through a normalizer after a
// synthetic clock reset, applies a traceable correction and records it.
func (e *Engine) injectBoundaryJump(pt *ingest.ParsedTrack, norm *timeline.TrackNormalizer, rep *repair.BoundaryRepairer, b *report.Builder) *timeline.StageError {
	if len(pt.Samples) < 3 {
		return nil
	}
	// Establish the original clock from the first samples.
	for i := 0; i < 2; i++ {
		if _, err := norm.PushSample(pt.Samples[i].TS.RawDTS, pt.Samples[i].TS.RawDTS); err != nil {
			return asStage(err)
		}
	}
	// Reset the raw clock close to zero to emulate a source switch.
	jumpedRaw := int64(100)
	_, err := norm.PushSample(jumpedRaw, jumpedRaw)
	if err == nil {
		// Small timelines may not trip the threshold; fall back to a
		// direct correction so the fault still produces a traceable record.
		se := &timeline.StageError{
			Stage: timeline.StageNormalize, Kind: timeline.KindBoundaryJump,
			Offset: -1, Position: 3, Track: pt.ID, Recoverable: true,
			Detail: "synthetic source-switch clock reset",
		}
		if _, rerr := rep.Handle(se, jumpedRaw); rerr != nil {
			return asStage(rerr)
		}
		return nil
	}
	se, ok := err.(*timeline.StageError)
	if !ok {
		return asStage(err)
	}
	if _, rerr := rep.Handle(se, jumpedRaw); rerr != nil {
		return asStage(rerr)
	}
	return nil
}

func asStage(err error) *timeline.StageError {
	if se, ok := err.(*timeline.StageError); ok {
		return se
	}
	return &timeline.StageError{
		Stage: timeline.StageFingerprint, Kind: timeline.KindUnrecoverable,
		Recoverable: false, Detail: err.Error(), Err: err,
	}
}
