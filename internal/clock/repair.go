package clock

import "sort"

// RepairReason explains why a timestamp was (or was not) repaired.
type RepairReason string

const (
	ReasonOK        RepairReason = "ok"         // original value kept
	ReasonWrap      RepairReason = "wrap"       // 32-bit timestamp wraparound extended
	ReasonBackwards RepairReason = "backwards"  // timestamp moved backwards, re-based
	ReasonJump      RepairReason = "jump"       // discontinuity larger than tolerance, gap kept
	ReasonDrift     RepairReason = "drift"      // small capture-clock drift corrected to nominal step
	ReasonDuplicate RepairReason = "duplicate"  // repeated unit, repaired value repeats
	ReasonReordered RepairReason = "reordered" // out-of-order arrival re-sequenced
)

// TimeEntry is one timestamp observation before and after repair.
// Original values are always retained so the repair decision is auditable.
type TimeEntry struct {
	Index       int          // arrival ordinal
	Seq         int64        // extended 16-bit sequence number
	Original    Tick         // extended but otherwise raw 90 kHz timestamp
	Repaired    Tick         // monotonically non-decreasing repaired timestamp
	NominalStep Tick         // expected per-unit clock step (0 if unknown)
	Reason      RepairReason
	DriftTicks  int64 // signed observed-minus-nominal delta when ReasonDrift
}

// Repairer turns a stream of (sequence, raw 32-bit timestamp) observations
// into a monotonic repaired timeline. It is deterministic: feeding the same
// observations twice yields byte-identical entries and digests.
type Repairer struct {
	nominalStep Tick
	jumpTicks   Tick // |delta-nominal| above this is a real discontinuity
	entries     []TimeEntry
	lastSeq     int64
	lastRaw32   Tick32
	lastRaw     Tick
	lastFixed   Tick
	started     bool
	seqSeen     map[int64]int // extended seq -> first entry index
}

// NewRepairer builds a repairer. nominalStep is the expected tick increment
// per unit (e.g. 900 for 10 ms packets); jumpLimit declares how far a delta
// may deviate from nominal before it is treated as a genuine gap.
func NewRepairer(nominalStep, jumpLimit Tick) (*Repairer, error) {
	if nominalStep < 0 || jumpLimit < 0 {
		return nil, NewError(ErrInvalidArgument, "repairer parameters must be non-negative")
	}
	return &Repairer{
		nominalStep: nominalStep,
		jumpTicks:   jumpLimit,
		seqSeen:     make(map[int64]int),
	}, nil
}

// Observe ingests one packet in arrival order. rawSeq/rawTS are the original
// 16/32-bit wire values; the repaired timeline is produced inside Observe.
func (r *Repairer) Observe(rawSeq Seq16, rawTS Tick32) TimeEntry {
	idx := len(r.entries)
	if !r.started {
		r.started = true
		r.lastSeq = ExtendSeq16(0, rawSeq)
		r.lastRaw = ExtendTick32(0, rawTS)
		r.lastRaw32, r.lastFixed = rawTS, r.lastRaw
		r.seqSeen[r.lastSeq] = idx
		e := TimeEntry{Index: idx, Seq: r.lastSeq, Original: r.lastRaw,
			Repaired: r.lastFixed, NominalStep: r.nominalStep, Reason: ReasonOK}
		r.entries = append(r.entries, e)
		return e
	}

	seq := ExtendSeq16(r.lastSeq, rawSeq)
	ts := ExtendTick32(r.lastRaw, rawTS)
	e := TimeEntry{Index: idx, Seq: seq, Original: ts, NominalStep: r.nominalStep}

	if first, ok := r.seqSeen[seq]; ok {
		// Duplicate sequence number: repeat the first-sight position.
		e.Reason, e.Repaired = ReasonDuplicate, r.entries[first].Repaired
	} else if seq < r.lastSeq {
		// Reordered arrival of an as-yet-unseen older packet: anchor it on
		// the nearest known neighbour using the nominal step.
		e = r.placeReordered(e, seq)
		r.seqSeen[seq] = idx
		r.entries = append(r.entries, e)
		return e
	} else {
		e = r.advance(e, seq, rawTS, ts)
	}

	r.seqSeen[seq] = idx
	if seq > r.lastSeq {
		r.lastSeq = seq
	}
	r.lastRaw, r.lastRaw32 = ts, rawTS
	r.entries = append(r.entries, e)
	return e
}

