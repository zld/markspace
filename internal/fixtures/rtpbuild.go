package fixtures

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"

	"mediasync/internal/stream"
)

// RTPLayout records the byte positions a test can corrupt.
type RTPLayout struct {
	Path      string
	Size      int64
	PacketAt  []int64 // offset of each 4-byte length prefix
	PayloadAt []int64 // offset of each RTP header byte
	HeaderAt  []int64 // alias for PayloadAt
}

// WriteRTP writes a synthetic RTP recording with a deterministic timestamp
// walk. startTS may be near the wrap boundary.
func WriteRTP(path string, clockRate uint32, packets int, startTS uint32, step uint32, seed int64, wrap bool) (RTPLayout, error) {
	rng := rand.New(rand.NewSource(seed))
	ssrc := uint32(0xA1B2C3D0) ^ uint32(rng.Intn(255))
	var recs [][]byte
	ts := startTS
	for i := 0; i < packets; i++ {
		payload := make([]byte, 8+rng.Intn(16))
		for k := range payload {
			payload[k] = byte(seed + int64(i*13+k))
		}
		if wrap && i == packets/2 {
			// Force a wrap mid-stream from near the top of the range.
			ts = uint32(0xFFFFFFFF - step/2)
		}
		pkt, err := stream.MarshalPacket(uint16(i+1), ts, i == packets-1, 96, ssrc, payload)
		if err != nil {
			return RTPLayout{}, err
		}
		recs = append(recs, pkt)
		ts += step
	}
	f, err := os.Create(path)
	if err != nil {
		return RTPLayout{}, err
	}
	defer f.Close()
	layout := RTPLayout{Path: path}
	if _, err := f.WriteString("MSRTP1\n"); err != nil {
		return RTPLayout{}, err
	}
	var rateBuf [4]byte
	binary.BigEndian.PutUint32(rateBuf[:], clockRate)
	if _, err := f.Write(rateBuf[:]); err != nil {
		return RTPLayout{}, err
	}
	off := int64(len("MSRTP1\n") + 4)
	var lenBuf [4]byte
	for _, p := range recs {
		layout.PacketAt = append(layout.PacketAt, off)
		layout.PayloadAt = append(layout.PayloadAt, off+4)
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(p)))
		if _, err := f.Write(lenBuf[:]); err != nil {
			return RTPLayout{}, err
		}
		if _, err := f.Write(p); err != nil {
			return RTPLayout{}, err
		}
		off += 4 + int64(len(p))
	}
	layout.HeaderAt = layout.PayloadAt
	st, err := f.Stat()
	if err != nil {
		return RTPLayout{}, err
	}
	layout.Size = st.Size()
	return layout, nil
}

// CorruptByte flips one byte in place and returns the offset.
func CorruptByte(path string, off int64, xor byte) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if off < 0 || off >= int64(len(b)) {
		return 0, fmt.Errorf("fixtures: offset %d outside %s", off, path)
	}
	if b[off] == xor {
		b[off] ^= 0xFF
	} else {
		b[off] ^= xor
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return 0, err
	}
	return off, nil
}

// TruncateAt cuts a file at a byte offset and returns the new size.
func TruncateAt(path string, off int64) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if off < 0 || off > int64(len(b)) {
		return 0, fmt.Errorf("fixtures: truncate %d outside %s", off, path)
	}
	if err := os.WriteFile(path, b[:off], 0o644); err != nil {
		return 0, err
	}
	return off, nil
}
