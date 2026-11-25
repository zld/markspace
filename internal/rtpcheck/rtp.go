// Package rtpcheck validates RTP packet structure and integer timestamp /
// sequence continuity using github.com/pion/rtp.
package rtpcheck

import (
	"github.com/pion/rtp"

	"assetcheck/internal/errmodel"
)

// Issue is one packet-level validation finding.
type Issue struct {
	Index     int           `json:"index"`
	Code      errmodel.Code `json:"code"`
	Message   string        `json:"message"`
	Sequence  uint16        `json:"sequence,omitempty"`
	Timestamp uint32        `json:"timestamp,omitempty"`
}

// Report summarizes a sequence of RTP packets.
type Report struct {
	PacketCount       int     `json:"packet-count"`
	SSRC              uint32  `json:"ssrc"`
	FirstSequence     uint16  `json:"first-sequence"`
	LastSequence      uint16  `json:"last-sequence"`
	FirstTimestamp    uint32  `json:"first-timestamp"`
	LastTimestamp     uint32  `json:"last-timestamp"`
	SequenceGaps      int     `json:"sequence-gaps"`
	MissingPackets    int     `json:"missing-packets"`
	DuplicateSequence int     `json:"duplicate-sequences"`
	TimestampBackward int     `json:"timestamp-backward"`
	TimestampWraps    int     `json:"timestamp-wraps"`
	Issues            []Issue `json:"issues,omitempty"`
}

// OK reports whether no structural/continuity issues were found.
func (r *Report) OK() bool {
	return r.SequenceGaps == 0 && r.DuplicateSequence == 0 &&
		r.TimestampBackward == 0 && len(r.Issues) == 0
}

// ParsePacket validates one raw RTP packet via pion/rtp.
func ParsePacket(b []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(b); err != nil {
		return nil, errmodel.New(errmodel.ErrRTPPacket, "invalid RTP packet: %v", err)
	}
	return p, nil
}

// Validate checks raw packets in capture order.
func Validate(raw [][]byte) (*Report, error) {
	rep := &Report{}
	var have bool
	var lastSeq uint16
	var lastTS uint32
	seenSeq := map[uint16]bool{}

	for i, b := range raw {
		rep.PacketCount++
		p, err := ParsePacket(b)
		if err != nil {
			rep.Issues = append(rep.Issues, Issue{Index: i, Code: errmodel.ErrRTPPacket, Message: err.Error()})
			continue
		}
		if !have {
			rep.SSRC = p.SSRC
			rep.FirstSequence = p.SequenceNumber
			rep.FirstTimestamp = p.Timestamp
			have = true
		}
		if p.SSRC != rep.SSRC {
			rep.Issues = append(rep.Issues, Issue{
				Index: i, Code: errmodel.ErrRTPPacket, Sequence: p.SequenceNumber,
				Message: "SSRC changes within one stream",
			})
		}

		if seenSeq[p.SequenceNumber] {
			rep.DuplicateSequence++
			rep.Issues = append(rep.Issues, Issue{
				Index: i, Code: errmodel.ErrRTPSequence, Sequence: p.SequenceNumber,
				Timestamp: p.Timestamp, Message: "duplicate RTP sequence number",
			})
		}
		seenSeq[p.SequenceNumber] = true

		if i > 0 {
			fwd := (uint32(p.SequenceNumber) - uint32(lastSeq)) & 0xFFFF
			switch {
			case fwd == 0:
				// counted as duplicate above
			case fwd == 1:
				// contiguous
			case fwd < 0x8000:
				rep.SequenceGaps++
				missing := int(fwd - 1)
				rep.MissingPackets += missing
				rep.Issues = append(rep.Issues, Issue{
					Index: i, Code: errmodel.ErrRTPSequence, Sequence: p.SequenceNumber,
					Message: "RTP sequence gap",
				})
			}

			if p.Timestamp < lastTS {
				// A small backward move is a real regression; a large jump is wrap.
				if uint64(lastTS)-uint64(p.Timestamp) < uint64(1)<<31 {
					rep.TimestampBackward++
					rep.Issues = append(rep.Issues, Issue{
						Index: i, Code: errmodel.ErrRTPTimestamp, Timestamp: p.Timestamp,
						Message: "RTP timestamp moved backward",
					})
				}
			}
			if p.Timestamp < lastTS {
				rep.TimestampWraps++
			}
		}
		lastSeq = p.SequenceNumber
		lastTS = p.Timestamp
		rep.LastSequence = p.SequenceNumber
		rep.LastTimestamp = p.Timestamp
	}
	if !have {
		return rep, errmodel.New(errmodel.ErrRTPPacket, "no parseable RTP packets")
	}
	return rep, nil
}

// TimestampToNanos converts an RTP timestamp in clockRate Hz to nanoseconds
// using integer arithmetic.
func TimestampToNanos(ts uint32, clockRate uint32) int64 {
	if clockRate == 0 {
		return 0
	}
	return int64(ts) * 1_000_000_000 / int64(clockRate)
}

// ForwardDelta returns the wrap-safe forward timestamp delta in clock ticks;
// unsigned 32-bit subtraction handles timestamp wraparound exactly.
func ForwardDelta(prev, curr uint32) uint32 { return curr - prev }
