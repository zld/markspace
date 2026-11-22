package rtp

import (
	"errors"
	"fmt"
)

// Recovery limits.
const (
	// MaxGapPackets bounds the size of one recoverable gap.
	MaxGapPackets = 512
	// MaxOutstandingGaps bounds simultaneous recovery state.
	MaxOutstandingGaps = 256
	// MaxRecoveryAttempts caps retransmission rounds per gap.
	MaxRecoveryAttempts = 8
)

var (
	// ErrGapTooLarge reports a gap beyond MaxGapPackets.
	ErrGapTooLarge = errors.New("rtp: gap exceeds recovery limit")
	// ErrTooManyGaps reports exhaustion of outstanding gap slots.
	ErrTooManyGaps = errors.New("rtp: too many outstanding gaps")
	// ErrInvalidPolicy reports a nonsensical retransmission policy.
	ErrInvalidPolicy = errors.New("rtp: invalid recovery policy")
	// ErrUnknownSequence reports a retransmission outside every open gap.
	ErrUnknownSequence = errors.New("rtp: retransmission does not fill an open gap")
)

// Policy bounds the retransmission wait. WaitTicks is measured in the unified
// 90 kHz tick line driven by the caller (packet arrivals advance virtual time).
type Policy struct {
	MaxAttempts int
	WaitTicks   int64
	Now         int64
}

// Validate checks policy bounds.
func (p Policy) Validate() error {
	if p.MaxAttempts < 1 || p.MaxAttempts > MaxRecoveryAttempts {
		return fmt.Errorf("%w: attempts %d not in [1,%d]", ErrInvalidPolicy, p.MaxAttempts, MaxRecoveryAttempts)
	}
	if p.WaitTicks <= 0 || p.WaitTicks > 90*90000 {
		return fmt.Errorf("%w: wait %d ticks", ErrInvalidPolicy, p.WaitTicks)
	}
	if p.Now < 0 {
		return fmt.Errorf("%w: negative start", ErrInvalidPolicy)
	}
	return nil
}

// GapState is the lifecycle state of one missing sequence range.
type GapState int

const (
	// GapPending is waiting for retransmission.
	GapPending GapState = iota
	// GapRecovered means every sequence in the range was retransmitted.
	GapRecovered
	// GapLost means the recovery wait expired; the missing state propagates.
	GapLost
)

func (s GapState) String() string {
	switch s {
	case GapPending:
		return "pending"
	case GapRecovered:
		return "recovered"
	case GapLost:
		return "lost"
	default:
		return "unknown"
	}
}

// GapRecord is the auditable record of one missing sequence range. FirstTS and
// LastTS are the timestamps of the neighbouring received packets, used to
// bound the missing span in media time without fabricating samples.
type GapRecord struct {
	Range        SeqRange
	FirstTS      uint32
	LastTS       uint32
	UnifiedStart int64
	RegisteredAt int64
	Deadline     int64
	Attempts     int
	Remaining    int
	Recovered    int
	RecoveryMask []bool // per-sequence fill state, allocated on first retransmit
	State        GapState
}

// RecoveryRequest asks the sender to retransmit an inclusive sequence range.
type RecoveryRequest struct {
	Range    SeqRange
	Attempt  int
	Deadline int64
}

// RecoveryStats are the transport recovery counters for the demo summary.
type RecoveryStats struct {
	Requests        uint64
	RequestedPacket uint64
	RecoveredPacket uint64
	RejectedFrag    uint64
	DuplicateFrag   uint64
	UnrecoveredGaps uint64
	UnrecoveredPkts uint64
	ExpiredGaps     uint64
}

// Recovery tracks sequence gaps, emits bounded retransmission requests and
// records the final missing state. It never interpolates or invents packets.
type Recovery struct {
	policy Policy
	now    int64
	gaps   []*GapRecord
	stats  RecoveryStats
}

// NewRecovery validates the policy and returns an empty tracker.
func NewRecovery(p Policy) (*Recovery, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &Recovery{policy: p, now: p.Now}, nil
}

// Now returns the current virtual time in unified ticks.
func (r *Recovery) Now() int64 { return r.now }

// Advance moves virtual time forward, never backwards.
func (r *Recovery) Advance(ticks int64) {
	if ticks <= 0 {
		return
	}
	r.now += ticks
}

// AdvanceTo moves virtual time to at least now.
func (r *Recovery) AdvanceTo(now int64) {
	if now > r.now {
		r.now = now
	}
}

