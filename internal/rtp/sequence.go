package rtp

// Disposition describes sequence classification.
type Disposition int

const (
	DispositionInvalid Disposition = iota
	DispositionNew
	DispositionDuplicate
	DispositionOutOfOrder
)

// SequenceReport is the result of inspecting one RTP sequence number.
type SequenceReport struct {
	Sequence    uint16
	ExtSequence uint64
	Kind        Disposition
	Missing     []uint64
}

// Unwrap16 extends a 16-bit RTP sequence number around an extended reference.
func Unwrap16(reference uint64, seq uint16) uint64 {
	delta := int16(seq - uint16(reference))
	return uint64(int64(reference) + int64(delta))
}

// Tracker unwraps 16-bit RTP sequence numbers and remembers seen values.
type Tracker struct {
	SSRC    uint32
	Started bool
	MaxExt  uint64
	seen    map[uint64]struct{}
}

// NewTracker creates a sequence tracker for an SSRC.
func NewTracker(ssrc uint32) *Tracker {
	return &Tracker{SSRC: ssrc, seen: make(map[uint64]struct{})}
}

// Accept classifies seq and updates the tracker.
func (t *Tracker) Accept(seq uint16) SequenceReport {
	if !t.Started {
		ext := uint64(seq)
		t.Started = true
		t.MaxExt = ext
		t.seen[ext] = struct{}{}
		return SequenceReport{Sequence: seq, ExtSequence: ext, Kind: DispositionNew}
	}

	ext := Unwrap16(t.MaxExt, seq)
	_, isSeen := t.seen[ext]
	report := SequenceReport{Sequence: seq, ExtSequence: ext}

	switch {
	case isSeen:
		report.Kind = DispositionDuplicate
	case ext < t.MaxExt:
		report.Kind = DispositionOutOfOrder
		t.seen[ext] = struct{}{}
	case ext == t.MaxExt:
		// Defensive: MaxExt should always be marked seen.
		report.Kind = DispositionDuplicate
	default:
		report.Kind = DispositionNew
		for missing := t.MaxExt + 1; missing < ext; missing++ {
			report.Missing = append(report.Missing, missing)
		}
		t.MaxExt = ext
		t.seen[ext] = struct{}{}
	}
	if ext > t.MaxExt {
		t.MaxExt = ext
	}
	return report
}

// Snapshot returns the maximum sequence and all seen extended sequences.
func (t *Tracker) Snapshot() (max uint64, seen []uint64) {
	seen = make([]uint64, 0, len(t.seen))
	for seq := range t.seen {
		seen = append(seen, seq)
	}
	return t.MaxExt, seen
}

// Restore replaces checkpointed tracker state.
func (t *Tracker) Restore(max uint64, seen []uint64) {
	t.Started = true
	t.MaxExt = max
	t.seen = make(map[uint64]struct{}, len(seen))
	for _, seq := range seen {
		t.seen[seq] = struct{}{}
	}
}

// MarkSeen restores checkpoint state. It does not produce gap reports.
func (t *Tracker) MarkSeen(ext uint64) {
	t.Started = true
	t.seen[ext] = struct{}{}
	if ext > t.MaxExt {
		t.MaxExt = ext
	}
}

// Seen reports whether an extended sequence has already been accepted.
func (t *Tracker) Seen(ext uint64) bool {
	_, ok := t.seen[ext]
	return ok
}
