// Package fixture builds self-contained sample media in memory: a valid
// MP4/fMP4 with multiple timescales and cross-track timed metadata, plus the
// deliberately damaged variants the demo and tests exercise.
package fixture

import (
	"bytes"
	"encoding/binary"
)

// box helpers ------------------------------------------------------------

type wbuf struct{ b bytes.Buffer }

func (w *wbuf) u8(v uint8)                 { w.b.WriteByte(v) }
func (w *wbuf) u16(v uint16)               { var t [2]byte; binary.BigEndian.PutUint16(t[:], v); w.b.Write(t[:]) }
func (w *wbuf) u24(v uint32)               { w.b.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)}) }
func (w *wbuf) u32(v uint32)               { var t [4]byte; binary.BigEndian.PutUint32(t[:], v); w.b.Write(t[:]) }
func (w *wbuf) u64(v uint64)               { var t [8]byte; binary.BigEndian.PutUint64(t[:], v); w.b.Write(t[:]) }
func (w *wbuf) raw(p []byte)               { w.b.Write(p) }
func (w *wbuf) str(s string)               { w.b.WriteString(s) }
func (w *wbuf) zeros(n int)                { w.b.Write(make([]byte, n)) }
func (w *wbuf) full(v uint8, flags uint32) { w.u8(v); w.u24(flags) }
func (w *wbuf) bytes() []byte              { return w.b.Bytes() }

func box(typ string, payload []byte) []byte {
	var b wbuf
	b.u32(uint32(8 + len(payload)))
	b.str(typ)
	b.raw(payload)
	return b.bytes()
}

func container(typ string, children ...[]byte) []byte {
	var b wbuf
	for _, c := range children {
		b.raw(c)
	}
	return box(typ, b.bytes())
}

func fullbox(typ string, version uint8, flags uint32, payload []byte) []byte {
	var b wbuf
	b.full(version, flags)
	b.raw(payload)
	return box(typ, b.bytes())
}
