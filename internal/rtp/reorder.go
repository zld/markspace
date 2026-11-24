// Package rtp reorders and deduplicates RTP packets using a 16-bit sequence
// window. It never fabricates payload for sequence gaps.
package rtp

import (
	"time"

	"github.com/pion/rtp"

	"medpipe/internal/fault"
)

const (
	// DefaultWindow bounds the sequence distance kept for reordering.
	DefaultWindow uint16 = 64
	maxSeenFactor        = 8
)

// Packet is the transport-neutral view of an RTP packet used by the kernel.
type Packet struct {
	Sequence   uint16
	Timestamp  uint32
	SSRC       uint32
	Marker     bool
	Payload    []byte
	ReceivedAt time.Time
}

// Unmarshal parses one RTP wire packet with github.com/pion/rtp.
func Unmarshal(data []byte, receivedAt time.Time) (Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(data); err != nil {
		return Packet{}, fault.Wrap(fault.ErrCorruptSample, "rtp.parse", -1, "invalid RTP packet", err)
	}
	return Packet{
		Sequence:   p.SequenceNumber,
		Timestamp:  p.Timestamp,
		SSRC:       p.SSRC,
		Marker:     p.Marker,
		Payload:    append([]byte(nil), p.Payload...),
		ReceivedAt: receivedAt,
	}, nil
}

// Marshal re-encodes the packet through pion/rtp. Tests and fixtures use it so
// generated inputs are real RTP wire packets.
func (p Packet) Marshal() ([]byte, error) {
	rp := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    33,
			SequenceNumber: p.Sequence,
			Timestamp:      p.Timestamp,
			SSRC:           p.SSRC,
			Marker:         p.Marker,
		},
		Payload: p.Payload,
	}
	return rp.Marshal()
}

// Stats contains observable reordering and loss counters.
type Stats struct {
	Received   int
	Emitted    int
	Duplicates int
	Late       int
	OutOfOrder int
	Gaps       int
}

// Reorderer is a sequence-window reorder buffer.
type Reorderer struct {
	window     uint16
	wait       time.Duration
	bandwidth  int
	packetSize int
	now        func() time.Time

	initialized bool
	next        uint16
	buffer      map[uint16]Packet
	seen        map[uint16]struct{}
	oldestAt    time.Time
	events      []fault.Event
	stats       Stats
}

// Option configures a Reorderer.
type Option func(*Reorderer)

// WithWait sets the maximum wait for a missing sequence at the head.
func WithWait(d time.Duration) Option {
	return func(r *Reorderer) { r.wait = d }
}

// WithClock injects a clock for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(r *Reorderer) { r.now = now }
}

// NewReorderer constructs a windowed reorderer.
func NewReorderer(window uint16, opts ...Option) *Reorderer {
	if window == 0 {
		window = DefaultWindow
	}
	r := &Reorderer{
		window:     window,
		wait:       20 * time.Millisecond,
		packetSize: 188 * 7,
		now:        time.Now,
		buffer:     make(map[uint16]Packet),
		seen:       make(map[uint16]struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Stats returns a copy of counters.
func (r *Reorderer) Stats() Stats { return r.stats }

// Events returns gap and late-packet events.
func (r *Reorderer) Events() []fault.Event {
	return append([]fault.Event(nil), r.events...)
}

// SetBandwidth updates only the head-of-line wait estimate. It never rewrites
// or reorders packets already emitted to the committed sequence prefix.
func (r *Reorderer) SetBandwidth(bitsPerSecond, estimatedPacketBytes int) {
	r.bandwidth = bitsPerSecond
	if estimatedPacketBytes > 0 {
		r.packetSize = estimatedPacketBytes
	}
	if bitsPerSecond <= 0 {
		return
	}
	wire := float64(r.packetSize*8) / float64(bitsPerSecond) * float64(time.Second)
	d := time.Duration(2*wire) + time.Millisecond
	if d < time.Millisecond {
		d = time.Millisecond
	}
	if d > 250*time.Millisecond {
		d = 250 * time.Millisecond
	}
	r.wait = d
}

// Push accepts one packet and returns any newly contiguous ordered packets.
func (r *Reorderer) Push(p Packet) []Packet {
	r.stats.Received++
	if !r.initialized {
		r.initialized = true
		r.next = p.Sequence
		r.markSeen(p.Sequence)
		r.stats.Emitted++
		return []Packet{p}
	}

	d := int16(p.Sequence - r.next)
	if _, ok := r.seen[p.Sequence]; ok {
		r.stats.Duplicates++
		return nil
	}

	if d < 0 {
		// Sequence lies below the committed next value.
		r.markSeen(p.Sequence)
		r.stats.Late++
		r.addGap(p.Sequence, "late packet outside committed window", false)
		return nil
	}

	r.markSeen(p.Sequence)
	if d > 0 {
		r.stats.OutOfOrder++
		r.buffer[p.Sequence] = p
		if r.oldestAt.IsZero() {
			r.oldestAt = r.now()
		}
	} else {
		r.buffer[p.Sequence] = p
	}
	return r.drain()
}

// Flush expires an aged head gap. It returns packets made contiguous after
// skipping missing sequences. Calling it with the injected clock lets tests
// advance time deterministically.
func (r *Reorderer) Flush() []Packet {
	if !r.initialized || len(r.buffer) == 0 {
		return nil
	}
	if r.now().Sub(r.oldestAt) < r.wait {
		return nil
	}
	// Skip exactly one missing expected sequence per expiry; callers invoke
	// Flush repeatedly when a burst contains several losses.
	if _, ok := r.buffer[r.next]; !ok {
		r.addGap(r.next, "sequence expired from reorder window", true)
		r.next++
		r.stats.Gaps++
		r.oldestAt = r.now()
	}
	return r.drain()
}

// Finish emits all buffered packets in sequence order, recording every
// unresolved missing sequence as a gap. No payload is synthesized.
func (r *Reorderer) Finish() []Packet {
	if !r.initialized {
		return nil
	}
	var out []Packet
	for len(r.buffer) > 0 {
		if p, ok := r.buffer[r.next]; ok {
			delete(r.buffer, r.next)
			out = append(out, p)
			r.next++
			continue
		}
		r.addGap(r.next, "missing RTP sequence at end of stream", true)
		r.stats.Gaps++
		r.next++
	}
	r.stats.Emitted += len(out)
	return out
}

func (r *Reorderer) drain() []Packet {
	var out []Packet
	for {
		p, ok := r.buffer[r.next]
		if !ok {
			break
		}
		delete(r.buffer, r.next)
		out = append(out, p)
		r.next++
	}
	if len(out) > 0 {
		r.oldestAt = time.Time{}
		r.stats.Emitted += len(out)
	}
	return out
}

func (r *Reorderer) markSeen(seq uint16) {
	r.seen[seq] = struct{}{}
	// Bound duplicate memory by evicting sequences far below next.
	if len(r.seen) > int(r.window)*maxSeenFactor {
		cut := r.next - r.window*maxSeenFactor
		for seq := range r.seen {
			if int16(seq-cut) < 0 {
				delete(r.seen, seq)
			}
		}
	}
}

func (r *Reorderer) addGap(seq uint16, msg string, recoverable bool) {
	r.events = append(r.events, fault.Event{
		Code:        fault.ErrContinuity,
		Severity:    "gap",
		Stage:       "rtp.reorder",
		Sequence:    seq,
		HasSequence: true,
		Message:     msg,
		Recoverable: recoverable,
	})
}
