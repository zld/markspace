// Package testmedia generates deterministic synthetic MP4/MOV containers and
// RTP packet streams from in-memory specs. No external files or network
// services are used: every byte derives from an integer seed so fixtures and
// their hashes stay reproducible across runs and machines.
package testmedia

import (
	"encoding/binary"
	"io"
)

// BoxWriter accumulates ISO BMFF boxes. Nested boxes are opened with Open and
// closed with Close; payload bytes are appended with Write.
type BoxWriter struct {
	// chunks[depth] holds the pieces of the box currently open at depth.
	chunks [][]byte
	types  [][4]byte
}

// NewBoxWriter returns an empty writer.
func NewBoxWriter() *BoxWriter { return &BoxWriter{} }

// Open begins a child box of the given four-character type.
func (w *BoxWriter) Open(typ string) {
	w.types = append(w.types, fourcc(typ))
	w.chunks = append(w.chunks, nil)
}

// Write appends raw payload bytes to the currently open box.
func (w *BoxWriter) Write(p []byte) {
	if len(w.chunks) == 0 {
		panic("testmedia: Write without Open")
	}
	d := len(w.chunks) - 1
	w.chunks[d] = append(w.chunks[d], p...)
}

// U16/U32/U64 append big-endian integers to the open box.
func (w *BoxWriter) U16(v uint16) { w.Write(u16b(v)) }
func (w *BoxWriter) U32(v uint32) { w.Write(u32b(v)) }
func (w *BoxWriter) U64(v uint64) { w.Write(u64b(v)) }

// Close seals the innermost box and appends it (with header) to its parent,
// or to the root when the last box is closed.
func (w *BoxWriter) Close() {
	n := len(w.chunks)
	if n == 0 {
		panic("testmedia: Close without Open")
	}
	payload := w.chunks[n-1]
	typ := w.types[n-1]
	box := encodeBox(typ, payload)
	w.chunks = w.chunks[:n-1]
	w.types = w.types[:n-1]
	if len(w.chunks) == 0 {
		w.chunks = [][]byte{box}
		return
	}
	d := len(w.chunks) - 1
	w.chunks[d] = append(w.chunks[d], box...)
}

// Bytes returns the serialized top-level boxes.
func (w *BoxWriter) Bytes() []byte {
	if len(w.chunks) != 1 || len(w.types) != 0 {
		panic("testmedia: Bytes called with unclosed boxes")
	}
	return w.chunks[0]
}

// WriteTo writes all top-level boxes. It implements io.WriterTo.
func (w *BoxWriter) WriteTo(out io.Writer) (int64, error) {
	n, err := out.Write(w.Bytes())
	return int64(n), err
}

func encodeBox(typ [4]byte, payload []byte) []byte {
	buf := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(8+len(payload)))
	copy(buf[4:], typ[:])
	return append(buf, payload...)
}

func fourcc(s string) [4]byte {
	if len(s) != 4 {
		panic("testmedia: box type must be 4 chars: " + s)
	}
	return [4]byte{s[0], s[1], s[2], s[3]}
}

func u16b(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64b(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}
