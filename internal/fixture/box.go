// Package fixture generates self-contained HLS/DASH style MP4 media and
// playlists for tests and the acceptance driver. No external sample files
// are referenced: every byte is synthesized deterministically.
package fixture

import "encoding/binary"

// Box wraps an ISO BMFF four-character code with a payload builder.
type Box struct {
	Type string
	Data []byte
	// Children, when non-empty, replaces Data as the box payload.
	Children []Box
}

// Encode serializes the box with its 8-byte size/type header.
func (b Box) Encode() []byte {
	payload := b.Data
	if len(b.Children) > 0 {
		payload = nil
		for _, c := range b.Children {
			payload = append(payload, c.Encode()...)
		}
	}
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], []byte(boxType(b.Type)))
	copy(out[8:], payload)
	return out
}

func boxType(t string) string {
	if len(t) != 4 {
		panic("fixture: box type must be 4 bytes: " + t)
	}
	return t
}

func fullBox(ver uint8, flags uint32) []byte {
	return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func putU32(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }
func putU64(b []byte, v uint64) { binary.BigEndian.PutUint64(b, v) }
