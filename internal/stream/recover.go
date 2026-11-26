package stream

import (
	"os"

	"medtrace/internal/core"
)

// recover finds the earliest segment after the failed one whose bytes verify
// and whose first in-range sample is a verified keyframe, then emits from that
// keyframe. It returns the recovery record and the index of the resume segment
// (or -1 when nothing could be resumed).
//
// The rule is deliberately conservative: a segment may only resume playback if
// its whole file hashes to the index value and its keyframe payload can be
// read in full. Anything else extends the unrecovered interval rather than
// risk emitting samples that were never verified.
func (r *Runner) recover(segs []*core.Segment, failedIdx int, failed *core.Segment, damage *core.Fault, from, to core.Nanos, res *Result) (core.Recovery, int) {
	rec := core.Recovery{
		FailedSegment:    failed.URI,
		FailedByteOffset: damage.Pos.Byte,
		UnrecoveredStart: clampStart(failed.Start, from),
		UnrecoveredEnd:   clampEnd(failed.End, to),
	}
	for j := failedIdx + 1; j < len(segs); j++ {
		cand := segs[j]
		if cand.Truncated {
			rec.UnrecoveredEnd = clampEnd(cand.End, to)
			continue
		}
		if !r.segmentVerifies(cand, res) {
			rec.UnrecoveredEnd = clampEnd(cand.End, to)
			continue
		}
		res.KeyframeTries++
		kf, ok := firstKeyframeIn(cand, from, to)
		if !ok {
			rec.UnrecoveredEnd = clampEnd(cand.End, to)
			continue
		}
		if f := verifyKeyframe(r.Root, cand, kf); f != nil {
			rec.UnrecoveredEnd = clampEnd(cand.End, to)
			res.Anomalies = append(res.Anomalies, core.Anomaly{
				Category: core.CatTruncated, Pos: core.Pos{File: cand.URI, Byte: kf.ByteOffset},
				Track: cand.Track, Segment: cand.URI, ByteOffset: kf.ByteOffset,
				RawTimestamp: kf.RawTicks, Timescale: kf.Timescale, Canonical: kf.Canonical,
				Detail: "resume keyframe failed verification: " + f.Detail,
			})
			continue
		}
		res.KeyframeHits++
		rec.Resumed = true
		rec.ResumeSegment = cand.URI
		rec.ResumeByteOffset = kf.ByteOffset
		rec.ResumeCanonical = kf.Canonical
		for _, s := range cand.Samples {
			if s.Canonical < kf.Canonical || s.Canonical < from || s.Canonical >= to {
				continue
			}
			res.Processed = append(res.Processed, s)
		}
		return rec, j
	}
	res.Anomalies = append(res.Anomalies, core.Anomaly{
		Category: core.CatNoKeyframe, Pos: core.Pos{File: failed.URI, Byte: rec.FailedByteOffset},
		Track: failed.Track, Segment: failed.URI, ByteOffset: rec.FailedByteOffset,
		Detail: "no verified keyframe after the failed segment",
	})
	return rec, -1
}

// segmentVerifies compares the segment's current hash to the index value when
// hash verification is enabled, and records a mismatch anomaly.
func (r *Runner) segmentVerifies(seg *core.Segment, res *Result) bool {
	if !r.VerifyHashes {
		return true
	}
	sum, err := hashFile(r.Root, seg.URI)
	if err != nil {
		return false
	}
	if sum != seg.SHA256 {
		res.HashBad++
		res.Anomalies = append(res.Anomalies, core.Anomaly{
			Category: core.CatHashMismatch, Pos: core.Pos{File: seg.URI, Byte: 0},
			Track: seg.Track, Segment: seg.URI, ByteOffset: 0,
			Expected: seg.SHA256, Actual: sum,
			Detail: "segment SHA-256 differs from the index",
		})
		return false
	}
	res.HashChecks++
	res.HashMatches++
	return true
}

// firstKeyframeIn returns the first keyframe of seg inside [from,to).
func firstKeyframeIn(seg *core.Segment, from, to core.Nanos) (core.Sample, bool) {
	for _, k := range seg.Keyframes {
		s := seg.Samples[k]
		if s.Canonical >= from && s.Canonical < to {
			return s, true
		}
	}
	return core.Sample{}, false
}

// verifyKeyframe checks the keyframe's bytes lie inside the file and can be
// read in full. It reports the original offset on failure.
func verifyKeyframe(root string, seg *core.Segment, kf core.Sample) *core.Fault {
	abs, err := core.SafeJoin(root, seg.URI)
	if err != nil {
		return core.NewFault(core.KindPath, core.Pos{File: seg.URI, Byte: kf.ByteOffset}, "verify", err.Error())
	}
	f, err := os.Open(abs)
	if err != nil {
		return core.WrapFault(err, core.KindIO, core.Pos{File: seg.URI, Byte: kf.ByteOffset}, "verify", "open")
	}
	defer f.Close()
	buf := make([]byte, kf.Size)
	if _, err := f.ReadAt(buf, kf.ByteOffset); err != nil {
		return core.WrapFault(err, core.KindSegmentTruncated, core.Pos{File: seg.URI, Byte: kf.ByteOffset}, "verify", "read keyframe")
	}
	return nil
}

// clampStart/End keep the unrecovered bounds inside the requested interval so
// a report never claims to have skipped media the caller did not ask for.
func clampStart(v, from core.Nanos) core.Nanos {
	if v < from {
		return from
	}
	return v
}

func clampEnd(v, to core.Nanos) core.Nanos {
	if v > to {
		return to
	}
	return v
}
