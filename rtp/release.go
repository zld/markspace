package rtp

// drainLocked releases the contiguous run at the floor and then enforces the
// depth bound by giving up on unordered positions.
func (w *Window) drainLocked(release *Release) {
	for {
		w.releaseContiguous(release)
		if len(w.pending)+len(w.gapPending) <= w.cfg.Depth {
			return
		}
		// The window is full: skip to the lowest resident position, declaring
		// everything between the floor and it lost.
		next := w.lowestResident()
		if next < 0 || next <= w.floor {
			return
		}
		gap := Gap{StartSeq: uint64(w.floor), Count: uint64(next - w.floor), DurationNS: (next - w.floor) * w.cfg.PacketDurationNS}
		release.Gaps = append(release.Gaps, gap)
		w.stats.PacketsLost += gap.Count
		w.floor = next
	}
}

// lowestResident returns the smallest pending or known-gap sequence, or -1.
func (w *Window) lowestResident() int64 {
	lowest := int64(-1)
	for seq := range w.pending {
		if seq >= w.floor && (lowest < 0 || seq < lowest) {
			lowest = seq
		}
	}
	for seq := range w.gapPending {
		if seq >= w.floor && (lowest < 0 || seq < lowest) {
			lowest = seq
		}
	}
	return lowest
}

// releaseContiguous emits pending frames and known gaps starting at the floor.
func (w *Window) releaseContiguous(release *Release) {
	for {
		if gap, ok := w.gapPending[w.floor]; ok {
			delete(w.gapPending, w.floor)
			release.Gaps = append(release.Gaps, gap)
			w.stats.PacketsLost += gap.Count
			w.floor++
			continue
		}
		frame, ok := w.pending[w.floor]
		if !ok {
			return
		}
		delete(w.pending, w.floor)
		w.floor++
		release.Frames = append(release.Frames, *frame)
		w.prevTS = frame.RTPTimestamp
		w.prevSeq = int64(frame.ExtendedSeq)
		w.havePrev = true
	}
}