// Register opens a gap record and schedules its first retransmission attempt.
// firstTS and lastTS delimit the neighbouring media timestamps.
func (r *Recovery) Register(rng SeqRange, firstTS, lastTS uint32, unifiedStart int64) (*GapRecord, error) {
	if rng.Count < 1 || rng.Count > MaxGapPackets {
		return nil, fmt.Errorf("%w: %d packets", ErrGapTooLarge, rng.Count)
	}
	if len(r.gaps) >= MaxOutstandingGaps {
		return nil, fmt.Errorf("%w: %d open", ErrTooManyGaps, len(r.gaps))
	}
	for _, g := range r.gaps {
		if g.State == GapPending && rangesOverlap(g.Range, rng) {
			return nil, fmt.Errorf("%w: overlaps %s", ErrGapTooLarge, g.Range)
		}
	}
	g := &GapRecord{
		Range:        rng,
		FirstTS:      firstTS,
		LastTS:       lastTS,
		UnifiedStart: unifiedStart,
		RegisteredAt: r.now,
		Deadline:     r.now + r.policy.WaitTicks,
		Remaining:    rng.Count,
		State:        GapPending,
	}
	r.gaps = append(r.gaps, g)
	return g, nil
}

func rangesOverlap(a, b SeqRange) bool {
	if a.Wrapped || b.Wrapped {
		return true // conservative for wrapped ranges
	}
	return a.Start <= b.End && b.Start <= a.End
}

// Due returns retransmission requests that are due now. Each call consumes one
// attempt per gap; a gap emits at most MaxAttempts requests in total.
func (r *Recovery) Due() []RecoveryRequest {
	var out []RecoveryRequest
	for _, g := range r.gaps {
		if g.State != GapPending || r.now > g.Deadline || g.Attempts >= r.policy.MaxAttempts {
			continue
		}
		g.Attempts++
		r.stats.Requests++
		r.stats.RequestedPacket += uint64(g.Remaining)
		out = append(out, RecoveryRequest{Range: g.Range, Attempt: g.Attempts, Deadline: g.Deadline})
	}
	return out
}

// Offer accepts a retransmitted packet. The caller must have already validated
// the payload integrity. It returns true when the packet filled an open gap.
func (r *Recovery) Offer(p Packet) (bool, error) {
	for _, g := range r.gaps {
		if g.State != GapPending {
			continue
		}
		// compare modulo 16 bits so wrapped ranges work naturally
		if !seqWithin(p.Seq, g.Range.Start, g.Range.Count) {
			continue
		}
		if g.RecoveryMask == nil {
			g.RecoveryMask = make([]bool, g.Range.Count)
		}
		idx := seqOffset(p.Seq, g.Range.Start)
		if g.RecoveryMask[idx] {
			r.stats.DuplicateFrag++
			return false, nil
		}
		g.RecoveryMask[idx] = true
		g.Remaining--
		g.Recovered++
		r.stats.RecoveredPacket++
		if g.Remaining == 0 {
			g.State = GapRecovered
		}
		return true, nil
	}
	r.stats.RejectedFrag++
	return false, ErrUnknownSequence
}

func seqOffset(seq, start uint16) int {
	return int(uint16(seq - start))
}

func seqWithin(seq, start uint16, count int) bool {
	return seqOffset(seq, start) < count
}

// Expire closes gaps whose retransmission wait is over. Lost gaps are returned
// once and their missing state is kept for the sync subsystem.
func (r *Recovery) Expire() []GapRecord {
	var lost []GapRecord
	for _, g := range r.gaps {
		if g.State != GapPending || r.now <= g.Deadline {
			continue
		}
		g.State = GapLost
		r.stats.ExpiredGaps++
		r.stats.UnrecoveredGaps++
		r.stats.UnrecoveredPkts += uint64(g.Remaining)
		lost = append(lost, *g)
	}
	return lost
}

// Lost returns every gap that expired unresolved.
func (r *Recovery) Lost() []GapRecord {
	var out []GapRecord
	for _, g := range r.gaps {
		if g.State == GapLost {
			out = append(out, *g)
		}
	}
	return out
}

// Open returns pending gaps.
func (r *Recovery) Open() []GapRecord {
	var out []GapRecord
	for _, g := range r.gaps {
		if g.State == GapPending {
			out = append(out, *g)
		}
	}
	return out
}

// Stats returns a snapshot of recovery counters.
func (r *Recovery) Stats() RecoveryStats { return r.stats }

// MissingRange is the missing-state contract handed to the sync subsystem. It
// carries identity and time bounds only; there are no sample bytes.
type MissingRange struct {
	Range        SeqRange
	FirstTS      uint32
	LastTS       uint32
	UnifiedStart int64
	Packets      int
}

// MissingState projects lost gaps into the form consumed by alignment.
func (r *Recovery) MissingState() []MissingRange {
	var out []MissingRange
	for _, g := range r.gaps {
		if g.State != GapLost {
			continue
		}
		out = append(out, MissingRange{
			Range:        g.Range,
			FirstTS:      g.FirstTS,
			LastTS:       g.LastTS,
			UnifiedStart: g.UnifiedStart,
			Packets:      g.Remaining,
		})
	}
	return out
}
