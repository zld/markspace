package pipeline

import (
	"mediarun/internal/frames"
	"mediarun/internal/recovery"
	"mediarun/internal/syncer"
)

// selectSegments partitions records and already-placed audio into segments.
//
// Every segment starts on a keyframe so each output fragment is independently
// decodable. When the stream has fewer keyframes than requested segments the
// count is reduced rather than emitting a segment that cannot be indexed.
// Within the keyframe constraint starts are spaced as evenly as possible. Frame
// order is preserved and no frame is duplicated or dropped. Audio is assigned
// by the placed timeline tick, so a segment carries the audio that actually
// sounds over its video span.
func selectSegments(recs []*frames.Record, audio []syncer.Placed, count int, gaps []recovery.Gap) []SegmentPlan {
	if len(recs) == 0 || count < 1 {
		return nil
	}
	starts := keyframeStarts(recs)
	if count > len(starts) {
		count = len(starts)
	}
	if count > len(recs) {
		count = len(recs)
	}
	if count < 1 {
		count = 1
	}
	// Pick evenly spaced keyframe starts, always including index zero.
	picked := make([]int, 0, count)
	for i := 0; i < count; i++ {
		pos := i * len(starts) / count
		if pos >= len(starts) {
			pos = len(starts) - 1
		}
		if len(picked) > 0 && starts[pos] <= picked[len(picked)-1] {
			continue
		}
		picked = append(picked, starts[pos])
	}
	plans := make([]SegmentPlan, 0, len(picked))
	for i, start := range picked {
		end := len(recs)
		if i+1 < len(picked) {
			end = picked[i+1]
		}
		if end <= start {
			continue
		}
		plans = append(plans, SegmentPlan{
			Index:     len(plans),
			Frames:    recs[start:end],
			StartTick: int64(recs[start].Meta.PTS),
			EndTick:   segmentEndTick(recs, start, end),
		})
	}
	if len(plans) == 0 {
		return nil
	}
	for _, p := range audio {
		idx := 0
		for i, plan := range plans {
			if p.Tick >= plan.StartTick {
				idx = i
			}
		}
		plans[idx].Audio = append(plans[idx].Audio, p)
	}
	// An unrecoverable audio hole taints only the segment whose audio resumes
	// after it; the other fragments remain complete.
	for _, g := range gaps {
		for i := range plans {
			found := false
			for _, p := range plans[i].Audio {
				if p.Unit.Seq == g.NextSeq {
					found = true
					break
				}
			}
			if found {
				plans[i].Incomplete = true
			}
		}
	}
	return plans
}

// keyframeStarts returns record indices that may begin a segment. Index zero is
// always eligible so the first fragment is never empty, even if the producer
// forgot to flag its first frame.
func keyframeStarts(recs []*frames.Record) []int {
	out := []int{0}
	for i := 1; i < len(recs); i++ {
		if recs[i].Keyframe() {
			out = append(out, i)
		}
	}
	return out
}

// segmentEndTick is the half-open end of a segment in the 90 kHz domain. The
// final segment extends by one nominal frame step so it is never empty.
func segmentEndTick(recs []*frames.Record, start, end int) int64 {
	if end < len(recs) {
		return int64(recs[end].Meta.PTS)
	}
	if end-start >= 2 {
		step := int64(recs[end-1].Meta.PTS) - int64(recs[end-2].Meta.PTS)
		if step <= 0 {
			step = int64(recs[end-1].Meta.PTS) - int64(recs[start].Meta.PTS)
		}
		return int64(recs[end-1].Meta.PTS) + step
	}
	return int64(recs[end-1].Meta.PTS)
}
