// Package fixture generates the deterministic MP4/RTP sample material used
// by the demo and the tests. Nothing here references external media: the
// payload bytes are derived from a fixed seed so every run produces the
// same bytes and therefore the same fingerprints and archive hashes.
package fixture

import (
	"bytes"
	"encoding/binary"
)

// boxBuilder accumulates one MP4 box. Children are kept in insertion order
// so the emitted byte layout is fully deterministic.
type boxBuilder struct {
	typ      [4]byte
	payload  []byte
	children []*boxBuilder
}

func newBox(typ string) *boxBuilder {
	b := &boxBuilder{}
	copy(b.typ[:], typ)
	return b
}

func (b *boxBuilder) raw(data []byte) *boxBuilder {
	b.payload = append(b.payload, data...)
	return b
}

func (b *boxBuilder) u32(v uint32) *boxBuilder {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return b.raw(buf[:])
}

func (b *boxBuilder) u16(v uint16) *boxBuilder {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], v)
	return b.raw(buf[:])
}

func (b *boxBuilder) child(c *boxBuilder) *boxBuilder {
	b.children = append(b.children, c)
	return b
}

// size returns the total on-wire box size including its 8-byte header.
func (b *boxBuilder) size() uint64 {
	n := uint64(8 + len(b.payload))
	for _, c := range b.children {
		n += c.size()
	}
	return n
}

// emit writes the box and its children into buf, preserving insertion order.
func (b *boxBuilder) emit(buf *bytes.Buffer) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(b.size()))
	copy(hdr[4:8], b.typ[:])
	buf.Write(hdr[:])
	buf.Write(b.payload)
	for _, c := range b.children {
		c.emit(buf)
	}
}

// emitStcoPositions writes the tree like emit but records the absolute
// byte offset of the single stco entry value (box header 8 + full-box 4 +
// entry-count 4) whenever an stco box is encountered. The position math
// mirrors emit exactly, so recorded offsets always land on real bytes.
func (b *boxBuilder) emitStcoPositions(buf *bytes.Buffer, pos *[]uint64) {
	start := uint64(buf.Len())
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(b.size()))
	copy(hdr[4:8], b.typ[:])
	buf.Write(hdr[:])
	buf.Write(b.payload)
	if string(b.typ[:]) == "stco" {
		// header(8) + full-box(4) + entry-count(4); the entry follows.
		*pos = append(*pos, start+8+uint64(len(b.payload))-4)
	}
	for _, c := range b.children {
		c.emitStcoPositions(buf, pos)
	}
}

// fullBoxPrefix is the version+flags header of an ISO full box.
func fullBox(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

// deterministicBytes fills n bytes from a keyed linear sequence. The key
// separates tracks/samples so equal-sized samples still differ on purpose.
func deterministicBytes(key byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(uint32(key)*2654435761>>24) ^ byte(i*7+int(key)*13)
	}
	return out
}
