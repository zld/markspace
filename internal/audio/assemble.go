package audio

// AssemblePCM builds one continuous mono S16LE buffer at TargetRate by placing
// each segment's PCM at its planned sample offset. Intervals with no source
// audio are filled with silence: nothing is fabricated as source content, and
// every such interval already appears as an explicit gap in the manifest.
//
// Overlapping segments (same PID, adjacent PTS) are concatenated; any samples
// that would run past an earlier segment's end are clipped to stay monotonic.
func AssemblePCM(placements []Placement, pcmBySegment map[int][]byte,
	totalSamples int64) []byte {
	out := make([]byte, totalSamples*pcmBytesPerSample)
	for _, pl := range placements {
		pcm, ok := pcmBySegment[pl.SegmentID]
		if !ok {
			continue
		}
		off := pl.StartSample * pcmBytesPerSample
		n := int64(len(pcm))
		if off < 0 || off >= int64(len(out)) {
			continue
		}
		if off+n > int64(len(out)) {
			n = int64(len(out)) - off
		}
		copy(out[off:off+n], pcm[:n])
	}
	return out
}

// GroupSegments coalesces adjacent elementary-stream access units into larger
// processing segments of roughly targetDuration nanoseconds. Longer segments
// give ffmpeg enough signal for stable loudness normalization and bitrate
// control. Grouping is deterministic and keyed on the monotonic PTS line.
func GroupSegments(segs []Segment, targetDuration int64) []Segment {
	if len(segs) == 0 {
		return nil
	}
	var out []Segment
	cur := Segment{
		ID: 0, PID: segs[0].PID, StartNanos: segs[0].StartNanos,
		CodecHint: segs[0].CodecHint,
	}
	curStart := segs[0].StartNanos
	// memberIDs remembers the original access-unit indices in each group
	for i, s := range segs {
		if i > 0 && (s.PID != cur.PID ||
			s.StartNanos-curStart >= targetDuration) {
			out = append(out, cur)
			cur = Segment{
				ID: len(out), PID: s.PID, StartNanos: s.StartNanos,
				CodecHint: s.CodecHint,
			}
			curStart = s.StartNanos
		}
		cur.Data = append(cur.Data, s.Data...)
	}
	out = append(out, cur)
	return out
}
