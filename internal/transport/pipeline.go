// Package transport adapts the two RTSP media transports — UDP datagrams and
// TCP-interleaved channels — onto the same ingest pipeline.
package transport

import (
	"sync"

	rtpx "rtspingest/internal/rtp"

	"github.com/pion/rtp"
)

// Stats is the transport-neutral snapshot used by the report module.
type Stats struct {
	PacketsReceived   uint64
	DuplicatePackets  uint64
	SeqGaps           uint64
	LateOutOfWindow   uint64
	ReassembledFrames uint64
	DiscardedFrames   uint64
	MissingFragments  uint64
	IllegalFUHeaders  uint64
	OversizedFrames   uint64
	MalformedPackets  uint64
	CompleteFrames    int
}

// Pipeline chains the RTP reorder buffer and the H.264 reassembler. One
// pipeline is created per transport run, so the UDP and TCP paths keep
// independent counters even though they share the exact same code.
type Pipeline struct {
	mu       sync.Mutex
	reorder  *rtpx.ReorderBuffer
	h264     *rtpx.Reassembler
	received uint64
	frames   []*rtpx.Frame
}

// NewPipeline creates an empty ingest pipeline.
func NewPipeline() *Pipeline {
	return &Pipeline{
		reorder: rtpx.NewReorderBuffer(),
		h264:    rtpx.NewReassembler(),
	}
}

// HandlePacket feeds one RTP packet as delivered by the server callback.
// The packet first enters the reorder buffer; every packet released in
// sequence order is then offered to the H.264 reassembler.
func (p *Pipeline) HandlePacket(pkt *rtp.Packet) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.received++
	for _, ordered := range p.reorder.Push(pkt) {
		frame, err := p.h264.Process(ordered)
		if err != nil {
			continue // counters are maintained inside the reassembler
		}
		if frame != nil {
			p.frames = append(p.frames, frame)
		}
	}
}

// Close drains both stages: packets still parked in the reorder window are
// delivered first, then any access units buffered at end of stream are
// emitted or discarded.
func (p *Pipeline) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, pkt := range p.reorder.Flush() {
		frame, err := p.h264.Process(pkt)
		if err != nil {
			continue
		}
		if frame != nil {
			p.frames = append(p.frames, frame)
		}
	}
	p.frames = append(p.frames, p.h264.Flush()...)
}

// Snapshot returns the current counters without mutating the pipeline.
func (p *Pipeline) Snapshot() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()

	rs := p.reorder.Stats()
	hs := p.h264.Stats()
	return Stats{
		PacketsReceived:   p.received,
		DuplicatePackets:  rs.Duplicates,
		SeqGaps:           rs.Gaps,
		LateOutOfWindow:   rs.LateOutOfWindow,
		ReassembledFrames: hs.ReassembledFrames,
		DiscardedFrames:   hs.DiscardedFrames,
		MissingFragments:  hs.MissingFragments,
		IllegalFUHeaders:  hs.IllegalFUHeaders,
		OversizedFrames:   hs.OversizedFrames,
		MalformedPackets:  hs.MalformedPackets,
		CompleteFrames:    len(p.frames),
	}
}

// Frames returns the reassembled frames collected so far.
func (p *Pipeline) Frames() []*rtpx.Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*rtpx.Frame, len(p.frames))
	copy(out, p.frames)
	return out
}
