package fixture

import "encoding/binary"

// CorruptMP4 returns a deliberately damaged copy of a generated MP4. The
// analyzer is expected to locate every injected defect with an offset and
// a master-clock time window rather than merely failing to parse.
type Corruption int

const (
	// CorruptTruncation cuts the mdat payload short, leaving the last
	// declared sample incomplete.
	CorruptTruncation Corruption = iota
	// CorruptInvalidTimestamp rewrites one stts delta to zero, producing
	// a non-positive frame duration.
	CorruptInvalidTimestamp
	// CorruptMissingIndex removes the video stss box so keyframe indices
	// are unavailable.
	CorruptMissingIndex
)

// CorruptMP4 applies one corruption kind. The returned bytes are a fresh
// allocation; the input slice is never modified.
func CorruptMP4(data []byte, kind Corruption) []byte {
	out := append([]byte(nil), data...)
	switch kind {
	case CorruptTruncation:
		// Drop the final 40 bytes, cutting across the last samples while
		// leaving every moov table intact.
		return out[:len(out)-40]
	case CorruptInvalidTimestamp:
		if off := findBoxPayload(out, "stts"); off >= 0 {
			// payload layout: ver/flags(4) entry_count(4) sample_count(4)
			// sample_delta(4) — zero the delta.
			binary.BigEndian.PutUint32(out[off+12:off+16], 0)
		}
		return out
	case CorruptMissingIndex:
		return removeBox(out, "stss")
	}
	return out
}

// findBoxPayload returns the absolute offset of the first box payload with
// the given 4-char type, or -1. It performs a shallow recursive scan over
// size-prefixed boxes without interpreting payloads.
func findBoxPayload(data []byte, typ string) int {
	off := findBox(data, typ, 0)
	if off < 0 {
		return -1
	}
	return off + 8
}

func findBox(data []byte, typ string, start int) int {
	for pos := start; pos+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		boxType := string(data[pos+4 : pos+8])
		if size < 8 || pos+size > len(data) {
			return -1
		}
		if boxType == typ {
			return pos
		}
		// Known container boxes are descended into.
		if isContainer(boxType) {
			if child := findBox(data, typ, pos+8); child >= 0 && child < pos+size {
				return child
			}
		}
		pos += size
	}
	return -1
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "edts", "udta":
		return true
	}
	return false
}

// removeBox splices the first box of the given type out of data, shrinks
// every enclosing size header, and rewrites stco chunk offsets so the
// remaining sample tables keep pointing at the same mdat bytes.
func removeBox(data []byte, typ string) []byte {
	pos := findBox(data, typ, 0)
	if pos < 0 {
		return data
	}
	size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	out := make([]byte, 0, len(data)-size)
	out = append(out, data[:pos]...)
	out = append(out, data[pos+size:]...)
	repairContainerSizes(out)
	shiftChunkOffsets(out, uint32(size))
	return out
}

// shiftChunkOffsets subtracts delta from every stco chunk offset entry.
func shiftChunkOffsets(data []byte, delta uint32) {
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if size < 8 || pos+size > len(data) {
			return
		}
		typ := string(data[pos+4 : pos+8])
		if typ == "stco" && pos+16 <= len(data) {
			count := int(binary.BigEndian.Uint32(data[pos+12 : pos+16]))
			for i := 0; i < count; i++ {
				entry := pos + 16 + i*4
				if entry+4 <= len(data) {
					binary.BigEndian.PutUint32(data[entry:entry+4],
						binary.BigEndian.Uint32(data[entry:entry+4])-delta)
				}
			}
		}
		if isContainer(typ) {
			shiftChunkOffsets(data[pos+8:pos+size], delta)
		}
		pos += size
	}
}

// repairContainerSizes recomputes every container box header from the sum
// of its children after a splice.
func repairContainerSizes(data []byte) {
	recomputeSizes(data, 0, len(data))
}

// recomputeSizes rewrites container box sizes from their children and
// returns the total consumed length within [start,end).
func recomputeSizes(data []byte, start, end int) int {
	pos := start
	for pos+8 <= end {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if size < 8 || pos+size > end {
			return pos - start
		}
		typ := string(data[pos+4 : pos+8])
		if isContainer(typ) {
			inner := recomputeSizes(data, pos+8, pos+size)
			newSize := 8 + inner
			binary.BigEndian.PutUint32(data[pos:pos+4], uint32(newSize))
			size = newSize
		}
		pos += size
	}
	return pos - start
}
