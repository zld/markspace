package mediakernel

// MaxSequenceJump bounds how far ahead a sequence number may jump before it is
// treated as a sequence anomaly rather than a (large) burst of losses. Normal
// 16-bit RTP wraparound produces a small modular delta and is handled fine.
const MaxSequenceJump = 512

// GapTracker records lost, duplicate and wrapped RTP sequence numbers. It is
// single-goroutine: the streaming pipeline feeds it from the reader stage.
type GapTracker struct {
	lastSeq  uint16
	hasLast  bool
	codec    Codec
	seen     map[uint16]int // arrival count per sequence number (late dup detection)
	gapSeqs  []uint16
	dupSeqs  []uint16
	wrapHits int
}

// NewGapTracker constructs an empty tracker.
func NewGapTracker() *GapTracker { return &GapTracker{seen: make(map[uint16]int)} }

// Observe classifies a newly parsed packet.
//
//	duplicate=true  : this exact sequence was already seen; callers must not
//	                  advance the media clock.
//	missing         : sequence numbers skipped since the previous packet, in
//	                  stream order across 16-bit wraparound.
//	err != nil      : a backwards jump or implausibly large jump; the error
//	                  phase is PhaseSequence and the caller attaches the offset.
func (g *GapTracker) Observe(seq uint16) (duplicate bool, missing []uint16, err error) {
	if !g.hasLast {
		g.hasLast = true
		g.lastSeq = seq
		g.seen[seq]++
		return false, nil, nil
	}
	// Any sequence already observed is a duplicate (immediate or late), even
	// when it arrived behind the current high-water mark.
	if g.seen[seq] > 0 {
		g.seen[seq]++
		g.dupSeqs = append(g.dupSeqs, seq)
		return true, nil, nil
	}
	// Modular forward distance in 1..65535 relative to the high-water mark.
	delta := int32(uint16(seq - g.lastSeq))
	switch {
	case delta < 0:
		return false, nil, NewError(PhaseSequence, 0,
			"unseen sequence number behind high-water mark").WithSeq(seq)
	case delta > MaxSequenceJump:
		return false, nil, NewError(PhaseSequence, 0,
			"sequence jump exceeds maximum").WithSeq(seq)
	}
	if seq < g.lastSeq {
		g.wrapHits++ // modular forward delta but raw value wrapped 65535 -> 0
	}
	for s := g.lastSeq + 1; s != seq; s++ {
		missing = append(missing, s)
	}
	g.gapSeqs = append(g.gapSeqs, missing...)
	g.lastSeq = seq
	g.seen[seq]++
	return false, missing, nil
}

// SetCodec records the stream codec used to size gap reservations.
func (g *GapTracker) SetCodec(c Codec) { g.codec = c }

// Codec returns the observed stream codec.
func (g *GapTracker) Codec() Codec { return g.codec }

// GapSamples estimates the missing duration of one lost sequence number. The
// estimate only reserves a time range; it never synthesizes audio content.
func (g *GapTracker) GapSamples() int64 { return DefaultGapSamples(g.codec) }

// GapCount is the total number of lost sequence numbers observed.
func (g *GapTracker) GapCount() int { return len(g.gapSeqs) }

// DuplicateCount is the total number of duplicate packets observed.
func (g *GapTracker) DuplicateCount() int { return len(g.dupSeqs) }

// WrapCount is how many times the sequence counter crossed 65535 -> 0.
func (g *GapTracker) WrapCount() int { return g.wrapHits }

// MissingSeqs returns every lost sequence number in stream order.
func (g *GapTracker) MissingSeqs() []uint16 { return append([]uint16(nil), g.gapSeqs...) }

// DuplicateSeqs returns every duplicated sequence number in arrival order.
func (g *GapTracker) DuplicateSeqs() []uint16 { return append([]uint16(nil), g.dupSeqs...) }
