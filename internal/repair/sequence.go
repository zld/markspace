// Package repair normalizes fragment sequence numbers and decode
// timestamps against the shared timeline.
package repair

// SeqDecision describes how an incoming fragment was mapped onto the
// logical, gap-free sequence of its track.
type SeqDecision struct {
	Logical     uint32
	Rebuilt     bool // mfhd gap detected; logical sequence rebuilt from input order
	Duplicate   bool // retransmission of already-received content
	Quarantined bool // duplicate mfhd with conflicting hash; isolated
}

type seenEntry struct {
	hash    [32]byte
	logical uint32
}

type trackSeq struct {
	nextLogical uint32
	lastMfhd    uint32
	hasLast     bool
	seen        map[uint32]seenEntry
}

// Sequencer assigns logical sequence numbers per track.
type Sequencer struct {
	tracks map[uint32]*trackSeq
}

// NewSequencer returns an empty sequencer.
func NewSequencer() *Sequencer { return &Sequencer{tracks: map[uint32]*trackSeq{}} }

func (s *Sequencer) track(trackID uint32) *trackSeq {
	ts := s.tracks[trackID]
	if ts == nil {
		ts = &trackSeq{seen: map[uint32]seenEntry{}}
		s.tracks[trackID] = ts
	}
	return ts
}

// Assign maps mfhd to a logical sequence number. A gap in mfhd numbers is
// rebuilt from input order and parsed durations; a retransmitted fragment
// (same mfhd, same hash) folds onto its original logical number; a
// conflicting fragment (same mfhd, different hash) is quarantined and
// never overwrites already-received data.
func (s *Sequencer) Assign(trackID, mfhd uint32, hash [32]byte) SeqDecision {
	ts := s.track(trackID)
	if prev, ok := ts.seen[mfhd]; ok {
		if prev.hash == hash {
			return SeqDecision{Logical: prev.logical, Duplicate: true}
		}
		return SeqDecision{Logical: prev.logical, Quarantined: true}
	}
	d := SeqDecision{Logical: ts.nextLogical}
	if ts.hasLast && mfhd != ts.lastMfhd+1 {
		d.Rebuilt = true
	}
	ts.seen[mfhd] = seenEntry{hash: hash, logical: d.Logical}
	ts.nextLogical++
	ts.lastMfhd = mfhd
	ts.hasLast = true
	return d
}

// Restore reinstates a previously accepted mapping when resuming from
// persisted index chunks. Calls must be made in original processing order.
func (s *Sequencer) Restore(trackID, mfhd, logical uint32, hash [32]byte) {
	ts := s.track(trackID)
	ts.seen[mfhd] = seenEntry{hash: hash, logical: logical}
	if logical >= ts.nextLogical {
		ts.nextLogical = logical + 1
	}
	ts.lastMfhd = mfhd
	ts.hasLast = true
}
