package scan

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"

	"mediainspect/internal/media"
)

// encodePayload serializes frame metadata into an RTP payload.
func encodePayload(s *frameSpec) []byte {
	buf := make([]byte, 0, 64)
	var fixed [25]byte
	binary.BigEndian.PutUint32(fixed[0:4], s.size)
	binary.BigEndian.PutUint32(fixed[4:8], s.declared)
	binary.BigEndian.PutUint32(fixed[8:12], s.checksum)
	binary.BigEndian.PutUint32(fixed[12:16], s.payloadCRC)
	binary.BigEndian.PutUint16(fixed[16:18], 1920)
	binary.BigEndian.PutUint16(fixed[18:20], 1080)
	if s.color.Present {
		fixed[20] = 1
	}
	binary.BigEndian.PutUint32(fixed[21:25], uint32(s.audio.SampleRate))
	buf = append(buf, fixed[:]...)
	buf = append(buf, byte(s.audio.Channels))
	buf = appendStr(buf, s.audio.Layout)
	buf = appendStr(buf, s.color.Primaries)
	buf = appendStr(buf, s.color.Transfer)
	buf = appendStr(buf, s.color.Matrix)
	buf = appendStr(buf, s.color.Range)
	return buf
}

func appendStr(buf []byte, s string) []byte {
	buf = append(buf, byte(len(s)))
	return append(buf, s...)
}

// decodePayload deserializes frame metadata from an RTP payload.
func decodePayload(p []byte, f *media.Frame) error {
	if len(p) < 26 {
		return fmt.Errorf("frame payload too short: %d bytes", len(p))
	}
	f.Size = binary.BigEndian.Uint32(p[0:4])
	f.DeclaredSize = binary.BigEndian.Uint32(p[4:8])
	f.Checksum = binary.BigEndian.Uint32(p[8:12])
	f.PayloadCRC = binary.BigEndian.Uint32(p[12:16])
	f.Width = binary.BigEndian.Uint16(p[16:18])
	f.Height = binary.BigEndian.Uint16(p[18:20])
	f.Color.Present = p[20] == 1
	f.Audio.SampleRate = int(binary.BigEndian.Uint32(p[21:25]))
	f.Audio.Channels = int(p[25])
	rest := p[26:]
	var err error
	if f.Audio.Layout, rest, err = readStr(rest); err != nil {
		return err
	}
	if f.Color.Primaries, rest, err = readStr(rest); err != nil {
		return err
	}
	if f.Color.Transfer, rest, err = readStr(rest); err != nil {
		return err
	}
	if f.Color.Matrix, rest, err = readStr(rest); err != nil {
		return err
	}
	if f.Color.Range, rest, err = readStr(rest); err != nil {
		return err
	}
	return nil
}

func readStr(p []byte) (string, []byte, error) {
	if len(p) < 1 || len(p) < 1+int(p[0]) {
		return "", nil, fmt.Errorf("malformed string field")
	}
	n := int(p[0])
	return string(p[1 : 1+n]), p[1+n:], nil
}

// DigestFrame folds one frame's canonical fields into h.
func DigestFrame(h hash.Hash64, f *media.Frame) {
	var b [40]byte
	binary.BigEndian.PutUint32(b[0:4], f.StreamID)
	binary.BigEndian.PutUint16(b[4:6], f.Seq)
	binary.BigEndian.PutUint32(b[6:10], f.Timestamp)
	binary.BigEndian.PutUint32(b[10:14], f.Size)
	binary.BigEndian.PutUint32(b[14:18], f.DeclaredSize)
	binary.BigEndian.PutUint32(b[18:22], f.Checksum)
	binary.BigEndian.PutUint32(b[22:26], f.PayloadCRC)
	binary.BigEndian.PutUint32(b[26:30], uint32(f.Audio.SampleRate))
	binary.BigEndian.PutUint32(b[30:34], uint32(f.Audio.Channels))
	if f.Keyframe {
		b[34] = 1
	}
	if f.Color.Present {
		b[35] = 1
	}
	h.Write(b[:36])
	h.Write([]byte(f.Audio.Layout))
	h.Write([]byte{0})
	h.Write([]byte(f.Color.Primaries))
	h.Write([]byte{0})
	h.Write([]byte(f.Color.Transfer))
	h.Write([]byte{0})
	h.Write([]byte(f.Color.Matrix))
	h.Write([]byte{0})
	h.Write([]byte(f.Color.Range))
	h.Write([]byte{0})
}

// NewDigester returns the hash used for input digests.
func NewDigester() hash.Hash64 { return fnv.New64a() }

// DigestString formats a digest value.
func DigestString(v uint64) string { return fmt.Sprintf("%016x", v) }
