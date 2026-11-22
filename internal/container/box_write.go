package container

import (
	"bytes"
	"encoding/binary"
)

// Writer builds boxes into an in-memory byte buffer. The fixture generator is
// the only production writer; the archive pipeline itself never rewrites
// customer media, it only emits manifests.
type Writer struct{ buf bytes.Buffer }

// Bytes returns the accumulated bytes.
func (w *Writer) Bytes() []byte { return w.buf.Bytes() }

// Len returns the current byte length.
func (w *Writer) Len() int { return w.buf.Len() }

// WriteBox wraps a payload with an 8-byte header (largesize is chosen
// automatically when the payload exceeds 32-bit range).
func (w *Writer) WriteBox(t FourCC, payload []byte) {
	total := uint64(8 + len(payload))
	if total > 0xFFFFFFFF {
		hdr := make([]byte, 16)
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], t[:])
		binary.BigEndian.PutUint64(hdr[8:16], total+8) // 16-byte header
		w.buf.Write(hdr)
	} else {
		hdr := make([]byte, 8)
		binary.BigEndian.PutUint32(hdr[0:4], uint32(total))
		copy(hdr[4:8], t[:])
		w.buf.Write(hdr)
	}
	w.buf.Write(payload)
}

// WriteContainer writes a parent box whose payload is the serialized
// children produced by build.
func (w *Writer) WriteContainer(t FourCC, build func(c *Writer)) {
	var child Writer
	build(&child)
	w.WriteBox(t, child.Bytes())
}

// FullBox prefixes version/flags to a payload.
func FullBox(version uint8, flags uint32, body []byte) []byte {
	out := make([]byte, 4+len(body))
	out[0] = version
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	copy(out[4:], body)
	return out
}

// PutU16/PutU32/PutU64 append big-endian integers to b and return the new
// slice.
func PutU16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}

func PutU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func PutU64(b []byte, v uint64) []byte {
	for i := 0; i < 8; i++ {
		b = append(b, byte(v>>(56-8*i)))
	}
	return b
}

// U16/U32/U64 read big-endian integers with bounds-checked slices; callers
// must guarantee the slice is long enough (payload lengths are validated at
// the box boundary).
func U16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
func U32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func U64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

// FullBoxView is a parsed version/flags prefix.
type FullBoxView struct {
	Version uint8
	Flags   uint32
	Body    []byte
}

// AsFullBox strips and validates the FullBox prefix of a payload.
func AsFullBox(payload []byte) (FullBoxView, bool) {
	if len(payload) < 4 {
		return FullBoxView{}, false
	}
	return FullBoxView{
		Version: payload[0],
		Flags:   uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3]),
		Body:    payload[4:],
	}, true
}
