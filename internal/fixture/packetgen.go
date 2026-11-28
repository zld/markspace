package fixture

import (
	"bytes"
	"encoding/binary"

	"github.com/pion/rtp"

	"mediasync/internal/stream"
)

// RTPClockRate is the shared clock rate (ticks per second) of every generated
// packet stream. 1000 makes one timestamp unit exactly one millisecond, the
// same unit as the generated 1kHz container tracks, so alignment arithmetic
// stays exact and rational.
const RTPClockRate = 1000

// PacketPayloadSize is the fixed RTP payload length in every fixture. Fixed
// lengths keep single-packet XOR recovery well defined.
const PacketPayloadSize = 8

// frame wraps one record in the stream's length-prefixed binary format.
func frame(kind byte, payload []byte) []byte {
	b := make([]byte, stream.FrameHeaderSize+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	b[4] = kind
	copy(b[5:], payload)
	return b
}

func rtpPacket(seq uint16, ts uint32, payload []byte) (*rtp.Packet, []byte) {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x4d53594e, // "MSYN"
			PayloadType:    96,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return p, raw
}

func payloadFor(seq int) []byte {
	p := make([]byte, PacketPayloadSize)
	for i := range p {
		p[i] = byte(seq*7 + i)
	}
	return p
}

// PacketOptions controls the synthesized packet stream.
type PacketOptions struct {
	Count          int
	StartTimestamp uint32
	// TimestampStep is the per-packet RTP timestamp increment.
	TimestampStep uint32
	// DropSequence, when >= 0, omits that RTP packet to simulate a loss.
	DropSequence int
	// IncludeXOR emits a single XOR redundancy frame covering all packets.
	IncludeXOR bool
}

// BuildPackets renders the framed packets.bin content for one scenario.
func BuildPackets(opts PacketOptions) []byte {
	var out bytes.Buffer
	payloads := make([][]byte, opts.Count)
	for i := 0; i < opts.Count; i++ {
		payloads[i] = payloadFor(i)
	}

	for i := 0; i < opts.Count; i++ {
		if i == opts.DropSequence {
			continue
		}
		ts := opts.StartTimestamp + uint32(i)*opts.TimestampStep
		_, raw := rtpPacket(uint16(i), ts, payloads[i])
		out.Write(frame(stream.KindRTP, raw))
	}

	if opts.IncludeXOR {
		xor := make([]byte, PacketPayloadSize)
		for _, pl := range payloads {
			for j := range xor {
				xor[j] ^= pl[j]
			}
		}
		hdr := make([]byte, 12)
		binary.BigEndian.PutUint32(hdr[0:4], 0) // base sequence
		binary.BigEndian.PutUint32(hdr[4:8], uint32(opts.Count))
		binary.BigEndian.PutUint32(hdr[8:12], PacketPayloadSize)
		out.Write(frame(stream.KindXOR, append(hdr, xor...)))
	}
	return out.Bytes()
}
