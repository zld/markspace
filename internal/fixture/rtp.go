package fixture

import (
	"bytes"
	"encoding/binary"
	"math/rand"

	"github.com/pion/rtp"
)

// RTPSpec configures a generated length-prefixed RTP capture.
type RTPSpec struct {
	ClockRate   uint32
	Packets     int
	StartSeq    uint16
	StartTS     uint32
	TSIncrement uint32 // timestamp delta per packet (before wrap effects)
	ID3Every    int    // >0: every nth packet carries an ID3 payload
	LossSeq     []uint16 // sequence numbers to omit (packet loss injection)
	Shuffle     bool     // swap one adjacent pair (reordering injection)
}

// RTPInput is a generated capture with ground truth.
type RTPInput struct {
	Bytes       []byte
	WrittenSeq  []uint16
	ID3Indices  []int
}

// GenerateRTP builds a deterministic RTP capture stream.
func GenerateRTP(seed int64, spec RTPSpec) RTPInput {
	rng := rand.New(rand.NewSource(seed))
	loss := map[uint16]bool{}
	for _, s := range spec.LossSeq {
		loss[s] = true
	}
	out := RTPInput{}
	var buf bytes.Buffer

	type frame struct {
		seq uint16
		raw []byte
	}
	var frames []frame
	for i := 0; i < spec.Packets; i++ {
		seq := spec.StartSeq + uint16(i)
		if loss[seq] {
			continue
		}
		ts := spec.StartTS + uint32(i)*spec.TSIncrement
		payload := []byte{byte(0xA0 + (i % 40)), byte(i), byte(seq), byte(ts)}
		payload = append(payload, make([]byte, 8+rng.Intn(8))...)
		if spec.ID3Every > 0 && i%spec.ID3Every == 0 {
			payload = buildID3(i)
			out.ID3Indices = append(out.ID3Indices, len(frames))
		}
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: seq, Timestamp: ts, SSRC: 0xC0FFEE,
				Marker: i%10 == 9,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		frames = append(frames, frame{seq: seq, raw: raw})
	}
	if spec.Shuffle && len(frames) > 3 {
		frames[1], frames[2] = frames[2], frames[1]
	}
	for _, f := range frames {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(f.raw)))
		buf.Write(lenBuf[:])
		buf.Write(f.raw)
		out.WrittenSeq = append(out.WrittenSeq, f.seq)
	}
	out.Bytes = buf.Bytes()
	return out
}

// buildID3 creates a minimal valid ID3v2.4 tag with an empty body, tagged
// with idx so distinct packets have distinct content hashes.
func buildID3(idx int) []byte {
	body := []byte{'T', 'I', 'T', 2, 0, 0, 0, byte(idx + 1)}
	for len(body) < 16 {
		body = append(body, 0)
	}
	hdr := []byte{'I', 'D', '3', 4, 0, 0}
	hdr = append(hdr, syncSafe(uint32(len(body)))...)
	return append(hdr, body...)
}

func syncSafe(n uint32) []byte {
	return []byte{
		byte(n >> 21),
		byte(n >> 14),
		byte(n >> 7),
		byte(n),
	}
}

// RTPFrameLen decodes the 4-byte frame prefix (test helper).
func RTPFrameLen(buf []byte) uint32 { return binary.BigEndian.Uint32(buf[:4]) }
