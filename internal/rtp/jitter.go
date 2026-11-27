// Package rtp buffers reordered RTP packets, extends 16-bit sequence numbers
// across wrap-around, and decides whether a gap is recoverable within a
// configured wait window.
package rtp

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Config controls buffering and gap recovery.
type Config struct {
	// WaitWindow is how many packets beyond a gap may arrive before the gap
	// is declared unrecoverable.
	WaitWindow int
	// BufferLimit caps the bytes held in the reorder buffer (default 64 KiB).
	BufferLimit int
	// ClockRate is the RTP media clock rate used to convert timestamps.
	ClockRate uint32
}

// DefaultConfig returns the documented defaults: a 64 KiB buffer, a wait
// window of eight packets and a 90 kHz video clock.
func DefaultConfig() Config {
	return Config{WaitWindow: 8, BufferLimit: 64 * 1024, ClockRate: 90000}
}

// Validate rejects nonsensical settings rather than silently clamping them.
func (c Config) Validate() error {
	if c.WaitWindow < 0 {
		return fault.Newf(fault.CodeRTPSequenceLimit, "", "wait window must be non-negative, got %d", c.WaitWindow)
	}
	if c.BufferLimit <= 0 {
		return fault.Newf(fault.CodeRTPBufferLimit, "", "buffer limit must be positive, got %d", c.BufferLimit)
	}
	if c.ClockRate == 0 {
		return fault.Newf(fault.CodeRTPSequenceLimit, "", "clock rate must be positive")
	}
	return nil
}

// Packet is a parsed RTP packet with a wrap-extended sequence number and a
// nanosecond arrival time.
type Packet struct {
	ExtSeq     uint64     `json:"ext_seq"`
	Seq        uint16     `json:"seq"`
	SSRC       uint32     `json:"ssrc"`
	Timestamp  uint32     `json:"timestamp"`
	Payload    []byte     `json:"-"`
	PayloadLen int        `json:"payload_len"`
	Marker     bool       `json:"marker"`
	Arrival    clock.Time `json:"arrival_ns"`
	Duplicate  bool       `json:"duplicate"`
}

// Gap describes a contiguous run of missing extended sequence numbers.
type Gap struct {
	From  uint64 `json:"from"`
	To    uint64 `json:"to"`
	Count uint64 `json:"count"`
}

// Stats summarises a jitter buffer run.
type Stats struct {
	Received      int    `json:"received"`
	Unique        int    `json:"unique"`
	Duplicates    int    `json:"duplicates"`
	Reordered     int    `json:"reordered"`
	Recovered     int    `json:"recovered"`
	Unrecoverable int    `json:"unrecoverable"`
	Delivered     int    `json:"delivered"`
	PeakBytes     int    `json:"peak_bytes"`
	BaseExt       uint64 `json:"base_ext"`
}

// Result carries packets that became deliverable after an operation together
// with any stable faults. An unrecoverable gap produces a fault while later
// valid packets are still delivered.
type Result struct {
	Delivered []*Packet
	Faults    []*fault.Fault
}

// Buffer reassembles packets in extended sequence order. It is not safe for
// concurrent use.
type Buffer struct {
	cfg      Config
	started  bool
	base     uint64
	high     uint64
	deliver  uint64
	seen     map[uint64]*Packet
	bufBytes int
	stats    Stats
	closed   bool
}

// NewBuffer creates a jitter buffer with the supplied configuration.
func NewBuffer(cfg Config) (*Buffer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Buffer{cfg: cfg, seen: make(map[uint64]*Packet)}, nil
}

