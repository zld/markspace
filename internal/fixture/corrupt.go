package fixture

import "encoding/binary"

// Truncate cuts the file so the final box header announces bytes that no
// longer exist. The returned offsets describe where parsing must notice.
func (f *BuiltFile) Truncate(cutAt int) []byte {
	if cutAt > len(f.Data) {
		cutAt = len(f.Data)
	}
	return append([]byte(nil), f.Data[:cutAt]...)
}

// TruncateMdat removes trailing mdat sample bytes while keeping the mdat
// header's declared size: a truncated-box condition at the original offset.
func (f *BuiltFile) TruncateMdat(dropBytes int) []byte {
	cut := len(f.Data) - dropBytes
	if cut < f.MdatData {
		cut = f.MdatData
	}
	return f.Truncate(cut)
}

// CorruptBoxSize rewrites a box's declared size so it extends past EOF.
// boxHeaderOffset is the absolute position of the target box header.
func (f *BuiltFile) CorruptBoxSize(boxHeaderOffset int) []byte {
	out := append([]byte(nil), f.Data...)
	binary.BigEndian.PutUint32(out[boxHeaderOffset:boxHeaderOffset+4], uint32(len(out)+4096))
	return out
}

// CorruptSample flips bytes inside one sample's payload so its CRC fails. The
// sample header and length stay intact, so the next sample remains reachable.
func (f *BuiltFile) CorruptSample(trackID uint32, sampleIndex int) []byte {
	out := append([]byte(nil), f.Data...)
	loc := f.TrackMdat[trackID][sampleIndex]
	// Flip the first payload byte (past the 24-byte sample header).
	out[loc.Offset+SampleHeaderSize] ^= 0xFF
	return out
}

// CorruptSampleMagic destroys the sync marker of one sample while leaving all
// surrounding bytes and the container index untouched.
func (f *BuiltFile) CorruptSampleMagic(trackID uint32, sampleIndex int) []byte {
	out := append([]byte(nil), f.Data...)
	loc := f.TrackMdat[trackID][sampleIndex]
	for i := 0; i < 8; i++ {
		out[loc.Offset+i] = 0x00
	}
	return out
}

// RemoveBox overwrites a box's type with 'free' and leaves its payload in
// place. The scanner then sees a free box where an index box used to be, which
// is how missing-index conditions are exercised without shifting offsets.
func (f *BuiltFile) RemoveBox(boxHeaderOffset int) []byte {
	out := append([]byte(nil), f.Data...)
	copy(out[boxHeaderOffset+4:boxHeaderOffset+8], "free")
	return out
}

// FindBoxHeader returns the absolute header offset of the first box with the
// given type, or -1. It is a linear type-code scan over the generated layout.
func (f *BuiltFile) FindBoxHeader(code string) int {
	want := BoxType(code)
	for i := 0; i+8 <= len(f.Data); i++ {
		if binary.BigEndian.Uint32(f.Data[i+4:i+8]) == want {
			return i
		}
	}
	return -1
}

// FindBoxHeaderForTrack finds the nth occurrence of a box type (n is 0-based),
// used to target a specific track's stco/stss when several tracks exist.
func (f *BuiltFile) FindBoxHeaderForTrack(code string, occurrence int) int {
	want := BoxType(code)
	seen := 0
	for i := 0; i+8 <= len(f.Data); i++ {
		if binary.BigEndian.Uint32(f.Data[i+4:i+8]) == want {
			if seen == occurrence {
				return i
			}
			seen++
		}
	}
	return -1
}
