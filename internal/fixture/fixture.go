// Package fixture generates self-contained synthetic input for the demo and
// tests: RTP datagrams carrying Opus or AAC payloads, including clean streams,
// loss/duplicate scenarios and truncated packets. No external files are used.
package fixture

import (
	"math/rand"

	"github.com/pion/rtp"
)

// Datagram is one wire RTP packet plus its input byte offset.
type Datagram struct {
	Data   []byte
	Offset int64
}

// Kind selects the anomaly pattern baked into a generated stream.
type Kind string

const (
	KindClean     Kind = "clean"
	KindLossDup   Kind = "lossdup"
	KindTruncated Kind = "truncated"
	KindConflict  Kind = "conflict"
)

// Spec configures a generated stream.
type Spec struct {
	Seed       int64
	Kind       Kind
	Codec      string // "opus" or "aac"
	Rate       uint32
	PacketCount int
	PayloadSize int
}

// Build produces ordered (on the wire, before any reordering) datagrams. The
// offset increments by the full datagram length so errors can report it.
func Build(s Spec) ([]Datagram, error) {
	rng := rand.New(rand.NewSource(s.Seed))
	var out []Datagram
	var offset int64
	seqBase := uint16(100)

	emit := func(seq uint16, ts uint32, payload []byte) {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           uint32(s.Seed),
			},
			Payload: payload,
		}
		b, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		out = append(out, Datagram{Data: b, Offset: offset})
		offset += int64(len(b))
	}

	for i := 0; i < s.PacketCount; i++ {
		seq := seqBase + uint16(i)
		ts := uint32(i) * 960
		payload := synthPayload(rng, s, i)

		switch s.Kind {
		case KindLossDup:
			// Drop packet index 3, duplicate index 5 (identical re-reception).
			if i == 3 {
				continue
			}
			emit(seq, ts, payload)
			if i == 5 {
				emit(seq, ts, payload) // same seq, same bytes -> duplicate
			}
		case KindTruncated:
			if i == 4 {
				emit(seq, ts, payload[:len(payload)/2]) // cut short
				continue
			}
			emit(seq, ts, payload)
		case KindConflict:
			// Packet index 6 is received twice with the same sequence number
			// but a different payload: a same-sequence conflict, not a dup.
			emit(seq, ts, payload)
			if i == 6 {
				conflictPayload := append([]byte(nil), payload...)
				conflictPayload[len(conflictPayload)-1] ^= 0xFF
				emit(seq, ts, conflictPayload)
			}
		default:
			emit(seq, ts, payload)
		}
	}
	return out, nil
}

// synthPayload builds a structurally valid codec payload of the requested size.
// The audio content is synthetic; only packet framing matters to the parser.
func synthPayload(rng *rand.Rand, s Spec, idx int) []byte {
	size := s.PayloadSize
	if size < 4 {
		size = 4
	}
	p := make([]byte, size)
	headerLen := 1
	if s.Codec == "opus" {
		// TOC: config 1 (SILK 20ms), mono, code 0 (single frame).
		p[0] = byte(1) << 3
	} else {
		// ADTS header for a 48k stereo frame, frame length covering the body.
		headerLen = 7
		copy(p, codecADTS(size))
	}
	for i := headerLen; i < size; i++ {
		p[i] = byte(rng.Intn(256))
	}
	return p
}

// codecADTS returns a 7-byte ADTS header (48kHz, stereo, protection absent).
func codecADTS(frameLen int) []byte {
	h := make([]byte, 7)
	h[0] = 0xFF
	h[1] = 0xF1 // MPEG-4, layer 0, protection absent
	h[2] = (3 << 2) | (2 >> 2) // freq idx 3 (48k), channel high bit
	h[3] = (2&0x03)<<6 | byte((frameLen>>11)&0x03)
	h[4] = byte((frameLen >> 3) & 0xFF)
	h[5] = byte((frameLen&0x07)<<5) | 0x1F
	h[6] = 0x1C
	return h
}
