package relay

// SeqEvent classifies an observed RTP sequence number.
type SeqEvent int

const (
	SeqNew SeqEvent = iota
	SeqDuplicate
	SeqReordered
)

// SequenceTracker counts duplicate and out-of-order RTP packets using
// extended (32-bit) sequence numbers so 16-bit wraparound is handled.
type SequenceTracker struct {
	cycles uint32
	maxSeq uint16
	maxExt uint32
	seen   map[uint32]struct{}
	init   bool
	dups   uint64
	reord  uint64
}

// NewSequenceTracker creates a tracker with no observed packets.
func NewSequenceTracker() *SequenceTracker {
	return &SequenceTracker{seen: make(map[uint32]struct{})}
}

// Observe records one sequence number and classifies it.
func (t *SequenceTracker) Observe(seq uint16) SeqEvent {
	if !t.init {
		t.init = true
		t.maxSeq = seq
		t.maxExt = uint32(seq)
		t.seen[t.maxExt] = struct{}{}
		return SeqNew
	}
	// Extend to 32 bits: a forward jump past the 16-bit boundary bumps the
	// cycle counter (RFC 3550 appendix A.1, simplified for local links).
	if seq < t.maxSeq && t.maxSeq-seq > 30000 {
		t.cycles += 1 << 16
	}
	ext := t.cycles | uint32(seq)
	if ext > t.maxExt {
		t.maxSeq = seq
		t.maxExt = ext
		t.seen[ext] = struct{}{}
		return SeqNew
	}
	// Late packet: either a retransmit of one we already counted, or an
	// out-of-order arrival we have not seen yet.
	if _, ok := t.seen[ext]; ok {
		t.dups++
		return SeqDuplicate
	}
	t.seen[ext] = struct{}{}
	t.reord++
	return SeqReordered
}

// Duplicates returns the count of packets observed more than once.
func (t *SequenceTracker) Duplicates() uint64 { return t.dups }

// Reordered returns the count of late-but-first-time packets.
func (t *SequenceTracker) Reordered() uint64 { return t.reord }