// Push accepts one marshalled RTP packet. Duplicate packets are idempotent
// no-ops. Packets that fill a gap left by a later arrival are counted as
// recovered. When the wait window is exceeded an unrecoverable-gap fault is
// reported and delivery continues past the gap.
func (b *Buffer) Push(raw []byte, arrival clock.Time) (Result, error) {
	if b.closed {
		return Result{}, fault.Newf(fault.CodeInternal, "", "jitter buffer is closed")
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return Result{}, fault.Newf(fault.CodeRTPMalformed, "", "cannot parse RTP packet: %v", err)
	}
	p := &Packet{
		Seq:        pkt.SequenceNumber,
		SSRC:       pkt.SSRC,
		Timestamp:  pkt.Timestamp,
		Payload:    pkt.Payload,
		PayloadLen: len(pkt.Payload),
		Marker:     pkt.Marker,
		Arrival:    arrival,
	}
	b.stats.Received++
	if !b.started {
		b.started = true
		b.base = uint64(p.Seq)
		b.high = b.base
		b.deliver = b.base
		b.stats.BaseExt = b.base
	}
	ext := clock.ExtendSequenceNumber(p.Seq, b.high)
	p.ExtSeq = ext
	if ext < b.deliver {
		// Already emitted (or skipped): treat as a duplicate so repeated
		// delivery is idempotent.
		b.stats.Duplicates++
		p.Duplicate = true
		return Result{}, nil
	}
	if _, ok := b.seen[ext]; ok {
		b.stats.Duplicates++
		p.Duplicate = true
		return Result{}, nil
	}
	// A packet below the high water mark fills a hole that a later packet
	// exposed; that is a recovery. A packet that advances the frontier is
	// simply out of order relative to the gap it creates.
	filledGap := ext < b.high
	if ext > b.high {
		b.high = ext
	}
	if filledGap {
		b.stats.Reordered++
		b.stats.Recovered++
	}
	b.seen[ext] = p
	b.stats.Unique++
	b.bufBytes += len(p.Payload)
	if b.bufBytes > b.stats.PeakBytes {
		b.stats.PeakBytes = b.bufBytes
	}
	if b.bufBytes > b.cfg.BufferLimit {
		return Result{}, fault.New(fault.CodeRTPBufferLimit, 0, "",
			"reorder buffer holds %d bytes, limit is %d", b.bufBytes, b.cfg.BufferLimit)
	}
	return b.advance(), nil
}

// advance emits contiguous packets, declaring a gap unrecoverable once the
// wait window has been exhausted.
func (b *Buffer) advance() Result {
	var res Result
	for {
		for {
			p, ok := b.seen[b.deliver]
			if !ok {
				break
			}
			delete(b.seen, b.deliver)
			b.bufBytes -= len(p.Payload)
			res.Delivered = append(res.Delivered, p)
			b.stats.Delivered++
			b.deliver++
		}
		next, ok := b.nextSeen()
		if !ok {
			break
		}
		// The window is measured in packets that have arrived past the gap.
		if b.high-b.deliver < uint64(b.cfg.WaitWindow) {
			break
		}
		gap := Gap{From: b.deliver, To: next - 1, Count: next - b.deliver}
		b.stats.Unrecoverable += int(gap.Count)
		res.Faults = append(res.Faults, fault.New(fault.CodeRTPGapUnrecovered, 0, "",
			"%d packet(s) missing in %d..%d after a %d packet wait window", gap.Count, gap.From, gap.To, b.cfg.WaitWindow))
		b.deliver = next
	}
	return res
}

func (b *Buffer) nextSeen() (uint64, bool) {
	if len(b.seen) == 0 {
		return 0, false
	}
	min := uint64(^uint64(0))
	for ext := range b.seen {
		if ext < min {
			min = ext
		}
	}
	return min, true
}

// Close flushes the remaining packets in order. Any gap that survives is
// reported as unrecoverable; packets after it are still delivered so the
// caller can continue with valid data.
func (b *Buffer) Close() (Result, error) {
	if b.closed {
		return Result{}, nil
	}
	b.closed = true
	var res Result
	for {
		next, ok := b.nextSeen()
		if !ok {
			break
		}
		if next > b.deliver {
			gap := Gap{From: b.deliver, To: next - 1, Count: next - b.deliver}
			b.stats.Unrecoverable += int(gap.Count)
			res.Faults = append(res.Faults, fault.New(fault.CodeRTPGapUnrecovered, 0, "",
				"%d packet(s) missing in %d..%d at end of stream", gap.Count, gap.From, gap.To))
			b.deliver = next
		}
		p := b.seen[b.deliver]
		delete(b.seen, b.deliver)
		b.bufBytes -= len(p.Payload)
		res.Delivered = append(res.Delivered, p)
		b.stats.Delivered++
		b.deliver++
	}
	return res, nil
}

// Stats returns a copy of the running counters.
func (b *Buffer) Stats() Stats { return b.stats }

// Config returns the buffer configuration.
func (b *Buffer) Config() Config { return b.cfg }

// Pending returns packets still held above the delivery point in extended
// order. It is meant for diagnostics and tests.
func (b *Buffer) Pending() []*Packet {
	out := make([]*Packet, 0, len(b.seen))
	for _, p := range b.seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExtSeq < out[j].ExtSeq })
	return out
}

// String renders the counters for terminal logging.
func (s Stats) String() string {
	return fmt.Sprintf("received=%d unique=%d dup=%d reordered=%d recovered=%d missing=%d",
		s.Received, s.Unique, s.Duplicates, s.Reordered, s.Recovered, s.Unrecoverable)
}