// placeReordered positions an unseen older sequence between known entries.
func (r *Repairer) placeReordered(e TimeEntry, seq int64) TimeEntry {
	var lower *TimeEntry
	for i := range r.entries {
		if r.entries[i].Seq <= seq && (lower == nil || r.entries[i].Seq > lower.Seq) {
			lower = &r.entries[i]
		}
	}
	e.Reason = ReasonReordered
	if lower != nil && r.nominalStep > 0 {
		e.Repaired = lower.Repaired + r.nominalStep*Tick(seq-lower.Seq)
	} else if lower != nil {
		e.Repaired = lower.Repaired
	} else if r.nominalStep > 0 {
		e.Repaired = r.lastFixed + r.nominalStep*Tick(seq-r.lastSeq)
	} else {
		e.Repaired = r.lastFixed
	}
	return e
}

// advance handles a genuinely newer sequence number.
func (r *Repairer) advance(e TimeEntry, seq int64, rawTS Tick32, ts Tick) TimeEntry {
	gapPackets := seq - r.lastSeq
	nominal := r.nominalStep * Tick(gapPackets)
	delta := ts - r.lastRaw
	modDelta := Tick(int32(rawTS - r.lastRaw32)) // signed 32-bit modular delta
	wrapped := modDelta > 0 && rawTS < r.lastRaw32
	e.Seq = seq

	switch {
	case modDelta < 0:
		// Clock really moved backwards (modular space says older).
		e.Reason = ReasonBackwards
		e.Repaired = r.lastFixed + maxTick(nominal, r.nominalStep)
	case wrapped:
		// 32-bit counter rolled over. Snap drift, otherwise keep exact delta.
		e.Reason = ReasonWrap
		e.Repaired = r.snap(ts, delta, nominal, false)
	case r.nominalStep > 0 && gapPackets > 0 && delta != nominal &&
		abs64(int64(delta-nominal)) <= int64(r.jumpTicks):
		// Small capture-clock drift: correct onto the nominal grid.
		e.DriftTicks = int64(delta - nominal)
		e.Reason, e.Repaired = ReasonDrift, r.lastFixed+nominal
	case r.nominalStep > 0 && abs64(int64(delta-nominal)) > int64(r.jumpTicks):
		// True discontinuity: preserve the gap, never invent media time.
		e.Reason, e.Repaired = ReasonJump, r.lastFixed+delta
	default:
		e.Reason, e.Repaired = ReasonOK, r.lastFixed+delta
	}
	r.lastFixed = e.Repaired
	return e
}

// snap returns a repaired increment, applying the nominal grid when the
// observed deviation is within the drift tolerance.
func (r *Repairer) snap(_ Tick, delta, nominal Tick, _ bool) Tick {
	if r.nominalStep > 0 && abs64(int64(delta-nominal)) <= int64(r.jumpTicks) {
		return r.lastFixed + nominal
	}
	return r.lastFixed + delta
}

// Entries returns the full repaired timeline.
func (r *Repairer) Entries() []TimeEntry { return r.entries }

// LastRepaired returns the most recent repaired tick.
func (r *Repairer) LastRepaired() Tick { return r.lastFixed }

// CountByReason returns how many entries carry each repair reason.
func (r *Repairer) CountByReason() map[RepairReason]int {
	out := make(map[RepairReason]int)
	for _, e := range r.entries {
		out[e.Reason]++
	}
	return out
}

// IsMonotonic verifies the repaired timeline never moves backwards when
// entries are read in sequence-number order (arrival order may contain late
// reordered packets, which are legitimately below later arrivals).
func (r *Repairer) IsMonotonic() bool {
	ordered := make([]TimeEntry, len(r.entries))
	copy(ordered, r.entries)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Repaired < ordered[i-1].Repaired {
			return false
		}
	}
	return true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxTick(a, b Tick) Tick {
	if a >= b {
		return a
	}
	return b
}
