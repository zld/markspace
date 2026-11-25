// Package stream decodes RTP auxiliary timing onto the unified PCM sample
// axis. Sequence numbers and timestamps are treated as wrapping counters;
// reordering and wraps are detected explicitly rather than guessed away.
package stream

import (
	"mediad/media"

	"github.com/pion/rtp"
)

// PacketInfo is one decoded RTP packet projected onto the monotonic axis.
type PacketInfo struct {
	Index            int    // 0-based arrival index
	Sequence         uint16 // raw RTP sequence number
	ExtendedSequence uint64 // unwrapped sequence number
	Timestamp        uint32 // raw RTP timestamp
	ExtendedStamp    uint64 // unwrapped RTP timestamp ticks
	SampleOffset     uint64 // timestamp mapped onto the PCM sample axis
	Marker           bool
	PayloadLen       int
}

// StreamResult is the per-stream decode output.
type StreamResult struct {
	Packets []PacketInfo
	// HighestExtendedSequence is the largest extended sequence seen, which
	// may be carried by a packet that arrived before a later reordered one.
	HighestExtendedSequence uint64
	Faults                  []media.Fault
}

// Decoder keeps the running state needed to extend wrapping counters.
type Decoder struct {
	tb            media.TimeBase
	prevSeq       uint64
	prevStamp     uint64
	highestSeq    uint64
	started       bool
}

// NewDecoder builds a decoder bound to the shared time base. tb.RTPClock
// must be non-zero for timestamp conversion; a zero clock is reported per
// packet instead of producing silent zero offsets.
func NewDecoder(tb media.TimeBase) *Decoder {
	return &Decoder{tb: tb}
}

// Decode parses every raw RTP packet in arrival order. Malformed buffers are
// recorded but do not abort the stream; later packets continue decoding so
// the report can cover the full input.
func (d *Decoder) Decode(raw [][]byte) *StreamResult {
	res := &StreamResult{}
	for i, buf := range raw {
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			res.Faults = append(res.Faults, media.NewFault(
				media.CatRTPMalformed, "packet %d: %v", i, err))
			continue
		}
		info := PacketInfo{
			Index:      i,
			Sequence:   pkt.SequenceNumber,
			Timestamp:  pkt.Timestamp,
			Marker:     pkt.Marker,
			PayloadLen: len(pkt.Payload),
		}

		if !d.started {
			d.prevSeq = uint64(pkt.SequenceNumber)
			d.prevStamp = uint64(pkt.Timestamp)
			d.highestSeq = d.prevSeq
			d.started = true
		} else {
			extSeq := media.Extend16(d.prevSeq, pkt.SequenceNumber)
			extStamp := media.Extend32(d.prevStamp, pkt.Timestamp)
			if extSeq < d.highestSeq {
				res.Faults = append(res.Faults, media.NewSampleFault(
					media.CatRTPReorder, int64(extSeq), int64(extSeq)+1,
					"packet %d sequence %d (extended %d) arrived after highest %d",
					i, pkt.SequenceNumber, extSeq, d.highestSeq))
			}
			// A positive extended step from a smaller raw value means the
			// 16-bit counter wrapped rather than jumped backward.
			if extSeq > d.prevSeq && uint64(pkt.SequenceNumber) < (d.prevSeq&0xFFFF) {
				res.Faults = append(res.Faults, media.NewFault(
					media.CatRTPSeqWrap, "packet %d: sequence wrapped to %d (extended %d)",
					i, pkt.SequenceNumber, extSeq))
			}
			// Same rule for the 32-bit RTP timestamp.
			if extStamp > d.prevStamp && uint64(pkt.Timestamp) < (d.prevStamp&0xFFFFFFFF) {
				res.Faults = append(res.Faults, media.NewFault(
					media.CatRTPTSWrap, "packet %d: timestamp wrapped to %d (extended %d)",
					i, pkt.Timestamp, extStamp))
			}
			d.prevSeq = extSeq
			d.prevStamp = extStamp
			if extSeq > d.highestSeq {
				d.highestSeq = extSeq
			}
		}

		info.ExtendedSequence = d.prevSeq
		info.ExtendedStamp = d.prevStamp
		samples, err := d.tb.RTPToSamples(d.prevStamp)
		if err != nil {
			res.Faults = append(res.Faults, media.NewFault(
				media.CatRTPClockUnavailable,
				"packet %d: cannot map RTP timestamp to samples: %v", i, err))
		} else {
			info.SampleOffset = samples
		}
		res.Packets = append(res.Packets, info)
	}
	res.HighestExtendedSequence = d.highestSeq
	return res
}
