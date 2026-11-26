// Package rtpclock classifies RTP sequence-number events (duplicate,
// reordered, lost) and unwraps 32-bit timestamps onto the shared 90kHz
// timebase without silently correcting wraparound.
package rtpclock

import (
	"math"

	"github.com/pion/rtp"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
)

// EventKind classifies a received packet relative to the stream state.
type EventKind string

const (
	EventOK        EventKind = "ok"
	EventDuplicate EventKind = "duplicate"
	EventReordered EventKind = "reordered"
	EventLost      EventKind = "lost"
	EventWrap      EventKind = "timestamp_wrap"
)

// Event is the per-packet classification result.
type Event struct {
	Kind            EventKind
	Seq             uint16
	RawTimestamp    uint32
	Unwrapped       uint64 // 64-bit extension of the RTP timestamp
	Ticks           clock.Ticks
	LostCount       int // packets declared missing at this point (EventLost only)
	BackJump        int // how far backwards a reordered packet sits
	WrapAccumulated uint64
}

// Stream is a stateful per-SSRC unwrapper.
type Stream struct {
	source   string
	clockRate uint32
	baseTS   uint32
	baseSet  bool
	wrapBase uint64 // accumulated value of the 32-bit boundary before base

	lastSeq      uint16
	lastSeqSet   bool
	seenSeq      map[uint16]bool
	lastTS       uint32
	lastUnwrap   uint64
	wrapAccum    uint64
	maxForward   uint16 // highest sequence number ever accepted
}

// NewStream creates an unwrapper for one source and RTP clock rate.
func NewStream(source string, rtpClockRate uint32) (*Stream, error) {
	if rtpClockRate == 0 {
		return nil, fault.New(fault.StageRTP, fault.KindRTP, source, "zero RTP clock rate")
	}
	return &Stream{source: source, clockRate: rtpClockRate, seenSeq: map[uint16]bool{}}, nil
}

const half uint32 = 1 << 31

// Observe ingests one parsed RTP packet and returns its classification.
func (s *Stream) Observe(p *rtp.Packet) (Event, error) {
	seq := p.SequenceNumber
	ts := p.Timestamp
	ev := Event{Seq: seq, RawTimestamp: ts, WrapAccumulated: s.wrapAccum, Kind: EventOK}

	// --- sequence classification ---
	if !s.lastSeqSet {
		s.lastSeq, s.maxForward, s.lastSeqSet = seq, seq, true
		s.seenSeq[seq] = true
	} else if s.seenSeq[seq] {
		ev.Kind = EventDuplicate
	} else {
		delta := forwardDistance(s.maxForward, seq)
		if seqDistance(seq, s.maxForward) < 0 {
			ev.Kind = EventReordered
			ev.BackJump = int(forwardDistance(seq, s.maxForward))
		} else if delta > 1 {
			ev.Kind = EventLost
			ev.LostCount = int(delta - 1)
			s.maxForward = seq
		} else {
			ev.Kind = EventOK
			s.maxForward = seq
		}
		s.seenSeq[seq] = true
		s.lastSeq = seq
	}

	// --- timestamp unwrap ---
	if !s.baseSet {
		s.baseTS, s.baseSet = ts, true
		s.lastTS, s.lastUnwrap = ts, uint64(ts)
	} else {
		diff := int64(ts) - int64(s.lastTS)
		if diff < 0 && uint32(-diff) < half {
			// Backwards jump small enough to be reorder, not a wrap.
			ev.Unwrapped = s.lastUnwrap - uint64(uint32(-diff))
		} else if diff < 0 {
			// A large backwards jump is a 32-bit wraparound; recorded, not hidden.
			s.wrapAccum += uint64(1) << 32
			ev.Kind = EventWrap
			ev.Unwrapped = s.wrapAccum + uint64(ts)
			s.lastTS = ts
			s.lastUnwrap = ev.Unwrapped
		} else {
			ev.Unwrapped = s.wrapAccum + uint64(ts)
			s.lastTS = ts
			s.lastUnwrap = ev.Unwrapped
		}
	}
	if ev.Unwrapped == 0 && s.baseSet {
		ev.Unwrapped = s.wrapAccum + uint64(ts)
	}

	relative := ev.Unwrapped - s.wrapBase - uint64(s.baseTS)
	if relative > math.MaxInt64 {
		return ev, fault.New(fault.StageRTP, fault.KindOverflow, s.source,
			"unwrapped RTP timestamp overflows the tick domain")
	}
	tk, err := clock.Ticks(relative).Scale(int64(clock.Resolution), int64(s.clockRate))
	if err != nil {
		return ev, err
	}
	ev.Ticks = tk
	return ev, nil
}

// forwardDistance returns how many steps ahead cur is from ref modulo 65536.
func forwardDistance(ref, cur uint16) uint16 { return cur - ref }

// seqDistance returns a signed forward/backward distance in (-32768, 32768].
func seqDistance(cur, ref uint16) int {
	d := int(int16(cur - ref))
	return d
}
