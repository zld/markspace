package rtp

// Frame is a reassembled RTP frame: all packets of one SSRC sharing one RTP
// timestamp, closed by a marker bit.
type Frame struct {
	SSRC       uint32
	Timestamp  uint32
	// Packets are the ordered packets that make up the frame.
	Packets []*Packet
	// Complete is false when the frame was flushed because the timestamp
	// changed (or the reassembler was reset) without a marker bit.
	Complete bool
	// FirstSeq and LastSeq are the 16-bit sequence numbers of the frame's
	// first and last packets.
	FirstSeq uint16
	LastSeq  uint16
}

// FrameStats counts reassembly outcomes.
type FrameStats struct {
	// CompleteFrames closed by a marker bit.
	CompleteFrames int
	// IncompleteFrames flushed on timestamp switch without a marker.
	IncompleteFrames int
}

type streamState struct {
	ssrc      uint32
	timestamp uint32
	frame     *Frame
}

// Reassembler groups reordered RTP packets into frames. Streams are isolated
// by SSRC, and within a stream by RTP timestamp.
type Reassembler struct {
	streams map[uint32]*streamState
	stats   FrameStats
}

// NewReassembler builds an empty Reassembler.
func NewReassembler() *Reassembler {
	return &Reassembler{streams: make(map[uint32]*streamState)}
}

// Stats returns a copy of the current frame counters.
func (f *Reassembler) Stats() FrameStats { return f.stats }

// FlushIncomplete closes every open frame as incomplete and returns it. Use it
// when a stream ends normally (TEARDOWN) so unclosed frames are reported
// rather than silently lost.
func (f *Reassembler) FlushIncomplete() []Frame {
	var flushed []Frame
	for ssrc, st := range f.streams {
		if st.frame != nil {
			st.frame.Complete = false
			f.stats.IncompleteFrames++
			flushed = append(flushed, *st.frame)
			st.frame = nil
		}
		delete(f.streams, ssrc)
	}
	return flushed
}

// Reset drops every open frame without counting and clears all stream state,
// so a hard disconnect cannot leak partial frames into the next run.
func (f *Reassembler) Reset() {
	for ssrc := range f.streams {
		delete(f.streams, ssrc)
	}
	f.stats = FrameStats{}
}

// Push adds one already reordered packet. It returns frames that become
// complete (marker) or that are force-closed because the timestamp changed
// without a marker. Returned frames are owned by the caller.
func (f *Reassembler) Push(pkt *Packet) []Frame {
	st, ok := f.streams[pkt.Header.SSRC]
	if !ok {
		st = &streamState{ssrc: pkt.Header.SSRC}
		f.streams[pkt.Header.SSRC] = st
	}

	var out []Frame
	// Timestamp switch: any open frame without a marker is incomplete.
	if st.frame != nil && st.frame.Timestamp != pkt.Header.Timestamp {
		st.frame.Complete = false
		f.stats.IncompleteFrames++
		out = append(out, *st.frame)
		st.frame = nil
	}

	if st.frame == nil {
		st.frame = &Frame{
			SSRC:      pkt.Header.SSRC,
			Timestamp: pkt.Header.Timestamp,
			FirstSeq:  pkt.Header.SequenceNumber,
			Packets:   make([]*Packet, 0, 4),
		}
	}
	st.timestamp = pkt.Header.Timestamp
	st.frame.Packets = append(st.frame.Packets, pkt)
	st.frame.LastSeq = pkt.Header.SequenceNumber

	if pkt.Header.Marker {
		st.frame.Complete = true
		f.stats.CompleteFrames++
		out = append(out, *st.frame)
		st.frame = nil
	}
	return out
}
