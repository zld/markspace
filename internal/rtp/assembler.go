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
}

type seqPayload struct {
	seq     uint16
	payload []byte
}

type frameState struct {
	ssrc      uint32
	timestamp uint32
	packets   []seqPayload
	gapCount  int
	complete  bool
}

// Assembler reassembles RTP packets into frames.
//
// Frames are isolated by SSRC and RTP timestamp; a marker packet ends
// a frame. Sequence gaps are detected through the embedded
// SequenceChecker and mark the affected frame incomplete. When a new
// timestamp arrives before the open frame got its marker, the open
// frame is flushed incomplete and the new packet starts a fresh
// frame: later-timestamp data is never merged into an earlier frame.
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
	gap := a.checker.Observe(pkt.SSRC, pkt.SequenceNumber)

	var switched *Frame
	fs := a.open[pkt.SSRC]
	if fs != nil && fs.timestamp != pkt.Timestamp {
		// A new timestamp arrived without a marker on the open frame.
		// If a sequence gap was observed on this first packet of the
		// new frame, the missing numbers belonged to the old frame's
		// tail: charge the gap to the old frame before flushing it.
		if gap != nil {
			fs.complete = false
			fs.gapCount += gap.MissingCount
			gap = nil
		}
		// The old frame is flushed incomplete and the new packet must
		// never be merged into it.
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
			complete:  true,
		}
		a.open[pkt.SSRC] = fs
	}

	if gap != nil {
		// Same-timestamp gap: the missing packets belong to this frame.
		fs.complete = false
		fs.gapCount += gap.MissingCount
	}

	fs.packets = append(fs.packets, seqPayload{
		seq:     pkt.SequenceNumber,
		payload: append([]byte(nil), pkt.Payload...),
	})

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
		Complete:    fs.complete && markerSeen,
		GapCount:    fs.gapCount,
		PacketCount: len(fs.packets),
		Payload:     payload,
	}
	a.frames = append(a.frames, f)
	return &f
}

// FlushOpen finalizes any frame still open at end of stream (e.g.
// connection interrupted before a marker). Such frames are incomplete.
func (a *Assembler) FlushOpen() []Frame {
	// Deterministic SSRC order for callers consuming the returned slice.
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
