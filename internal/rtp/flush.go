package rtp

// FlushTerminal records count unrecovered gaps immediately following the
// observed high-water mark and returns their placeholder slots. These are
// end-of-stream losses the jitter window could never have concealed.
func (b *JitterBuffer) FlushTerminal(count int) []Slot {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]Slot, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, b.resolveTerminalGap(b.nextOut))
		b.nextOut++
	}
	return out
}

// resolveTerminalGap records an end-of-stream loss that the window could
// never have concealed.
func (b *JitterBuffer) resolveTerminalGap(seq uint64) Slot {
	b.stats.WindowGaps++
	b.stats.UnrecoveredPackets++
	return Slot{Sequence: seq, Class: SlotGap}
}
