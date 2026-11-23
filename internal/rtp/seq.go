package rtp

import (
	"sort"

	"mediatool/internal/fault"
)

// ConfirmWindow is the default number of sequence numbers that must pass
// before an unseen sequence number is declared lost.
const ConfirmWindow = 64

// seqTracker confirms losses, reorders, and duplicates for one track.
//
// It remembers at most window observed/pending sequence numbers, so memory is
// O(window) regardless of stream length.
type seqTracker struct {
	trackID      uint32
	window       uint64
	have         bool
	largest      uint64
	largestRaw   uint16
	lastOffset   int64
	seen         map[uint64]bool
	pending      map[uint64]bool
	nextExpected uint64
}

func newSeqTracker(window int) *seqTracker {
	if window <= 0 {
		window = ConfirmWindow
	}
	return &seqTracker{
		window:  uint64(window),
		seen:    make(map[uint64]bool),
		pending: make(map[uint64]bool),
	}
}

// extend turns a 16-bit sequence number into an extended 64-bit value using a
// signed modular distance from the highest value seen, which tolerates
// reordering across the 16-bit wrap.
func (t *seqTracker) extend(seq uint16) uint64 {
	if !t.have {
		return uint64(seq)
	}
	delta := int64(int16(seq - t.largestRaw))
	return uint64(int64(t.largest) + delta)
}

func (t *seqTracker) observe(seq uint16, offset int64, window int) ([]*fault.Fault, uint64) {
	if window > 0 {
		t.window = uint64(window)
	}
	if t.window > 1<<20 {
		t.window = 1 << 20
	}
	ext := t.extend(seq)
	t.lastOffset = offset
	if !t.have {
		t.have = true
		t.largest = ext
		t.largestRaw = seq
		t.seen[ext] = true
		t.nextExpected = ext + 1
		return nil, ext
	}

	var faults []*fault.Fault
	switch {
	case ext > t.largest:
		gap := ext - t.largest
		if gap > 8*t.window {
			// A jump this large is not a reorder window's worth of loss; record
			// one located loss and resync rather than allocating per-number.
			faults = append(faults, (&fault.Fault{
				Kind: fault.KindLoss, TrackID: t.trackID, Offset: offset,
				Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged,
			}).WithDetail("%d sequence numbers missing before %d", gap-1, ext))
			t.pending = make(map[uint64]bool)
		} else {
			for s := t.largest + 1; s < ext; s++ {
				if !t.seen[s] {
					t.pending[s] = true
				}
			}
		}
		t.largest = ext
		t.largestRaw = seq
		t.seen[ext] = true
		faults = append(faults, t.confirmLocked(ext, offset)...)
		t.prune()
	case t.seen[ext]:
		faults = append(faults, (&fault.Fault{
			Kind: fault.KindDuplicate, TrackID: t.trackID, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityWarning,
		}).WithDetail("duplicate sequence %d (extended %d)", seq, ext))
	default:
		t.seen[ext] = true
		faults = append(faults, (&fault.Fault{
			Kind: fault.KindReordered, TrackID: t.trackID, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityWarning,
		}).WithDetail("sequence %d (extended %d) arrived after %d", seq, ext, t.largest))
		// A reordered packet filled a gap; it was not lost after all.
		delete(t.pending, ext)
	}
	return faults, ext
}

// confirmLocked reports gaps more than window behind the highest sequence
// number, where no reordered packet can still fill them.
func (t *seqTracker) confirmLocked(highest uint64, offset int64) []*fault.Fault {
	if highest <= t.window {
		return nil
	}
	cutoff := highest - t.window
	var lost []uint64
	for s := range t.pending {
		if s < cutoff {
			lost = append(lost, s)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	sort.Slice(lost, func(i, j int) bool { return lost[i] < lost[j] })
	out := make([]*fault.Fault, 0, len(lost))
	for _, s := range lost {
		delete(t.pending, s)
		out = append(out, (&fault.Fault{
			Kind: fault.KindLoss, TrackID: t.trackID, Offset: offset,
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged,
		}).WithDetail("sequence %d never arrived before %d", s, highest))
	}
	return out
}

func (t *seqTracker) prune() {
	if t.largest <= t.window {
		return
	}
	floor := t.largest - t.window
	for s := range t.seen {
		if s < floor {
			delete(t.seen, s)
		}
	}
	for s := range t.pending {
		if s < floor {
			delete(t.pending, s)
		}
	}
}

// confirmAll drains every outstanding gap at end of stream, where no reordered
// packet can still arrive.
func (t *seqTracker) confirmAll() []*fault.Fault {
	var lost []uint64
	for s := range t.pending {
		lost = append(lost, s)
	}
	sort.Slice(lost, func(i, j int) bool { return lost[i] < lost[j] })
	out := make([]*fault.Fault, 0, len(lost))
	for _, s := range lost {
		out = append(out, (&fault.Fault{
			Kind: fault.KindLoss, TrackID: t.trackID, Offset: t.lastOffset,
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged,
		}).WithDetail("sequence %d missing at end of stream", s))
	}
	t.pending = make(map[uint64]bool)
	return out
}
