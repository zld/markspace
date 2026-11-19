// Package frame reassembles RTP packets into frames keyed by SSRC and
// RTP timestamp. Frames closed by a marker bit are complete; frames
// abandoned by a timestamp switch, session end or a buffer-limit
// eviction are reported as incomplete and never masquerade as whole
// frames.
package frame

import (
	"errors"
	"sort"

	"rtspcheck/internal/rtp"
)

// ErrBufferLimit is returned when the per-frame packet cap is reached.
var ErrBufferLimit = errors.New("frame: packet buffer limit reached")

// DefaultMaxBufferedFrames bounds the number of frames held at once.
const DefaultMaxBufferedFrames = 256

// Frame is one reassembled frame.
type Frame struct {
	SSRC      uint32
	Timestamp uint32
	// Packets are the constituent RTP packets in arrival order; the
	// reorderer normally supplies them in sequence order.
	Packets []*rtp.Packet
	// Complete is true only when a marker packet closed the frame.
	Complete bool
	// Size is the total payload byte count.
	Size int
}

// Stats counts reassembly outcomes.
type Stats struct {
	Complete   int
	Incomplete int
	Evicted    int // frames dropped by the buffered-frame limit
}

// pendingFrame accumulates packets until a marker or a switch.
type pendingFrame struct {
	frame Frame
}

// Assembler groups packets by (SSRC, timestamp) and closes frames.
type Assembler struct {
	maxPackets int
	maxFrames  int
	pending    map[uint32]*pendingFrame // one open frame per SSRC
	order      []uint32                  // insertion order of SSRCs
	seenTS     map[uint32]map[uint32]struct{}
	stats      Stats
}

// NewAssembler builds an assembler. maxPacketsPerFrame caps packets in
// a single frame and maxBufferedFrames caps concurrently open frames;
// non-positive values select conservative defaults.
func NewAssembler(maxPacketsPerFrame, maxBufferedFrames int) *Assembler {
	if maxPacketsPerFrame <= 0 {
		maxPacketsPerFrame = 512
	}
	if maxBufferedFrames <= 0 {
		maxBufferedFrames = DefaultMaxBufferedFrames
	}
	return &Assembler{
		maxPackets: maxPacketsPerFrame,
		maxFrames:  maxBufferedFrames,
		pending:    make(map[uint32]*pendingFrame),
		order:      make([]uint32, 0, 8),
		seenTS:     make(map[uint32]map[uint32]struct{}),
	}
}

// Push adds a packet to the frame identified by its SSRC and
// timestamp. It returns the closed frame when the packet carries a
// marker, plus any frame abandoned by a timestamp switch. An
// over-cap frame is released as incomplete together with
// ErrBufferLimit; processing continues with the new packet.
func (a *Assembler) Push(p *rtp.Packet) ([]Frame, error) {
	out := make([]Frame, 0, 2)
	var limitErr error

	pf, ok := a.pending[p.SSRC]
	if !ok {
		a.enforceFrameLimit(&out)
		a.order = append(a.order, p.SSRC)
		pf = &pendingFrame{frame: Frame{SSRC: p.SSRC, Timestamp: p.Timestamp}}
		a.pending[p.SSRC] = pf
	} else if pf.frame.Timestamp != p.Timestamp {
		// Timestamp changed without a marker: the previous frame
		// is incomplete by definition.
		out = append(out, a.closeIncomplete(p.SSRC))
		pf = &pendingFrame{frame: Frame{SSRC: p.SSRC, Timestamp: p.Timestamp}}
		a.pending[p.SSRC] = pf
	}

	if len(pf.frame.Packets) >= a.maxPackets {
		out = append(out, a.closeIncomplete(p.SSRC))
		limitErr = ErrBufferLimit
		pf = &pendingFrame{frame: Frame{SSRC: p.SSRC, Timestamp: p.Timestamp}}
		a.pending[p.SSRC] = pf
	}

	pf.frame.Packets = append(pf.frame.Packets, p)
	pf.frame.Size += len(p.Payload)

	tsSet, ok := a.seenTS[p.SSRC]
	if !ok {
		tsSet = make(map[uint32]struct{})
		a.seenTS[p.SSRC] = tsSet
	}
	tsSet[p.Timestamp] = struct{}{}

	if p.Marker {
		out = append(out, a.closeComplete(p.SSRC))
	}
	return out, limitErr
}

func (a *Assembler) closeComplete(ssrc uint32) Frame {
	pf := a.pending[ssrc]
	delete(a.pending, ssrc)
	a.removeOrder(ssrc)
	pf.frame.Complete = true
	a.stats.Complete++
	return pf.frame
}

func (a *Assembler) closeIncomplete(ssrc uint32) Frame {
	pf := a.pending[ssrc]
	delete(a.pending, ssrc)
	a.removeOrder(ssrc)
	pf.frame.Complete = false
	a.stats.Incomplete++
	return pf.frame
}

// enforceFrameLimit evicts the oldest open frame (incomplete) when
// the number of concurrently buffered frames hits the cap.
func (a *Assembler) enforceFrameLimit(out *[]Frame) {
	if len(a.pending) < a.maxFrames {
		return
	}
	oldest := a.order[0]
	*out = append(*out, a.closeIncomplete(oldest))
	a.stats.Evicted++
}

func (a *Assembler) removeOrder(ssrc uint32) {
	for i, v := range a.order {
		if v == ssrc {
			a.order = append(a.order[:i], a.order[i+1:]...)
			return
		}
	}
}

// Flush ends the session: every still-open frame is released as
// incomplete. Frames are returned in deterministic SSRC order so
// repeated runs produce identical output.
func (a *Assembler) Flush() []Frame {
	ssrcs := make([]uint32, 0, len(a.pending))
	for ssrc := range a.pending {
		ssrcs = append(ssrcs, ssrc)
	}
	sort.Slice(ssrcs, func(i, j int) bool { return ssrcs[i] < ssrcs[j] })
	out := make([]Frame, 0, len(ssrcs))
	for _, ssrc := range ssrcs {
		out = append(out, a.closeIncomplete(ssrc))
	}
	a.order = a.order[:0]
	return out
}

// Stats returns a copy of the reassembly counters.
func (a *Assembler) Stats() Stats { return a.stats }

// FramesAccounted returns how many distinct (SSRC, timestamp) frames
// have started arriving, including frames still open because their
// marker was lost. It is the live completion signal: terminal
// incomplete counts are only finalized by the end-of-session Flush.
func (a *Assembler) FramesAccounted() int {
	n := 0
	for _, tsSet := range a.seenTS {
		n += len(tsSet)
	}
	return n
}

// Pending reports how many frames are currently open.
func (a *Assembler) Pending() int { return len(a.pending) }
