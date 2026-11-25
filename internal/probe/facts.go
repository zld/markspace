package probe

import (
	"fmt"

	"mediaflow/internal/fault"
)

const sparseKeyframeGapMS = 4000

func detectTimelineFacts(t *Track) []fault.Interval {
	var out []fault.Interval
	if t.Timescale == 0 {
		out = append(out, fault.Interval{
			Kind: "invalid_timebase", Class: fault.ClassTimeBase,
			TrackID: t.ID, TrackNumber: t.Number, Reject: true,
			Message: "track timescale is zero",
		})
		return out
	}
	if len(t.Samples) == 0 {
		out = append(out, fault.Interval{
			Kind: "empty_sample_table", Class: fault.ClassMissingTrack,
			TrackID: t.ID, TrackNumber: t.Number, Reject: true,
			Message: "track contains no samples",
		})
		return out
	}

	if len(t.CompositionOffsets) > 0 && t.CompositionOffsets[0] > 0 {
		tick := t.CompositionOffsets[0]
		startMS, _ := t.Timeline.AbsoluteMS(tick)
		endMS, _ := t.Timeline.AbsoluteMS(tick + int64(t.Samples[0].Duration))
		out = append(out, fault.Interval{
			Kind: "non_zero_start", Class: fault.ClassNonZeroStart,
			TrackID: t.ID, TrackNumber: t.Number, StartSample: 0, EndSample: 1,
			ByteStart: t.Samples[0].Offset, ByteEnd: t.Samples[0].Offset + int64(t.Samples[0].Size),
			StartMS: startMS, EndMS: endMS, Reject: false, Quarantine: false,
			Message: fmt.Sprintf("first composition timestamp is %d ticks (%d ms), not zero", tick, startMS),
		})
	}

	if t.Type == "video" && len(t.SyncSamples) > 0 {
		out = append(out, detectSparseKeyframes(t)...)
	}
	return out
}

func detectSparseKeyframes(t *Track) []fault.Interval {
	keys := sortedSyncIndexes(t.SyncSamples)
	if len(keys) == 0 {
		return nil
	}
	type span struct{ a, b int }
	var spans []span
	if keys[0] > 0 {
		spans = append(spans, span{0, keys[0]})
	}
	for i := 0; i+1 < len(keys); i++ {
		spans = append(spans, span{keys[i], keys[i+1]})
	}
	spans = append(spans, span{keys[len(keys)-1], len(t.Samples)})

	var out []fault.Interval
	for _, sp := range spans {
		startTick := tickAt(t, sp.a)
		endTick := t.LastTick
		if sp.b < len(t.Samples) {
			endTick = tickAt(t, sp.b)
		}
		gapMS, err := t.Timeline.MediaTimebase().TicksToMS(endTick - startTick)
		if err != nil || gapMS <= sparseKeyframeGapMS {
			continue
		}
		startMS, _ := t.Timeline.AbsoluteMS(startTick)
		endMS, _ := t.Timeline.AbsoluteMS(endTick)
		var byteStart, byteEnd int64
		if sp.a < len(t.Samples) {
			byteStart = t.Samples[sp.a].Offset
		}
		if sp.b > 0 && sp.b <= len(t.Samples) {
			idx := sp.b - 1
			byteEnd = t.Samples[idx].Offset + int64(t.Samples[idx].Size)
		}
		out = append(out, fault.Interval{
			Kind: "sparse_keyframe_gap", Class: fault.ClassSparseKeyframe,
			TrackID: t.ID, TrackNumber: t.Number,
			StartSample: int64(sp.a), EndSample: int64(sp.b),
			ByteStart: byteStart, ByteEnd: byteEnd,
			StartMS: startMS, EndMS: endMS, Reject: false,
			Message: fmt.Sprintf("keyframe span samples %d..%d is %d ms", sp.a, sp.b, gapMS),
		})
	}
	return out
}

func tickAt(t *Track, sample int) int64 {
	var tick int64
	for i := 0; i < sample && i < len(t.Samples); i++ {
		tick += int64(t.Samples[i].Duration)
	}
	return tick
}

// MaxKeyframeGapMS returns the largest sync-sample span in milliseconds.
func MaxKeyframeGapMS(t *Track) int64 {
	if t == nil || t.Type != "video" || len(t.SyncSamples) == 0 {
		return 0
	}
	keys := sortedSyncIndexes(t.SyncSamples)
	maxMS := int64(0)
	consider := func(a, b int) {
		startTick := tickAt(t, a)
		endTick := t.LastTick
		if b < len(t.Samples) {
			endTick = tickAt(t, b)
		}
		if ms, err := t.Timeline.MediaTimebase().TicksToMS(endTick - startTick); err == nil && ms > maxMS {
			maxMS = ms
		}
	}
	if keys[0] > 0 {
		consider(0, keys[0])
	}
	for i := 0; i+1 < len(keys); i++ {
		consider(keys[i], keys[i+1])
	}
	consider(keys[len(keys)-1], len(t.Samples))
	return maxMS
}
