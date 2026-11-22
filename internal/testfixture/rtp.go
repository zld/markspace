package testfixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// RTPSpec configures a generated RTP packet capture.
type RTPSpec struct {
	Seed        int64
	ClockRate   uint32
	PacketCount int
	BaseSeq     uint16
	BaseTS      uint32
	PayloadSize int
}

// GenerateRTP produces an in-order sequence of marshalled RTP packets.
func GenerateRTP(spec RTPSpec) ([][]byte, error) {
	if spec.ClockRate == 0 || spec.PacketCount <= 0 || spec.PayloadSize <= 0 {
		return nil, fmt.Errorf("invalid RTPSpec: %+v", spec)
	}
	rng := rand.New(rand.NewSource(spec.Seed + 1))
	out := make([][]byte, 0, spec.PacketCount)
	step := spec.ClockRate / 50 // 20 ms packets
	for i := 0; i < spec.PacketCount; i++ {
		payload := make([]byte, spec.PayloadSize)
		if _, err := rng.Read(payload); err != nil {
			return nil, err
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: spec.BaseSeq + uint16(i),
				Timestamp:      spec.BaseTS + uint32(i)*step,
				SSRC:           uint32(spec.Seed),
			},
			Payload: payload,
		}
		raw, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

// Flatten concatenates RTP packets with a 2-byte length prefix per packet, the
// simple self-describing capture format used by the demo (no external format
// dependency, fully generated locally).
func Flatten(packets [][]byte) []byte {
	var buf bytes.Buffer
	for _, p := range packets {
		var hdr [2]byte
		binary.BigEndian.PutUint16(hdr[:], uint16(len(p)))
		buf.Write(hdr[:])
		buf.Write(p)
	}
	return buf.Bytes()
}

// SplitFlattened parses the length-prefixed capture produced by Flatten.
func SplitFlattened(raw []byte) ([][]byte, error) {
	var out [][]byte
	for len(raw) >= 2 {
		n := int(binary.BigEndian.Uint16(raw[:2]))
		raw = raw[2:]
		if n == 0 || n > 65535 || len(raw) < n {
			return nil, fmt.Errorf("malformed RTP capture at packet %d", len(out))
		}
		out = append(out, append([]byte(nil), raw[:n]...))
		raw = raw[n:]
	}
	if len(raw) != 0 {
		return nil, fmt.Errorf("trailing %d bytes in RTP capture", len(raw))
	}
	return out, nil
}
