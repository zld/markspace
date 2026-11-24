package fixture

import "encoding/binary"

func buildMvhd(cfg TrackConfig) []byte {
	d := append(fullBox(0, 0), make([]byte, 96)...)
	putU32(d[4:], cfg.Timescale) // timescale field offset inside mvhd
	putU32(d[8:], 1000)          // duration
	return d
}

func buildTkhd(cfg TrackConfig) []byte {
	d := append(fullBox(0, 7), make([]byte, 84)...)
	putU32(d[4+4+4+4+4:], cfg.TrackID) // creation/modification precede ID
	putU32(d[4+76:], uint32(cfg.Width)<<16)
	putU32(d[4+80:], uint32(cfg.Height)<<16)
	return d
}

func buildMdhd(cfg TrackConfig) []byte {
	d := append(fullBox(0, 0), make([]byte, 20)...)
	putU32(d[4+4+4:], cfg.Timescale) // after creation/modification times
	return d
}

func buildStsd(cfg TrackConfig) []byte {
	entry := make([]byte, 78)
	copy(entry[0:4], "avc1")
	binary.BigEndian.PutUint16(entry[24:26], cfg.Width)
	binary.BigEndian.PutUint16(entry[26:28], cfg.Height)
	d := append(fullBox(0, 0), 0, 0, 0, 1) // entry count
	return append(d, entry...)
}

func buildDref() []byte {
	d := append(fullBox(0, 0), 0, 0, 0, 1) // entry count
	url := Box{Type: "url ", Data: fullBox(0, 1)}
	return append(d, url.Encode()...)
}

func buildMfhd(seq uint32) []byte {
	d := append(fullBox(0, 0), 0, 0, 0, 0)
	putU32(d[4:], seq)
	return d
}

func buildTfhd(trackID uint32, flags uint32) []byte {
	d := append(fullBox(0, flags), 0, 0, 0, 0)
	putU32(d[4:], trackID)
	return d
}

func buildTfdt(base uint64) []byte {
	d := append(fullBox(1, 0), make([]byte, 8)...)
	putU64(d[4:], base)
	return d
}

// buildTrun emits v1 trun with data-offset, duration, size and flags per
// sample (flags 0x000701).
func buildTrun(samples []Sample, withDataOffset bool) []byte {
	flags := uint32(0x000700) // duration|size|flags
	// Bytes following the 4-byte full box: sample count plus optional
	// data offset.
	header := 4
	if withDataOffset {
		flags |= 0x000001
		header += 4
	}
	d := append(fullBox(1, flags), make([]byte, header)...)
	putU32(d[4:], uint32(len(samples)))
	for _, s := range samples {
		entry := make([]byte, 12)
		putU32(entry[0:], s.Duration)
		putU32(entry[4:], s.Size)
		if !s.Keyframe {
			// sample_is_non_sync_sample is bit 16 of the flags word,
			// i.e. the second byte in big-endian order.
			entry[9] = 0x01
		}
		d = append(d, entry...)
	}
	return d
}

// patchTrunDataOffset rewrites the trun data-offset field after layout.
func patchTrunDataOffset(segment []byte, offset int32) {
	pos := findBox(segment, "trun")
	if pos < 0 {
		panic("fixture: trun box not found")
	}
	// trun: 8 header + 4 fullbox + 4 sample_count => data offset at +16.
	binary.BigEndian.PutUint32(segment[pos+16:], uint32(offset))
}

// findBox returns the absolute start offset of a box by recursive scan.
func findBox(data []byte, want string) int {
	return scanBoxes(data, want, 0)
}

func scanBoxes(data []byte, want string, base int) int {
	for i := 0; i+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[i : i+4]))
		typ := string(data[i+4 : i+8])
		if size < 8 || i+size > len(data) {
			return -1
		}
		if typ == want {
			return base + i
		}
		if isContainer(typ) {
			if hit := scanBoxes(data[i+8:i+size], want, base+i+8); hit >= 0 {
				return hit
			}
		}
		i += size
	}
	return -1
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "moof", "traf":
		return true
	}
	return false
}
