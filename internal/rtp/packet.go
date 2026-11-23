// Package rtp parses RTP packets with pion/rtp and extends the 16-bit
// sequence number to a 64-bit monotonic index, then deduplicates and
// reorders packets per SSRC within a bounded window.
package rtp

import (
	"github.com/pion/rtp"

	"mediaverify/internal/errs"
)

// Packet is a parsed RTP packet on the shared, monotonic sequence space.
type Packet struct {
	SSRC       uint32
	ExtSeq     uint64 // 64-bit extended sequence number
	Timestamp  uint32 // raw RTP media timestamp
	Marker     bool
	Payload    []byte
	PayloadTyp byte
}

// Extender unwraps 16-bit sequence numbers into a monotonic 64-bit space
// independently for each SSRC. It also detects backward timestamp jumps.
type Extender struct {
	lastSeq map[uint32]uint16
	cycles  map[uint32]uint64
	started map[uint32]bool
	// lastTS tracks the previous raw timestamp per SSRC for rollover checks.
	lastTS map[uint32]uint32
	// BackwardJumps counts non-wrapping timestamp decreases.
	BackwardJumps int
}

// NewExtender builds an empty extender.
func NewExtender() *Extender {
	return &Extender{
		lastSeq: map[uint32]uint16{},
		cycles:  map[uint32]uint64{},
		started: map[uint32]bool{},
		lastTS:  map[uint32]uint32{},
	}
}

// Extend converts a pion packet into our Packet with an extended sequence.
func (e *Extender) Extend(p *rtp.Packet) (Packet, error) {
	if p == nil {
		return Packet{}, errs.New(errs.KindInvalidInput, "rtp", "nil packet")
	}
	ssrc := p.SSRC
	seq := p.SequenceNumber
	if !e.started[ssrc] {
		e.started[ssrc] = true
		e.lastSeq[ssrc] = seq
		e.cycles[ssrc] = 0
		e.lastTS[ssrc] = p.Timestamp
	} else {
		prev := e.lastSeq[ssrc]
		delta := int64(seq) - int64(prev)
		switch {
		case delta == 0:
			// Duplicate: keep current cycle count.
		case delta > 0 && delta < 1<<15:
			// Forward within the same cycle.
		case delta < 0 && delta > -(1<<15):
			// Ordinary backward reorder: no cycle change.
		case delta <= -(1 << 15):
			// Raw sequence fell from high to low: a genuine wrap,
			// unless the RTP timestamp contradicts it (a late packet
			// would carry an older timestamp).
			if e.timestampFollows(ssrc, p.Timestamp) {
				e.cycles[ssrc] += 1 << 16
			}
		default:
			// Large positive delta: a far-late packet from an older
			// cycle. Roll the cycle back when the timestamp confirms
			// the packet predates the current one.
			if !e.timestampFollows(ssrc, p.Timestamp) && e.cycles[ssrc] >= 1<<16 {
				e.cycles[ssrc] -= 1 << 16
			}
		}
		e.lastSeq[ssrc] = seq
		// A modest timestamp decrease without a 32-bit wrap is an anomaly;
		// full wraps and equal timestamps are normal.
		tsDelta := int64(p.Timestamp) - int64(e.lastTS[ssrc])
		if tsDelta < 0 && tsDelta > -(1<<31) {
			e.BackwardJumps++
		}
		e.lastTS[ssrc] = p.Timestamp
	}
	return Packet{
		SSRC:       ssrc,
		ExtSeq:     e.cycles[ssrc] | uint64(seq),
		Timestamp:  p.Timestamp,
		Marker:     p.Marker,
		Payload:    append([]byte(nil), p.Payload...),
		PayloadTyp: p.PayloadType,
	}, nil
}

// timestampFollows reports whether ts is at or after the stream's last RTP
// timestamp, treating a 32-bit unsigned wrap as forward motion.
func (e *Extender) timestampFollows(ssrc uint32, ts uint32) bool {
	prev := e.lastTS[ssrc]
	d := int64(ts) - int64(prev)
	return d >= 0 || d <= -(1<<31)
}

// Parse decodes raw bytes through pion and runs extension in one step.
func (e *Extender) Parse(raw []byte) (Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		return Packet{}, errs.New(errs.KindInvalidInput, "rtp", "unmarshal: %v", err)
	}
	return e.Extend(p)
}
