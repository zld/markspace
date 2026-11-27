// Package fixture generates all sample media locally: deterministic elementary
// streams, key-frame index containers, RTP packet feeds, multi-version assets
// and bandwidth profiles.  No external media files or network streams are used.
//
// The same seed always yields the same bytes, and every injection records the
// exact byte offset or packet position it touched so tests can assert the
// reported damage location matches the injected one within one byte.
package fixture

import (
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"

	"mediapipeline/internal/ingest"
)

// FrameCount is the number of picture units generated per stream.
const FrameCount = 12

// KeyStride puts a key frame every KeyStride units (indices 0,4,8).
const KeyStride = 4

// ssrc is the single synchronisation source used by generated feeds.
const ssrc uint32 = 0x5151A0A1

// AV1 OBU type values (AV1 spec 6.2.2), mirrored here because the ingest
// package keeps its own constants unexported.
const (
	obuFrame    = 6
	obuKeyFrame = 7
)

// Injection names recognised by Generate.
const (
	InjectTruncate      = "truncate"
	InjectIndexOOB      = "index-oob"
	InjectDuplicate     = "duplicate"
	InjectReorder       = "reorder"
	InjectBurstLoss     = "burst-loss"
	InjectBandwidthDrop = "bandwidth-drop"
)

// BandwidthProfile is the bytes-per-second budget at each planning round.
type BandwidthProfile struct {
	NormalBps  int64
	DroppedBps int64 // used during the drop window when bandwidth-drop is injected
}

// DefaultBandwidth keeps the clean demo comfortably inside every limit.
func DefaultBandwidth() BandwidthProfile {
	return BandwidthProfile{NormalBps: 8_000_000, DroppedBps: 64}
}

// rng builds the deterministic source of payload bytes for a seed.
func rng(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// frameType returns the codec-specific header byte for the unit at idx.
func frameType(codec ingest.Codec, idx int) byte {
	key := idx%KeyStride == 0
	switch codec {
	case ingest.CodecH264:
		if key {
			return 5 // IDR slice
		}
		return 1 // non-IDR slice
	case ingest.CodecH265:
		if key {
			return 19 << 1 // IDR_W_RADL in the high six bits
		}
		return 1 << 1 // TRAIL_R
	default: // AV1 OBU header byte
		if key {
			return obuKeyFrame << 3
		}
		return obuFrame << 3
	}
}

// framePayload returns the deterministic payload body for one unit.  For
// H.264/H.265 the body starts with the NAL header byte(s); for AV1 it is the
// raw OBU payload, framed separately by av1OBU.
func framePayload(codec ingest.Codec, idx int, r *rand.Rand) []byte {
	n := 128 + r.Intn(256) // 128..383 payload bytes
	p := make([]byte, n)
	r.Read(p)
	switch codec {
	case ingest.CodecH264:
		p[0] = frameType(codec, idx)
	case ingest.CodecH265:
		p[0] = frameType(codec, idx) // HVCC NAL header occupies two bytes
		p[1] = byte(r.Intn(256))
	case ingest.CodecAV1:
		// Leave the body untouched; the OBU header is added during framing.
	}
	return p
}

// buildUnits returns (framed stream bytes, per-unit payload slices) for codec.
// For AV1 the payload slice is the OBU body the scanner reports, keeping
// PayloadOff+len(Payload) tightly packed against the next unit.
func buildUnits(codec ingest.Codec, seed int64) ([]byte, [][]byte) {
	r := rng(seed)
	var stream []byte
	var payloads [][]byte
	for idx := 0; idx < FrameCount; idx++ {
		body := framePayload(codec, idx, r)
		if codec == ingest.CodecAV1 {
			stream = append(stream, av1OBU(frameType(codec, idx), body)...)
			payloads = append(payloads, body)
			continue
		}
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
		stream = append(stream, prefix[:]...)
		stream = append(stream, body...)
		payloads = append(payloads, body)
	}
	return stream, payloads
}

// av1OBU frames an OBU body in the low-overhead bitstream format: one header
// byte (type in bits 6..3, has_size set), the leb128 body size and the body.
func av1OBU(header byte, body []byte) []byte {
	out := []byte{header | 0x02}
	out = append(out, leb128(uint64(len(body)))...)
	out = append(out, body...)
	return out
}

// leb128 encodes v as unsigned LEB128.
func leb128(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			out = append(out, b)
			return out
		}
	}
}

// packetise wraps each unit payload in one RTP datagram with sequential
// sequence numbers and a 90 kHz timestamp, returning the canonical in-order
// feed before any transport injection.
func packetise(payloads [][]byte) [][]byte {
	out := make([][]byte, 0, len(payloads))
	for i, p := range payloads {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * 3000), // 30 fps on a 90 kHz clock
				SSRC:           ssrc,
				Marker:         i%KeyStride == KeyStride-1,
			},
			Payload: append([]byte(nil), p...),
		}
		raw, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		out = append(out, raw)
	}
	return out
}
