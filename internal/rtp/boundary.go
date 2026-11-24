// Package rtp validates transport packet boundaries without touching media
// payloads. It parses RTP headers with pion/rtp and enforces sequence
// continuity plus per-packet payload boundary integrity. Every anomaly is
// reported with the RTP sequence number and the associated media byte offset,
// and verification never mutates the remux payload.
package rtp

import (
	"github.com/pion/rtp"

	"mediapack/internal/fault"
)

// Boundary associates one RTP packet with the media block it transports.
type Boundary struct {
	// SequenceNumber is the 16-bit RTP sequence number.
	SequenceNumber uint16
	// MediaOffset is the absolute input offset of the associated block.
	MediaOffset int64
	// PayloadLen is the RTP payload length in bytes.
	PayloadLen int
	// Marker mirrors the RTP marker bit.
	Marker bool
}

// Verifier consumes packets in arrival order and detects duplicates and
// jumps. Wrap-around at the 16-bit boundary is treated as continuity.
type Verifier struct {
	hasLast bool
	last    uint16
	// seen detects duplicate sequence numbers within the current window.
	seen map[uint16]int64
}

// NewVerifier creates an empty boundary verifier.
func NewVerifier() *Verifier {
	return &Verifier{seen: make(map[uint16]int64)}
}

// Check parses one raw RTP datagram and validates it against its expected
// media boundary. wantPayloadLen is the exact number of codec bytes the
// sender intended to carry (from the demuxed block); a mismatch means the
// transport frame was truncated or padded. It returns the parsed Boundary on
// success. A duplicate, jump or boundary violation yields a structured fault
// carrying both the RTP sequence and the media offset; the payload is never
// modified.
func (v *Verifier) Check(datagram []byte, mediaOffset int64, wantPayloadLen int) (*Boundary, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(datagram); err != nil {
		return nil, fault.New(fault.KindRTPSequence, mediaOffset,
			"rtp header unmarshal failed: %v", err)
	}
	if err := verifyPayloadBoundary(pkt, len(datagram), wantPayloadLen); err != nil {
		fe := fault.Of(err)
		fe.Offset = mediaOffset
		return nil, fe
	}
	b := &Boundary{
		SequenceNumber: pkt.SequenceNumber,
		MediaOffset:    mediaOffset,
		PayloadLen:     len(pkt.Payload),
		Marker:         pkt.Marker,
	}
	if off, dup := v.seen[pkt.SequenceNumber]; dup {
		return b, fault.New(fault.KindRTPSequence, mediaOffset,
			"duplicate RTP sequence %d (first seen at media offset %d)",
			pkt.SequenceNumber, off)
	}
	v.seen[pkt.SequenceNumber] = mediaOffset
	if v.hasLast {
		want := v.last + 1 // wraps naturally at uint16
		if pkt.SequenceNumber != want {
			gap := int(pkt.SequenceNumber) - int(want)
			return b, fault.New(fault.KindRTPSequence, mediaOffset,
				"RTP sequence jump: got %d expected %d (gap=%d) at media offset %d",
				pkt.SequenceNumber, want, gap, mediaOffset)
		}
	}
	v.last = pkt.SequenceNumber
	v.hasLast = true
	return b, nil
}

// verifyPayloadBoundary ensures the datagram exactly covers the RTP header
// plus the expected payload, guarding against truncated or padded frames that
// a permissive parser would otherwise accept silently.
func verifyPayloadBoundary(pkt *rtp.Packet, datagramLen, wantPayloadLen int) error {
	headerLen := pkt.Header.MarshalSize()
	want := headerLen + wantPayloadLen
	if want != datagramLen {
		return fault.New(fault.KindRTPSequence, -1,
			"payload boundary mismatch: header %d + expected payload %d = %d, datagram %d",
			headerLen, wantPayloadLen, want, datagramLen)
	}
	if len(pkt.Payload) != wantPayloadLen {
		return fault.New(fault.KindRTPSequence, -1,
			"carried payload %d bytes differs from expected %d",
			len(pkt.Payload), wantPayloadLen)
	}
	return nil
}

// CheckStream runs Check over a sequence of (datagram, mediaOffset,
// wantPayloadLen) triples, stopping at the first anomaly.
func CheckStream(pairs [][]byte, mediaOffsets []int64, wantLens []int) ([]*Boundary, error) {
	v := NewVerifier()
	out := make([]*Boundary, 0, len(pairs))
	for i, dg := range pairs {
		off := int64(-1)
		if i < len(mediaOffsets) {
			off = mediaOffsets[i]
		}
		want := 0
		if i < len(wantLens) {
			want = wantLens[i]
		}
		b, err := v.Check(dg, off, want)
		if err != nil {
			return out, err
		}
		out = append(out, b)
	}
	return out, nil
}
