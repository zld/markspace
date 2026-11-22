package clock

import (
	"math/bits"

	"github.com/pion/rtp"
)

// mul128 returns the 128-bit product a*b as (hi, lo).
func mul128(a, b uint64) (hi, lo uint64) { return bits.Mul64(a, b) }

// div128 returns the 128-bit / 64-bit quotient and remainder.
func div128(hi, lo, y uint64) (q, r uint64) { return bits.Div64(hi, lo, y) }

// Unwrapper expands cyclic 32-bit sender timestamps into a monotonic 64-bit
// media timeline. Wrap decisions use the signed half-range rule, identical to
// RFC 3550 sequence-number extension, and are therefore fully deterministic.
type Unwrapper struct {
	last32   uint32
	last64   uint64
	started  bool
	base64   uint64 // absolute timeline value of the first observed tick
	wrapSeen uint64
}

// NewUnwrapper creates an unwrapper. baseNanos anchors the first 32-bit tick
// to an arbitrary absolute position (usually zero).
func NewUnwrapper(base uint64) *Unwrapper { return &Unwrapper{base64: base} }

// Extend feeds one raw 32-bit timestamp and returns its absolute 64-bit value.
func (u *Unwrapper) Extend(ts32 uint32) uint64 {
	if !u.started {
		u.started = true
		u.last32 = ts32
		u.last64 = u.base64 + uint64(ts32)
		return u.last64
	}
	delta := int64(ts32 - u.last32) // modular subtraction, interpreted signed
	next := int64(u.last64) + delta
	if next < 0 { // cannot predate the timeline anchor
		next = int64(u.last64) + int64(int32(ts32-u.last32))
	}
	if ts32 < u.last32 && delta > 0 {
		u.wrapSeen++
	}
	u.last32 = ts32
	u.last64 = uint64(next)
	return u.last64
}

// WrapCount reports how many 2^32 cycles have been observed.
func (u *Unwrapper) WrapCount() uint64 { return u.wrapSeen }

// Snapshot captures restartable unwrapper state.
type Snapshot struct {
	Last32   uint32
	Last64   uint64
	Started  bool
	Base64   uint64
	WrapSeen uint64
}

// Snapshot returns the current state.
func (u *Unwrapper) Snapshot() Snapshot {
	return Snapshot{u.last32, u.last64, u.started, u.base64, u.wrapSeen}
}

// Restore rehydrates an unwrapper from a checkpoint snapshot.
func (u *Unwrapper) Restore(s Snapshot) {
	u.last32, u.last64, u.started, u.base64, u.wrapSeen = s.Last32, s.Last64, s.Started, s.Base64, s.WrapSeen
}

// Packetizer wraps raw sample bytes in RTP packets so pion/rtp owns packet
// boundaries and header (de)serialization; the RTP timestamp field carries the
// cyclic 32-bit node clock that the Unwrapper expands.
type Packetizer struct {
	ssrc      uint32
	seq       uint16
	clockRate uint32
	mtu       int
}

// NewPacketizer builds a packetizer. mtu is the maximum RTP payload per packet.
func NewPacketizer(ssrc uint32, clockRate uint32, mtu int) *Packetizer {
	if mtu <= 0 {
		mtu = 1400
	}
	return &Packetizer{ssrc: ssrc, clockRate: clockRate, mtu: mtu}
}

// Packetize splits one sample's payload into RTP packets sharing ts32.
func (p *Packetizer) Packetize(payload []byte, ts32 uint32) ([]*rtp.Packet, error) {
	if len(payload) == 0 {
		return nil, NewError(ErrInvariant, "empty sample payload")
	}
	var out []*rtp.Packet
	for off := 0; off < len(payload); {
		end := off + p.mtu
		if end > len(payload) {
			end = len(payload)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SSRC:           p.ssrc,
				SequenceNumber: p.seq,
				Timestamp:      ts32,
				Marker:         end == len(payload),
			},
			Payload: append([]byte(nil), payload[off:end]...),
		}
		p.seq++
		out = append(out, pkt)
		off = end
	}
	return out, nil
}

// Reassemble validates a packet set and rejoins its payloads at the marker
// boundary, exercising pion/rtp's unmarshal path rather than trusting memory.
func Reassemble(pkts []*rtp.Packet) (uint32, []byte, error) {
	if len(pkts) == 0 {
		return 0, nil, NewError(ErrInvariant, "no packets to reassemble")
	}
	var payload []byte
	ts := pkts[0].Header.Timestamp
	for i, pkt := range pkts {
		raw, err := pkt.Marshal()
		if err != nil {
			return 0, nil, ErrInvalidRTP(err.Error())
		}
		var got rtp.Packet
		if err := got.Unmarshal(raw); err != nil {
			return 0, nil, ErrInvalidRTP(err.Error())
		}
		if got.Header.Timestamp != ts {
			return 0, nil, NewError(ErrTimestampReversal, "timestamp changed inside sample packet set")
		}
		if i < len(pkts)-1 && got.Header.Marker {
			return 0, nil, ErrInvalidRTP("premature marker")
		}
		payload = append(payload, got.Payload...)
	}
	return ts, payload, nil
}
