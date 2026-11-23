package fixture

import "github.com/broadcast/mediad/internal/model"

// trackLayout holds the computed per-track tables shared between moov and mdat.
type trackLayout struct {
	spec       TrackSpec
	deltas     []uint32 // stts deltas in mdhd units
	sizes      []uint32
	keyframes  []uint32 // 1-based sample numbers, empty = no stss
	chunks     []chunkLayout
	totalUnits uint32
	// mdatBase is this track's first payload offset inside the global mdat.
	mdatBase uint64
}

type chunkLayout struct {
	firstSample int // 0-based
	count       int
}

// planTables computes stts/stsc/stss tables and total media duration for a
// spec, applying timestamp corruption at this layer so the moov and mdat stay
// internally consistent with the damaged timeline.
func planTables(s Spec, ts TrackSpec) trackLayout {
	l := trackLayout{spec: ts}
	l.sizes = make([]uint32, ts.SampleCount)
	for i := range l.sizes {
		l.sizes[i] = ts.SampleSize
	}
	// Default one sample per chunk; repair logic later proves offsets from it.
	for i := 0; i < ts.SampleCount; i++ {
		l.chunks = append(l.chunks, chunkLayout{firstSample: i, count: 1})
	}

	baseDelta := uint32(1000)
	if ts.Kind == model.KindAudio {
		baseDelta = uint32(ts.Timescale / 1000) // 1 ms audio frames
	}
	l.deltas = make([]uint32, ts.SampleCount)
	for i := range l.deltas {
		l.deltas[i] = baseDelta
	}
	switch s.Corruption {
	case CorruptBackwardTimestamps:
		// Force a negative jump at 2/3 of the track: stts itself cannot encode
		// negatives, so we reorder sample durations to create an overlapping
		// timeline by shrinking an earlier span and inserting a zero delta.
		j := ts.SampleCount * 2 / 3
		if j > 1 && j < ts.SampleCount {
			l.deltas[j] = 0 // duplicate decode timestamp (overlap)
			l.deltas[j-1] = baseDelta / 2
		}
	case CorruptSparseKeyframes:
		// Keyframes only at the head and tail: with a 24k timescale and 48
		// samples this produces a ~1.96s gap, so widen the delta to force a
		// >2s gap that the delivery threshold must reject.
		if ts.Kind == model.KindVideo {
			l.deltas = make([]uint32, ts.SampleCount)
			for i := range l.deltas {
				l.deltas[i] = baseDelta * 2 // ~41.7 ms per frame => ~2 s mid-gap
			}
			l.keyframes = []uint32{1, uint32(ts.SampleCount)}
		}
	}
	// Every non-sparse video fixture keeps a regular keyframe index so
	// boundary recovery stays provable even when timestamps are damaged.
	if ts.Kind == model.KindVideo && len(l.keyframes) == 0 && ts.KeyframeEvery > 0 {
		for i := 1; i <= ts.SampleCount; i += ts.KeyframeEvery {
			l.keyframes = append(l.keyframes, uint32(i))
		}
		if l.keyframes[len(l.keyframes)-1] != uint32(ts.SampleCount) {
			l.keyframes = append(l.keyframes, uint32(ts.SampleCount))
		}
	}
	if s.Corruption == CorruptSparseKeyframes && ts.Kind != model.KindVideo {
		// audio tracks keep no stss in any variant
		l.keyframes = nil
	}

	var total uint64
	total += uint64(ts.StartUnits)
	for _, d := range l.deltas {
		total += uint64(d)
	}
	// Cap to uint32: fixtures are tiny, but guard anyway.
	if total > uint64(^uint32(0)) {
		total = uint64(^uint32(0))
	}
	l.totalUnits = uint32(total)
	return l
}
