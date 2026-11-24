// Package fixture generates the deterministic MP4/MOV and RTP test assets
// used by every module and by the demo command. All bytes are synthesized
// locally: the kernel never depends on external sample files.
package fixture

import "encoding/binary"

// Box wraps a four-char type and payload in an ISOBMFF small-size box
// header. Nested containers are built by passing already-boxed children as
// payload.
func Box(typ string, payload []byte) []byte {
	n := uint32(8 + len(payload))
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], n)
	copy(buf[4:8], []byte(typ))
	copy(buf[8:], payload)
	return buf
}

// FullBox prefixes a payload with version (0) and 24-bit flags (0).
func FullBox(typ string, payload []byte) []byte {
	return Box(typ, append([]byte{0, 0, 0, 0}, payload...))
}

// u32 / u64 / i32 keep box construction terse.
func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func i32(v int32) []byte { return u32(uint32(v)) }

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
