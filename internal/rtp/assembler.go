package rtp

import (
	"sort"

	"github.com/pion/rtp"
)

// Frame is a reassembled RTP frame: every packet sharing one SSRC and
// RTP timestamp, terminated by a marker packet, or cut off early.
type Frame struct {
	SSRC        uint32
	Timestamp   uint32
	Complete    bool
	GapCount    int
	PacketCount int
	// Payloads concatenated in sequence-number order, independent of
	// the arrival order, so reordered-but-complete frames reassemble
	// deterministically.
	Payload []byte
	// lastSeq is the highest sequence number observed in the frame,
	// used internally to attribute sequence gaps to frames.
	lastSeq uint16
}

type seqPayload struct {
	seq     uint16
	payload []byte
}

type frameState struct {
	ssrc      uint32
	timestamp uint32
	packets   []seqPayload
	lastSeq   uint16
	haveLast  bool
}

// Assembler reassembles RTP packets into frames.
//
// Frames are isolated by SSRC and RTP timestamp; a marker packet ends
// a frame. Sequence gaps are detected by the embedded SequenceChecker
// and attributed to frames at finalization: a gap belongs to the frame
// whose last observed sequence is immediately before the gap. When a
// new timestamp arrives before the open frame got its marker, the open
// frame is flushed incomplete and the new packet starts a fresh frame,
// so later-timestamp data is never merged into an earlier frame.
type Assembler struct {
	checker *SequenceChecker
	open    map[uint32]*frameState
	// flushedTS remembers the timestamp of the last finalized frame
	// per SSRC so late packets cannot reopen a finished frame.
	flushedTS map[uint32]uint32
	frames    []Frame
	late      int
}

// NewAssembler creates an empty assembler.
func NewAssembler() *Assembler {
	return &Assembler{
		checker:   NewSequenceChecker(),
		open:      map[uint32]*frameState{},
		flushedTS: map[uint32]uint32{},
	}
}

// Push processes one RTP packet. It returns a finalized frame when the
// packet terminates a frame (marker) or forces an earlier frame to be
// flushed incomplete (timestamp switch); nil otherwise. Every finalized
// frame is also retained and available through Frames/Stats.
func (a *Assembler) Push(pkt *rtp.Packet) *Frame {
	// Feed the continuity checker; gap accounting is read at Finalize.
	a.checker.Observe(pkt.SSRC, pkt.SequenceNumber)

	var switched *Frame
	fs := a.open[pkt.SSRC]
	if fs != nil && fs.timestamp != pkt.Timestamp {
		// A new timestamp arrived without a marker on the open frame:
		// flush the old frame incomplete; the new packet starts a
		// separate frame and is never merged into the old one.
		switched = a.flush(pkt.SSRC, false)
		fs = nil
	}

	// A late packet for an already finalized frame is discarded rather
	// than resurrecting the frame.
	if fs == nil && a.flushedTS[pkt.SSRC] == pkt.Timestamp {
		a.late++
		return switched
	}

	if fs == nil {
		fs = &frameState{
			ssrc:      pkt.SSRC,
			timestamp: pkt.Timestamp,
		}
		a.open[pkt.SSRC] = fs
	}

	fs.packets = append(fs.packets, seqPayload{
		seq:     pkt.SequenceNumber,
		payload: append([]byte(nil), pkt.Payload...),
	})
	if !fs.haveLast || delta(fs.lastSeq, pkt.SequenceNumber) < seqMod/2 {
		fs.lastSeq = pkt.SequenceNumber
		fs.haveLast = true
	}

	if !pkt.Marker {
		return switched
	}
	ended := a.flush(pkt.SSRC, true)
	if switched != nil {
		// Two frames finalized in this call; the timestamp-switch one
		// is returned here, the marker frame is retained internally.
		return switched
	}
	return ended
}

// flush finalizes the open frame for ssrc. markerSeen=false means the
// frame was cut off by a timestamp switch or stream end and is
// incomplete even if no sequence gap was charged to it.
func (a *Assembler) flush(ssrc uint32, markerSeen bool) *Frame {
	fs := a.open[ssrc]
	if fs == nil {
		return nil
	}
	delete(a.open, ssrc)
	a.flushedTS[ssrc] = fs.timestamp

	// Order packets by sequence number with modulo-65536 ranks so a
	// frame spanning the 65535->0 wraparound sorts correctly.
	base := fs.packets[0].seq
	for _, p := range fs.packets[1:] {
		if delta(base, p.seq) >= seqMod/2 {
			base = p.seq
		}
	}
	sort.SliceStable(fs.packets, func(i, j int) bool {
		return delta(base, fs.packets[i].seq) < delta(base, fs.packets[j].seq)
	})

	var payload []byte
	for _, p := range fs.packets {
		payload = append(payload, p.payload...)
	}

	f := Frame{
		SSRC:        fs.ssrc,
		Timestamp:   fs.timestamp,
		Complete:    markerSeen,
		PacketCount: len(fs.packets),
		Payload:     payload,
		lastSeq:     fs.lastSeq,
	}
	a.frames = append(a.frames, f)
	return &f
}

