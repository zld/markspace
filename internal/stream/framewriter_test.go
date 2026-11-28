package stream

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"
)

func writeStream(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "packets.bin")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func frame(kind byte, payload []byte) []byte {
	b := make([]byte, FrameHeaderSize+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(payload)))
	b[4] = kind
	copy(b[5:], payload)
	return b
}

func rtpFrame(seq uint16, ts uint32, payload []byte) []byte {
	p := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x12345678,
			PayloadType:    96,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return frame(KindRTP, raw)
}

func xorFrame(base uint16, count uint32, xorPayload []byte) []byte {
	hdr := make([]byte, xorHeaderSize)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(base))
	binary.BigEndian.PutUint32(hdr[4:8], count)
	binary.BigEndian.PutUint32(hdr[8:12], uint32(len(xorPayload)))
	return frame(KindXOR, append(hdr, xorPayload...))
}

// xorBytes returns the byte-wise XOR of all supplied equal-length payloads.
func xorBytes(payloads ...[]byte) []byte {
	out := make([]byte, len(payloads[0]))
	for _, pl := range payloads {
		for i := range out {
			out[i] ^= pl[i]
		}
	}
	return out
}
