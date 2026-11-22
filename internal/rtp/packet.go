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

// Extender performs 16->64 bit sequence number extension per stream once a
// fixed origin (the minimum wire sequence in the warm-up window) is known.
type Extender struct {
	origin    uint16
	originSet bool
	largest   uint64
}

const maxJump = 1 << 15 // half the 16-bit space, per RFC 3550 wrap heuristic

// setOrigin pins the extended-zero wire sequence.
func (e *Extender) setOrigin(seq uint16) {
	e.origin = seq
	e.originSet = true
}

// Extend maps a wire sequence number to a stable, non-negative extended value
// relative to the fixed origin. Only valid once the origin is established.
func (e *Extender) Extend(seq uint16) uint64 {
	d := int32(seq) - int32(e.origin)
	var ext uint64
	if d < -maxJump {
		// Forward wrap across the 16-bit boundary.
		ext = uint64(int64(seq)-int64(e.origin)) + (1 << 16)
	} else {
		if d < 0 {
			d = 0
		}
		ext = uint64(d)
	}
	if ext > e.largest {
		e.largest = ext
	}
	return ext
}

// OriginReady reports whether the anchor has been established.
func (e *Extender) OriginReady() bool { return e.originSet }

// Largest reports the highest extended sequence seen so far.
func (e *Extender) Largest() uint64 { return e.largest }

// streamDemux holds the warm-up backlog and extender for one (SSRC,PT) stream.
type streamDemux struct {
	warmup  int
	seen    []uint16
	pending []Packet
	ext     *Extender
}

// Demux fans incoming packets out per (SSRC, PT) and extends their sequence
// numbers independently. It buffers a warm-up window per stream to anchor a
// non-negative extended origin before releasing any packet downstream.
type Demux struct {
	streams map[StreamKey]*streamDemux
	warmup  int
}

// NewDemux creates a demultiplexer with the default 64-packet warm-up.
func NewDemux() *Demux { return NewDemuxWarmup(64) }

// NewDemuxWarmup sets the per-stream warm-up window in packets.
func NewDemuxWarmup(warmup int) *Demux {
	if warmup < 1 {
		warmup = 1
	}
	return &Demux{streams: map[StreamKey]*streamDemux{}, warmup: warmup}
}

// Push buffers a packet until the warm-up window establishes the origin, then
// returns all now-releasable packets (the backlog plus p when it completes the
// window) with stable non-negative extended sequence numbers. During warm-up
// it returns nil.
func (d *Demux) Push(p Packet) []Packet {
	k := p.Key()
	s, ok := d.streams[k]
	if !ok {
		s = &streamDemux{warmup: d.warmup, ext: &Extender{}}
		d.streams[k] = s
	}
	if !s.ext.OriginReady() {
		s.pending = append(s.pending, p)
		s.seen = append(s.seen, p.Seq)
		if len(s.seen) < s.warmup {
			return nil
		}
		origin := s.seen[0]
		for _, q := range s.seen[1:] {
			if q < origin {
				origin = q
			}
		}
		s.ext.setOrigin(origin)
		out := make([]Packet, 0, len(s.pending))
		for _, b := range s.pending {
			b.ExtSeq = s.ext.Extend(b.Seq)
			out = append(out, b)
		}
		s.pending = nil
		s.seen = nil
		return out
	}
	p.ExtSeq = s.ext.Extend(p.Seq)
	return []Packet{p}
}

// PushOne extends a single packet once the warm-up origin is established.
// It returns ok=false while the stream is still collecting its warm-up
// window; callers that must not lose those packets should use Push and feed
// every returned packet into the jitter buffer.
func (d *Demux) PushOne(p Packet) (extended Packet, ok bool) {
	out := d.Push(p)
	if len(out) == 0 {
		return Packet{}, false
	}
	return out[len(out)-1], true
}

// Streams reports the stream keys observed so far.
func (d *Demux) Streams() []StreamKey {
	keys := make([]StreamKey, 0, len(d.streams))
	for k := range d.streams {
		keys = append(keys, k)
	}
	return keys
}
