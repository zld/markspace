package transport

import (
	"mediaroute/internal/clock"
)

// MaxFrameBytes bounds a reassembled frame; fragmented frames exceeding the
// cap are rejected instead of growing memory without limit.
const MaxFrameBytes = 1 << 20 // 1 MiB

// Frame is a reassembled access unit for one SSRC.
type Frame struct {
	SSRC      uint32
	Timestamp uint32
	Bytes     []byte
	FirstSeq  uint16
	LastSeq   uint16
	// Complete is false when a sequence gap cut the frame before its marker
	// packet arrived, or the stream ended without a marker.
	Complete bool
}

// Reassembler groups ordered RTP packets into frames using the RTP marker
// bit. The caller drives it from a Reorderer: Push for released packets and
// Gap whenever the reorderer reports a new sequence hole. Damage is scoped
// strictly to the frame intersecting a gap; once a new frame starts after
// the hole, subsequent clean frames are complete again.
type Reassembler struct {
	ssrc     uint32
	maxFrame int
	cur      []byte
	curStart uint16
	curTS    uint32
	curHave  bool
	// frameDamaged is true while the frame currently assembling intersects an
	// open gap. It is cleared when a fresh frame starts.
	frameDamaged bool
	// gapOpen latches a gap that arrived while no frame was open, so the next
	// frame started from post-gap packets is marked damaged exactly once.
	gapOpen bool
}

// NewReassembler builds a frame reassembler for one SSRC.
func NewReassembler(ssrc uint32) *Reassembler {
	return &Reassembler{ssrc: ssrc, maxFrame: MaxFrameBytes}
}

// Push feeds one in-order packet. It returns a frame when the marker bit
// closes it, otherwise nil while fragments accumulate.
func (g *Reassembler) Push(p *Packet) (*Frame, error) {
	if p.SSRC != g.ssrc {
		return nil, clock.E(clock.PhaseTransport, p.SSRC, int(p.Seq), true,
			"SSRC mismatch on reassembler", nil)
	}
	if !g.curHave {
		g.curHave = true
		g.curStart = p.Seq
		g.curTS = p.Timestamp
		g.cur = g.cur[:0]
		// A gap latched before this frame started damages exactly this frame.
		if g.gapOpen {
			g.frameDamaged = true
		}
	}
	if len(g.cur)+len(p.Payload) > g.maxFrame {
		return nil, clock.E(clock.PhaseTransport, p.SSRC, int(p.Seq), false,
			"reassembled frame exceeds size cap", nil)
	}
	g.cur = append(g.cur, p.Payload...)
	if p.Marker {
		f := &Frame{
			SSRC:      g.ssrc,
			Timestamp: g.curTS,
			Bytes:     append([]byte(nil), g.cur...),
			FirstSeq:  g.curStart,
			LastSeq:   p.Seq,
			Complete:  !g.frameDamaged,
		}
		g.resetFrame()
		return f, nil
	}
	return nil, nil
}

// Gap signals that the reorderer declared a sequence hole. If a frame is in
// flight it is closed immediately as incomplete; otherwise the damage latches
// onto the next frame that starts. A new frame beginning after the damaged
// one clears the latch, so clean downstream frames stay complete.
func (g *Reassembler) Gap() *Frame {
	g.gapOpen = true
	g.frameDamaged = true
	if !g.curHave {
		return nil
	}
	f := &Frame{
		SSRC:      g.ssrc,
		Timestamp: g.curTS,
		Bytes:     append([]byte(nil), g.cur...),
		FirstSeq:  g.curStart,
		LastSeq:   g.curStart, // true last sequence is inside the lost range
		Complete:  false,
	}
	g.resetFrame()
	// Keep the latch so the post-gap frame is also treated as damaged once.
	g.gapOpen = true
	g.frameDamaged = true
	return f
}

// Flush returns any unterminated frame at stream end, always incomplete
// because no marker ever closed it.
func (g *Reassembler) Flush() *Frame {
	if !g.curHave {
		return nil
	}
	f := &Frame{
		SSRC:      g.ssrc,
		Timestamp: g.curTS,
		Bytes:     append([]byte(nil), g.cur...),
		FirstSeq:  g.curStart,
		LastSeq:   g.curStart,
		Complete:  false,
	}
	g.resetFrame()
	return f
}

// resetFrame ends the current frame. The gap latch is consumed here because
// the frame it damaged has now been emitted; the following frame starts clean.
func (g *Reassembler) resetFrame() {
	g.cur = g.cur[:0]
	g.curHave = false
	g.frameDamaged = false
	g.gapOpen = false
}
