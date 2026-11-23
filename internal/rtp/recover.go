package rtp

import (
	"github.com/pion/rtp"

	"mediaarchive/internal/timeline"
)

// RecoveredStream is the ordered, loss-annotated result of running a raw
// arrival-order packet capture through the jitter buffer.
type RecoveredStream struct {
	Slots        []Slot
	Stats        Stats
	TimeNanos    []int64 // parallel to Slots, stream-relative nanoseconds
	FirstSeq     uint64
	LastSeq      uint64
	Expected     int // sequence span, inclusive
}

// Recover feeds arrival-order packets through a JitterBuffer and returns the
// fully ordered stream after flushing. clockRate is the RTP timestamp rate
// (e.g. 90000 for video) used to map timestamps to nanoseconds.
func Recover(packets []*rtp.Packet, cfg Config, clockRate uint64) (*RecoveredStream, error) {
	buf := NewJitterBuffer(cfg)
	for _, p := range packets {
		if err := buf.Push(p); err != nil {
			return nil, err
		}
	}
	slots := append(buf.Pop(), buf.Flush()...)

	tb := timeline.TimeBase{ClockRate: clockRate}
	tsClock := timeline.NewWrapClock(32)
	out := &RecoveredStream{Slots: slots, Stats: buf.Stats()}
	if len(slots) == 0 {
		return out, nil
	}
	out.FirstSeq = slots[0].Sequence
	out.LastSeq = slots[len(slots)-1].Sequence
	out.Expected = int(out.LastSeq-out.FirstSeq) + 1

	var originRaw uint64
	haveOrigin := false
	for _, s := range slots {
		if s.Packet != nil {
			raw := tsClock.Unwrap(uint64(s.Packet.Timestamp))
			if !haveOrigin {
				originRaw = raw
				haveOrigin = true
			}
			ns, err := tb.ToNanos(raw - originRaw)
			if err != nil {
				return nil, err
			}
			out.TimeNanos = append(out.TimeNanos, ns)
		} else {
			// Placeholder slots inherit the previous timestamp; the merger
			// treats them as loss markers, not as new media time.
			prev := int64(0)
			if n := len(out.TimeNanos); n > 0 {
				prev = out.TimeNanos[n-1]
			}
			out.TimeNanos = append(out.TimeNanos, prev)
		}
	}
	return out, nil
}

// MissingSequenceNumbers returns the unwrapped sequence numbers classified
// as unrecovered gaps, convenient for manifest output and cross-stage checks.
func (s *RecoveredStream) MissingSequenceNumbers() []uint64 {
	var missing []uint64
	for _, slot := range s.Slots {
		if slot.Class == SlotGap {
			missing = append(missing, slot.Sequence)
		}
	}
	return missing
}

// IntegrityError returns ErrGap when unrecovered packets remain, so callers
// can fold RTP loss into the unified error taxonomy without losing counters.
func (s *RecoveredStream) IntegrityError() error {
	if s.Stats.UnrecoveredPackets == 0 {
		return nil
	}
	return timeline.NewError("rtp", "integrity", -1, timeline.RecoverPartial,
		"unrecovered packet gaps survived the jitter window",
		timeline.ErrGap)
}
