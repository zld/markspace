// Package recovery closes audio gaps that the jitter buffer has sealed.
//
// Recovery never fabricates image data. For audio it may insert an explicit
// silence unit so the timeline keeps its duration and downstream muxing stays
// sample accurate. A gap larger than the policy threshold is not filled; the
// fragment is marked incomplete instead and the decision is recorded with the
// sequence range and source offsets.
package recovery

import (
	"fmt"

	"mediarun/internal/fault"
	rtpparse "mediarun/internal/rtp"
)

// Policy controls which gaps may be filled.
type Policy struct {
	// MaxGapPackets is the largest gap, in missing packets, that can be
	// concealed. Larger gaps mark the fragment incomplete.
	MaxGapPackets int
	// MaxGapEvents bounds how many incomplete markers one fragment records.
	MaxGapEvents int
	// NominalTickSpan is the fallback RTP tick duration of one packet when
	// neighbours cannot supply it (e.g. a truncated stream).
	NominalTickSpan int64
}

// DefaultPolicy is tuned for 20 ms Opus packets: up to three lost packets
// (60 ms) are concealed, anything longer is surfaced as an incomplete
// fragment.
func DefaultPolicy() Policy {
	return Policy{MaxGapPackets: 3, MaxGapEvents: 64, NominalTickSpan: 960}
}

func (p Policy) validate() error {
	if p.MaxGapPackets < 0 || p.MaxGapEvents < 0 || p.NominalTickSpan <= 0 {
		return fmt.Errorf("invalid recovery policy %+v", p)
	}
	return nil
}

// Unit is one audio timeline slot: a real packet or a concealment slot.
type Unit struct {
	Seq          uint64
	RTPTimestamp uint32
	TickSpan     int64 // RTP ticks covered by the slot
	Packet       *rtpparse.Packet
	Silence      bool
	Gap          bool // true when this unit represents a recovered hole
}

// Report is the outcome for one fragment.
type Report struct {
	Units       []Unit
	Recovered   int // silence slots inserted
	GapEvents   int // distinct holes examined
	Incomplete  bool
	Unrecovered int // packets that could not be concealed
	Faults      fault.List
}

// Recover walks sequence-ordered packets and seals holes below the highest
// sequence. The caller must have flushed the jitter buffer so every remaining
// hole is final.
func Recover(ordered []*rtpparse.Packet, policy Policy) (Report, error) {
	if err := policy.validate(); err != nil {
		return Report{}, fault.New(fault.StageRecovery, fault.KindConfig, "%v", err)
	}
	var rep Report
	if len(ordered) == 0 {
		return rep, nil
	}
	for i, cur := range ordered {
		if i > 0 {
			prev := ordered[i-1]
			if cur.ExtSeq <= prev.ExtSeq {
				continue // defensive: duplicates belong in the jitter buffer
			}
			gap := int(cur.ExtSeq - prev.ExtSeq - 1)
			span := slotsSpan(prev, cur, gap, policy.NominalTickSpan)
			if gap > 0 {
				rep.GapEvents++
				if gap <= policy.MaxGapPackets {
					for k := 0; k < gap; k++ {
						rep.Units = append(rep.Units, Unit{
							Seq:          prev.ExtSeq + uint64(k) + 1,
							RTPTimestamp: uint32(uint64(prev.Timestamp) + uint64(span)*uint64(k+1)),
							TickSpan:     span,
							Silence:      true,
							Gap:          true,
						})
						rep.Recovered++
					}
				} else {
					rep.Incomplete = true
					rep.Unrecovered += gap
					if rep.Faults.Len() < policy.MaxGapEvents {
						off := prev.Offset
						if off < 0 {
							off = cur.Offset
						}
						e := fault.New(fault.StageRecovery, fault.KindLoss,
							"gap of %d packets between seq %d and %d", gap, prev.ExtSeq, cur.ExtSeq)
						e.At(off, -1)
						rep.Faults = append(rep.Faults, e)
						e = fault.New(fault.StageRecovery, fault.KindIncomplete,
							"fragment incomplete: %d packets lost between seq %d and %d",
							gap, prev.ExtSeq, cur.ExtSeq)
						e.At(off, -1)
						rep.Faults = append(rep.Faults, e)
					}
				}
			}
			rep.Units = append(rep.Units, Unit{
				Seq:          cur.ExtSeq,
				RTPTimestamp: cur.Timestamp,
				TickSpan:     span,
				Packet:       cur,
			})
			continue
		}
		rep.Units = append(rep.Units, Unit{
			Seq:          cur.ExtSeq,
			RTPTimestamp: cur.Timestamp,
			TickSpan:     policy.NominalTickSpan,
			Packet:       cur,
		})
	}
	return rep, nil
}

// LengthTicks sums the RTP tick duration of every unit.
func (r Report) LengthTicks() int64 {
	var n int64
	for _, u := range r.Units {
		n += u.TickSpan
	}
	return n
}

// slotsSpan estimates the RTP ticks per slot between two packets from their
// timestamp delta, falling back to the policy default when implausible.
func slotsSpan(prev, cur *rtpparse.Packet, gap int, fallback int64) int64 {
	delta := int64(uint32(cur.Timestamp - prev.Timestamp))
	slots := int64(gap) + 1
	if delta <= 0 || delta > 1<<20 || delta/slots <= 0 {
		return fallback
	}
	return delta / slots
}
