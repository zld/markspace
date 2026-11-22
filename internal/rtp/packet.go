// Package rtp parses RTP streams, extends 16-bit sequence numbers onto a
// monotonic 64-bit axis, and feeds a bounded jitter/repair pipeline.
package rtp

import (
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// Packet is the internal representation shared by the jitter buffer and
// repair stages. Positions use an extended 64-bit sequence number and a
// shared nanosecond time base.
type Packet struct {
	SSRC      uint32
	PT        uint8
	Seq       uint16 // wire sequence number
	ExtSeq    uint64 // extended sequence number (zero-based at stream start)
	RTPTime   uint32 // RTP timestamp as carried on the wire
	Nanos     int64  // position on the shared nanosecond base
	Marker    bool
	Payload   []byte
	ArrivalNS int64 // local receive time on the shared base
}

// StreamKey identifies one RTP track. Packets of different streams are never
// reordered against each other.
type StreamKey struct {
	SSRC uint32
	PT   uint8
}

// Key returns the stream key carried by a packet.
func (p Packet) Key() StreamKey { return StreamKey{SSRC: p.SSRC, PT: p.PT} }

// Unmarshal parses one wire datagram. Empty payloads are legal (comfort-noise
// / keep-alive) and preserved rather than rejected.
func Unmarshal(wire []byte, arrivalNS int64) (Packet, error) {
	var rp rtp.Packet
	if err := rp.Unmarshal(wire); err != nil {
		return Packet{}, fmt.Errorf("rtp unmarshal: %w", err)
	}
	return Packet{
		SSRC:      rp.SSRC,
		PT:        rp.PayloadType,
		Seq:       rp.SequenceNumber,
		RTPTime:   rp.Timestamp,
		Marker:    rp.Marker,
		Payload:   append([]byte(nil), rp.Payload...),
		ArrivalNS: arrivalNS,
	}, nil
}

// Marshal re-encodes a packet to the RTP wire format.
func (p Packet) Marshal() ([]byte, error) {
	rp := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    p.PT,
			SequenceNumber: p.Seq,
			Timestamp:      p.RTPTime,
			SSRC:           p.SSRC,
			Marker:         p.Marker,
		},
		Payload: p.Payload,
	}
	return rp.Marshal()
}

// ClockRate validates an RTP clock rate and returns the nanoseconds per clock
// tick as a rational (nanos per `per` ticks), e.g. 48kHz returns (125000, 6).
// All standard media clocks (8/16/44.1/48/90kHz) are supported; conversion
// multiplies by nanos then divides by per at the call site to stay exact.
func ClockRate(rate uint32) (nanos, per int64, err error) {
	if rate == 0 {
		return 0, 0, errors.New("rtp clock rate must be non-zero")
	}
	g := gcd(1_000_000_000, int64(rate))
	return 1_000_000_000 / g, int64(rate) / g, nil
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// RTPToNanos converts an RTP timestamp delta to integer nanoseconds using the
// rational tick description returned by ClockRate.
func RTPToNanos(ticks uint32, nanos, per int64) int64 {
	return int64(ticks) * nanos / per
}

// TickNanos is the whole-nanosecond approximation used for positioning gap
// events when only an average packet spacing is available.
func TickNanos(nanos, per int64) int64 { return nanos / per }

// Extender performs RFC 3550-style 16->64 bit sequence number extension per
// stream, including restart/reset detection after a large jump.
type Extender struct {
	last      uint16
	cycles    uint64
	started   bool
	base      uint16
	baseSet   bool
	largest   uint64
}

const maxJump = 1 << 15 // half the 16-bit space, per RFC 3550 wrap heuristic

// Extend maps a wire sequence number to a monotonic extended value. The very
// first packet anchors the stream at extended sequence zero.
func (e *Extender) Extend(seq uint16) uint64 {
	if !e.started {
		e.started = true
		e.base, e.baseSet = seq, true
		e.last = seq
		e.largest = 0
		return 0
	}
	delta := int32(seq) - int32(e.last)
	switch {
	case delta > maxJump: // backwards jump over a cycle boundary => stream reset
		e.cycles -= 1 << 16
	case delta < -maxJump: // forward wrap past 65535
		e.cycles += 1 << 16
	}
	ext := e.cycles + uint64(seq) - uint64(e.base)
	e.last = seq
	if ext > e.largest {
		e.largest = ext
	}
	return ext
}

// Largest reports the highest extended sequence seen so far.
func (e *Extender) Largest() uint64 { return e.largest }

// Demux fans incoming packets out per (SSRC, PT) and extends their sequence
// numbers independently for every stream.
type Demux struct {
	ext map[StreamKey]*Extender
}

// NewDemux creates an empty demultiplexer.
func NewDemux() *Demux { return &Demux{ext: map[StreamKey]*Extender{}} }

// Push extends one parsed packet and returns it with ExtSeq filled in.
func (d *Demux) Push(p Packet) Packet {
	k := p.Key()
	e, ok := d.ext[k]
	if !ok {
		e = &Extender{}
		d.ext[k] = e
	}
	p.ExtSeq = e.Extend(p.Seq)
	return p
}

// Streams reports the stream keys observed so far.
func (d *Demux) Streams() []StreamKey {
	keys := make([]StreamKey, 0, len(d.ext))
	for k := range d.ext {
		keys = append(keys, k)
	}
	return keys
}
