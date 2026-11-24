package rtp

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediacheck/internal/clock"
)

// PayloadHeaderLen is the length of the generated frame header prepended
// to every payload by the sample generator: frame index (4) + flags (1).
const PayloadHeaderLen = 5

// FlagKeyframe marks payloads belonging to a keyframe (decoder restart).
const FlagKeyframe = 0x01

// FrameMeta is the per-frame metadata carried in generated payloads.
type FrameMeta struct {
	FrameIndex uint32
	Keyframe   bool
}

// DecodeMeta extracts the generated frame header from a packet payload.
func DecodeMeta(p *rtp.Packet) (FrameMeta, error) {
	if len(p.Payload) < PayloadHeaderLen {
		return FrameMeta{}, fmt.Errorf("payload too short for frame header: %d bytes", len(p.Payload))
	}
	return FrameMeta{
		FrameIndex: binary.BigEndian.Uint32(p.Payload[0:4]),
		Keyframe:   p.Payload[4]&FlagKeyframe != 0,
	}, nil
}

// Range describes one contiguous loss segment and whether decoding can
// restart at a following keyframe.
type Range struct {
	FirstLostSeq uint16
	LastLostSeq  uint16
	LostCount    int
	StartNanos   int64 // media time at the start of the affected segment
	EndNanos     int64 // media time where decoding can resume (or stream end)
	Recoverable  bool
	Reason       string
}

// Classify groups declared-lost sequences into ranges using the ordered
// emitted packets as anchors. A gap is recoverable when the first packet
// after the gap belongs to a keyframe; otherwise the unrecoverable extent
// runs forward to the next keyframe (or the end of the stream). No media
// packets are fabricated: ranges only describe what is missing.
func Classify(ordered []*rtp.Packet, lost []uint16, ts clock.Timescale) []Range {
	if len(lost) == 0 {
		return nil
	}
	bySeq := make(map[uint16]*rtp.Packet, len(ordered))
	for _, p := range ordered {
		bySeq[p.SequenceNumber] = p
	}
	toNanos := func(rtpTS uint32) int64 { return ts.ToNanos(int64(rtpTS)) }

	var ranges []Range
	for i := 0; i < len(lost); {
		// Gather one maximal run of consecutive lost sequences.
		j := i
		for j+1 < len(lost) && lost[j+1] == lost[j]+1 {
			j++
		}
		first, last := lost[i], lost[j]
		r := Range{FirstLostSeq: first, LastLostSeq: last, LostCount: j - i + 1}

		prev, next := bySeq[first-1], bySeq[last+1]
		switch {
		case prev != nil:
			r.StartNanos = toNanos(prev.Timestamp)
		case next != nil:
			r.StartNanos = toNanos(next.Timestamp)
		}
		nextKey := false
		if next != nil {
			if meta, err := DecodeMeta(next); err == nil && meta.Keyframe {
				nextKey = true
				r.EndNanos = toNanos(next.Timestamp)
			}
		}
		if nextKey {
			r.Recoverable = true
			r.Reason = "decoder restarts at following keyframe"
		} else {
			// Unrecoverable until the next keyframe after the gap.
			r.Recoverable = false
			r.Reason = "no keyframe immediately after gap; unrecoverable until next keyframe"
			r.EndNanos = r.StartNanos
			for _, p := range ordered {
				if seqLess(last, p.SequenceNumber) {
					if meta, err := DecodeMeta(p); err == nil && meta.Keyframe {
						r.EndNanos = toNanos(p.Timestamp)
						break
					}
					r.EndNanos = toNanos(p.Timestamp)
				}
			}
		}
		ranges = append(ranges, r)
		i = j + 1
	}
	return ranges
}
