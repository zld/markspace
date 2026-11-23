// Package fixture generates fully deterministic synthetic inputs: a small
// documented MP4 subset (ftyp/moov/mdat with two video tracks and one audio
// track), damaged variants of it, and RTP packet streams with controlled
// loss, reordering and duplication. No external files are ever read.
package fixture

import (
	"encoding/binary"
	"fmt"
)

// putType writes a four-character box type.
func putType(b []byte, typ string) {
	copy(b, []byte(typ))
}

// boxSize returns the full size of a box with the given payload length.
func boxSize(payload int) int { return 8 + payload }

// writeBox emits a box header (size+type) followed by payload into buf at
// off, and returns the offset just past the box.
func writeBox(buf []byte, off int, typ string, payload []byte) int {
	binary.BigEndian.PutUint32(buf[off:], uint32(8+len(payload)))
	putType(buf[off+4:], typ)
	copy(buf[off+8:], payload)
	return off + 8 + len(payload)
}

// appendBox returns a new byte slice containing buf plus one box.
func appendBox(buf []byte, typ string, payload []byte) []byte {
	out := make([]byte, 0, len(buf)+8+len(payload))
	out = append(out, buf...)
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	putType(hdr[4:], typ)
	out = append(out, hdr...)
	out = append(out, payload...)
	return out
}

// fullBoxPrefix builds version(1)+flags(3) for a FullBox.
func fullBoxPrefix(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

// u32 / u64 helpers for deterministic table construction.
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

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// SamplePayload builds the deterministic media payload of one synthetic
// sample. The layout is:
//
//	[0:4]   magic 'MFSM' (media-forensics synthetic media)
//	[4:8]   track ID (big endian uint32)
//	[8:12]  sample index (big endian uint32)
//	[12:16] content seed (big endian uint32) — drives pixel/PCM values
//	[16:20] payload CRC placeholder, zeroed by the generator
//	[20:]   fill bytes derived from a deterministic xorshift stream
//
// Every payload starts with a 20-byte header so the repair layer can locate
// sample boundaries even when chunk offsets are missing.
func SamplePayload(trackID, sampleIndex, contentSeed int, size int) []byte {
	if size < 20 {
		size = 20
	}
	b := make([]byte, size)
	copy(b[0:4], []byte("MFSM"))
	binary.BigEndian.PutUint32(b[4:], uint32(trackID))
	binary.BigEndian.PutUint32(b[8:], uint32(sampleIndex))
	binary.BigEndian.PutUint32(b[12:], uint32(contentSeed))
	state := uint32(trackID)*2654435761 ^ uint32(sampleIndex)*40503 ^ uint32(contentSeed)*2246822519
	for i := 20; i < size; i++ {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		b[i] = byte(state)
	}
	return b
}

// BoxTypes walks the box structure recursively and returns every box type in
// depth-first encounter order. It is used by tests to assert table presence
// without confusing random payload bytes for box headers.
func BoxTypes(data []byte) ([]string, error) {
	var out []string
	var walk func(from, limit int64, depth int) error
	walk = func(from, limit int64, depth int) error {
		off := from
		for off+8 <= limit {
			size := int64(binary.BigEndian.Uint32(data[off:]))
			if size < 8 || off+size > limit {
				return errBox("box length crosses parent boundary at offset %d", off)
			}
			typ := string(data[off+4 : off+8])
			out = append(out, typ)
			if isContainer[typ] {
				if err := walk(off+8, off+size, depth+1); err != nil {
					return err
				}
			}
			off += size
		}
		return nil
	}
	if err := walk(0, int64(len(data)), 0); err != nil {
		return nil, err
	}
	return out, nil
}

var isContainer = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "dinf": true, "edts": true, "udta": true,
}

type boxError string

func (e boxError) Error() string { return "fixture: " + string(e) }

func errBox(format string, args ...interface{}) error {
	return boxError(fmt.Sprintf(format, args...))
}

// ValidPayload reports whether a sample payload still carries its magic and
// declared track/index header. A zeroed or torn region fails here.
func ValidPayload(b []byte, wantTrack, wantIndex int) bool {
	if len(b) < 16 || string(b[0:4]) != "MFSM" {
		return false
	}
	if binary.BigEndian.Uint32(b[4:]) != uint32(wantTrack) {
		return false
	}
	return binary.BigEndian.Uint32(b[8:]) == uint32(wantIndex)
}
