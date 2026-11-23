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
	// Complete is false when the stream ended or a sequence gap cut the
	// frame before its marker packet arrived.
	Complete bool
}

// Reassembler groups ordered RTP packets into frames using the RTP marker
// bit. Input must come from a Reorderer so sequence gaps are already known;
// the caller signals a gap by calling Gap before pushing later packets.
type Reassembler struct {
	ssrc     uint32
	maxFrame int
	cur      []byte
	curStart uint16
	curTS    uint32
	curHave  bool
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
			Complete:  true,
		}
		g.reset()
		return f, nil
	}
	return nil, nil
}

// Gap closes the frame currently being built as incomplete, preserving the
// bytes that did arrive so downstream stages can see (but never trust) the
// truncated time range.
func (g *Reassembler) Gap() *Frame {
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
	g.reset()
	return f
}

// Flush returns any unterminated frame at stream end, marked incomplete.
func (g *Reassembler) Flush() *Frame { return g.Gap() }

func (g *Reassembler) reset() {
	g.cur = g.cur[:0]
	g.curHave = false
}
