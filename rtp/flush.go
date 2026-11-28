package rtp

import (
	"sort"
)

// Flush ends the stream: every remaining position up to the highest seen is
// emitted in order, with still-missing positions reported as gaps.
func (w *Window) Flush() Release {
	w.mu.Lock()
	defer w.mu.Unlock()
	var release Release
	if !w.started {
		return release
	}
	// Walk the ordered positions, filling holes with gaps.
	var runStart int64 = -1
	emitGap := func(end int64) {
		if runStart >= 0 && end > runStart {
			gap := Gap{StartSeq: uint64(runStart), Count: uint64(end - runStart),
				DurationNS: (end - runStart) * w.cfg.PacketDurationNS}
			release.Gaps = append(release.Gaps, gap)
			w.stats.PacketsLost += gap.Count
			runStart = -1
		}
	}
	for seq := w.floor; seq <= w.highest; seq++ {
		if idxGap, known := w.gapPending[seq]; known {
			emitGap(seq)
			release.Gaps = append(release.Gaps, idxGap)
			w.stats.PacketsLost += idxGap.Count
			continue
		}
		if frame, ok := w.pending[seq]; ok {
			emitGap(seq)
			release.Frames = append(release.Frames, *frame)
			continue
		}
		if runStart < 0 {
			runStart = seq
		}
	}
	emitGap(w.highest + 1)
	// Anything above the high-water mark still resident is emitted too.
	keys := make([]int64, 0, len(w.pending))
	for seq := range w.pending {
		if seq > w.highest {
			keys = append(keys, seq)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, seq := range keys {
		release.Frames = append(release.Frames, *w.pending[seq])
	}
	w.floor = w.highest + 1
	w.pending = make(map[int64]*Frame, w.cfg.Depth*2+1)
	w.seen = make(map[int64]struct{}, w.cfg.Depth*4+4)
	w.gapPending = make(map[int64]Gap)
	return release
}
