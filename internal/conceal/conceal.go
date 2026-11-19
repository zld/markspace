// Package conceal builds substitution frames for audio that failed to
// arrive by its playout deadline, and tags output continuity.
package conceal

import "jitterlab/internal/protocol"

// Frame is one playout output slot: either a real decoded frame or a
// concealment substitute.
type Frame struct {
	StreamID uint32
	Seq      uint32
	// Concealed marks frames synthesized rather than received.
	Concealed bool
	// Unrecoverable marks concealment runs that reached the configured
	// threshold; the decoder should treat the stream as broken here.
	Unrecoverable bool
	Payload       []byte
}

// FromReal wraps a successfully received packet.
func FromReal(p protocol.Packet) Frame {
	return Frame{StreamID: p.StreamID, Seq: p.Seq, Payload: p.Payload}
}

// Substitute builds a concealment frame for seq from the most recent valid
// frame. The payload is an attenuated copy of the last good audio (a cheap
// fade that avoids clicks); with no history the payload is empty. run is
// the current consecutive concealment count, threshold the limit at which
// the gap is declared unrecoverable.
func Substitute(last *protocol.Packet, streamID, seq uint32, run, threshold int) Frame {
	f := Frame{
		StreamID:      streamID,
		Seq:           seq,
		Concealed:     true,
		Unrecoverable: threshold > 0 && run >= threshold,
	}
	if last != nil && len(last.Payload) > 0 {
		out := make([]byte, len(last.Payload))
		for i, b := range last.Payload {
			out[i] = b / 2
		}
		f.Payload = out
	}
	return f
}