// Finalize ends a healthy stream: the sequence checker resolves any
// pending holes, gaps are attributed to the frames that lost packets,
// and any frame without a marker is flushed incomplete. Must be called
// once after the last expected packet has been received.
func (a *Assembler) Finalize() []Frame {
	a.checker.Finalize()
	out := a.flushAllOpen()
	a.attributeGaps()
	return out
}

// FlushOpen finalizes any frame still open at end of stream (e.g.
// connection interrupted before a marker). It behaves like Finalize.
func (a *Assembler) FlushOpen() []Frame {
	a.checker.Finalize()
	out := a.flushAllOpen()
	a.attributeGaps()
	return out
}

// EndStream declares the true final sequence number of the stream. It
// lets the checker charge a tail gap that a transport withheld behind
// an unfillable end-of-stream loss, then finalizes frames and
// attributes the gap to the frame that owns it.
func (a *Assembler) EndStream(ssrc uint32, expectedLastSeq uint16) []Frame {
	a.checker.EndStream(ssrc, expectedLastSeq)
	a.checker.Finalize()
	out := a.flushAllOpen()
	a.attributeGaps()
	return out
}

// flushAllOpen writes every open frame, in deterministic SSRC order.
func (a *Assembler) flushAllOpen() []Frame {
	ssrcs := make([]uint32, 0, len(a.open))
	for ssrc := range a.open {
		ssrcs = append(ssrcs, ssrc)
	}
	sort.Slice(ssrcs, func(i, j int) bool { return ssrcs[i] < ssrcs[j] })

	var out []Frame
	for _, ssrc := range ssrcs {
		if f := a.flush(ssrc, false); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

// attributeGaps assigns every recorded sequence gap to the frame that
// actually straddles it (per SSRC) and marks that frame incomplete.
//
// A gap AfterPrev->BeforeNext belongs to the first finalized frame
// whose observed sequences reach at least AfterPrev+1, i.e. the frame
// containing BeforeNext or ending exactly at AfterPrev when the tail
// was lost. Frames are stored in finalization order, which matches
// increasing sequence order within one SSRC, so the first hit wins.
func (a *Assembler) attributeGaps() {
	bySSRC := map[uint32][]int{}
	for i := range a.frames {
		bySSRC[a.frames[i].SSRC] = append(bySSRC[a.frames[i].SSRC], i)
	}

	for _, g := range a.checker.Gaps() {
		idxs := bySSRC[g.SSRC]
		owner := -1
		for _, idx := range idxs {
			// This frame observed a sequence at or beyond the first
			// missing number, so the loss happened inside its range.
			if delta(g.AfterPrev, a.frames[idx].lastSeq) <=
				delta(g.AfterPrev, g.BeforeNext) {
				owner = idx
				break
			}
		}
		if owner == -1 && len(idxs) > 0 {
			// Tail loss with no frame reaching the gap: charge the last
			// frame on the SSRC (it ends at AfterPrev).
			owner = idxs[len(idxs)-1]
		}
		if owner == -1 {
			continue
		}
		a.frames[owner].GapCount += g.MissingCount
		a.frames[owner].Complete = false
	}
}

// Frames returns every finalized frame in finalization order.
func (a *Assembler) Frames() []Frame {
	out := make([]Frame, len(a.frames))
	copy(out, a.frames)
	return out
}

// Stats returns the number of complete and incomplete frames.
func (a *Assembler) Stats() (complete, incomplete int) {
	for _, f := range a.frames {
		if f.Complete {
			complete++
		} else {
			incomplete++
		}
	}
	return complete, incomplete
}

// GapCount returns the total number of missing sequence numbers seen
// by the embedded sequence checker.
func (a *Assembler) GapCount() int { return a.checker.GapCount() }

// Gaps returns the checker's recorded gaps.
func (a *Assembler) Gaps() []Gap { return a.checker.Gaps() }

// LatePackets returns the count of packets dropped because their frame
// was already finalized.
func (a *Assembler) LatePackets() int { return a.late }
