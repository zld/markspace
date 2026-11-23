package stream

// CorruptRange delimits one damaged segment in original stream coordinates.
// The boundary values are preserved exactly as observed so later stages can
// trace damage impact without re-deriving them.
type CorruptRange struct {
	Reason   string  `json:"reason"`
	StartSeq uint16  `json:"start_seq"`
	EndSeq   uint16  `json:"end_seq"`
	StartMs  float64 `json:"start_ms"`
	EndMs    float64 `json:"end_ms"`
	Frames   int     `json:"frames"`
}

// DamageTracker tracks the currently open damaged segment and the closed
// ranges. A segment opens on the first damaging event and closes at the next
// valid keyframe.
type DamageTracker struct {
	Ranges []CorruptRange
	open   *CorruptRange
}

// Begin opens a damaged segment if none is open.
func (d *DamageTracker) Begin(reason string, seq uint16, ms float64) {
	if d.open != nil {
		return
	}
	d.open = &CorruptRange{Reason: reason, StartSeq: seq, EndSeq: seq, StartMs: ms, EndMs: ms}
}

// Extend grows the open segment to cover one more frame.
func (d *DamageTracker) Extend(seq uint16, ms float64) {
	if d.open == nil {
		return
	}
	d.open.EndSeq = seq
	d.open.EndMs = ms
	d.open.Frames++
}

// InDamage reports whether a damaged segment is currently open.
func (d *DamageTracker) InDamage() bool { return d.open != nil }

// Recover closes the open segment at a valid keyframe. The range end stays
// on the last damaged frame; the keyframe itself is healthy output.
func (d *DamageTracker) Recover() {
	if d.open == nil {
		return
	}
	d.Ranges = append(d.Ranges, *d.open)
	d.open = nil
}

// CorruptFrames returns the total number of frames inside closed ranges plus
// the frames accumulated in the open segment.
func (d *DamageTracker) CorruptFrames() int {
	n := 0
	for _, r := range d.Ranges {
		n += r.Frames
	}
	if d.open != nil {
		n += d.open.Frames
	}
	return n
}
