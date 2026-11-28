package fanout

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediapack/internal/render"
)

// Custom payload header (14 bytes), big-endian, followed by frame-hash chunks:
//
//	segment u32 | frame u32 | tick u32 | sourceTrack u8 | sourceSample u8
//
// The marker bit flags the final packet of a frame. Fragmentation makes loss
// and reassembly observable at packet granularity.
type payloadHeader struct {
	segment      uint32
	frame        uint32
	tick         uint32
	sourceTrack  uint8
	sourceSample uint8
}

const headerLen = 14

func encodeHeader(h payloadHeader) []byte {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint32(b[0:4], h.segment)
	binary.BigEndian.PutUint32(b[4:8], h.frame)
	binary.BigEndian.PutUint32(b[8:12], h.tick)
	b[12] = h.sourceTrack
	b[13] = h.sourceSample
	return b
}

func decodeHeader(b []byte) (payloadHeader, error) {
	if len(b) < headerLen {
		return payloadHeader{}, fmt.Errorf("fanout payload shorter than %d bytes", headerLen)
	}
	return payloadHeader{
		segment:      binary.BigEndian.Uint32(b[0:4]),
		frame:        binary.BigEndian.Uint32(b[4:8]),
		tick:         binary.BigEndian.Uint32(b[8:12]),
		sourceTrack:  b[12],
		sourceSample: b[13],
	}, nil
}

// packetize turns one layer's rendered frames into an ordered RTP stream.
// Sequence numbers start at 0 and RTP timestamps carry the shared tick clock.
// It also returns the segment index for every emitted sequence number, so a
// fully-lost segment can still be attributed correctly during reassembly.
func packetize(layer LayerSpec, frames []render.Frame) ([]*rtp.Packet, []int, error) {
	var packets []*rtp.Packet
	seqToSeg := []int{}
	seq := uint16(0)
	for _, f := range frames {
		seg := f.Index / layer.FramesPerSeg
		hdr := payloadHeader{
			segment:      uint32(seg),
			frame:        uint32(f.Index),
			tick:         uint32(f.Tick),
			sourceTrack:  uint8(f.SourceTrack),
			sourceSample: uint8(f.SourceSample),
		}
		// The frame hash is the media content; split it into bounded chunks.
		chunks := splitChunks([]byte(f.Hash), MaxChunkPayload-headerLen)
		for ci, chunk := range chunks {
			payload := encodeHeader(hdr)
			payload = append(payload, chunk...)
			p := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    PayloadType,
					SequenceNumber: seq,
					Timestamp:      uint32(f.Tick),
					SSRC:           layer.SSRC,
					Marker:         ci == len(chunks)-1,
				},
				Payload: payload,
			}
			packets = append(packets, p)
			seqToSeg = append(seqToSeg, seg)
			seq++
		}
	}
	return packets, seqToSeg, nil
}

func splitChunks(data []byte, size int) [][]byte {
	if size <= 0 {
		size = 1
	}
	var out [][]byte
	for i := 0; i < len(data); i += size {
		end := i + size
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[i:end])
	}
	if len(out) == 0 {
		out = [][]byte{{}}
	}
	return out
}
