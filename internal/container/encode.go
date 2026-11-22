package container

import (
	"bytes"
	"encoding/binary"

	"mediatimeline/internal/clock"
)

// put16/put32/put64 append big-endian integers to a buffer.
func put16(b *bytes.Buffer, v uint16) {
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], v)
	b.Write(t[:])
}
func put32(b *bytes.Buffer, v uint32) {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	b.Write(t[:])
}
func put64(b *bytes.Buffer, v uint64) {
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], v)
	b.Write(t[:])
}

func encodeMdhd(ts uint32, deltas []uint32) []byte {
	var total uint64
	for _, d := range deltas {
		total += uint64(d)
	}
	b := bytes.NewBuffer(nil)
	put32(b, 0) // creation_time
	put32(b, 0) // modification_time
	put32(b, ts)
	put32(b, uint32(total))
	put16(b, 0x55C4) // language 'und'
	put16(b, 0)
	return b.Bytes()
}

func encodeMvhd(ts uint32, deltas []uint32) []byte {
	var total uint64
	for _, d := range deltas {
		total += uint64(d)
	}
	b := bytes.NewBuffer(nil)
	put32(b, 0)
	put32(b, 0)
	put32(b, ts)
	put32(b, uint32(total))
	put32(b, 0x00010000)      // rate 1.0
	put16(b, 0x0100)          // volume
	b.Write(make([]byte, 10)) // reserved
	b.Write(make([]byte, 36)) // identity matrix
	b.Write(make([]byte, 24)) // pre_defined
	put32(b, 2)               // next_track_ID
	return b.Bytes()
}

func encodeTkhd(trackID uint32) []byte {
	b := bytes.NewBuffer(nil)
	put32(b, 0)
	put32(b, 0)
	put32(b, trackID)
	put32(b, 0)               // reserved
	put32(b, 0)               // duration (patched context not needed for parsing tests)
	b.Write(make([]byte, 8))  // reserved
	put16(b, 0)               // layer/alt
	put16(b, 0)               // volume
	b.Write(make([]byte, 2))  // reserved
	b.Write(make([]byte, 36)) // matrix
	put32(b, 0x00010000)
	put32(b, 0)
	put32(b, 0)
	return b.Bytes()
}

func encodeHdlr(handler string) []byte {
	b := bytes.NewBuffer(nil)
	put32(b, 0) // pre_defined
	b.WriteString(handler)
	b.Write(make([]byte, 12)) // reserved
	b.WriteString("synthetic")
	return b.Bytes()
}

func encodeDref() []byte {
	b := bytes.NewBuffer(nil)
	put32(b, 1) // entry_count
	// url entry, self-contained flag
	url := box("url ", fullBox(1, nil))
	b.Write(url)
	return b.Bytes()
}

// encodeStsd emits a minimal uncompressed-audio (raw PCM) sample entry. The
// scanner never decodes this header; it only needs the box structure to be
// valid for go-mp4's stbl traversal.
func encodeStsd(channels uint16, timescale uint32) []byte {
	b := bytes.NewBuffer(nil)
	put32(b, 1) // entry_count
	entry := bytes.NewBuffer(nil)
	entry.Write(make([]byte, 6)) // reserved
	put16(entry, 1)              // data_reference_index
	entry.Write(make([]byte, 8)) // reserved (audio sample entry)
	put16(entry, channels)
	put16(entry, 16) // sample size
	put16(entry, 0)  // predefined
	put16(entry, 0)  // reserved
	put32(entry, timescale<<16)
	put32(entry, 0) // reserved
	b.Write(box("sowt", fullBox(0, entry.Bytes())))
	return b.Bytes()
}

func encodeStts(deltas []uint32) []byte {
	// Collapse identical adjacent deltas into run-length entries.
	type run struct{ count, delta uint32 }
	var runs []run
	for _, d := range deltas {
		if n := len(runs); n > 0 && runs[n-1].delta == d && runs[n-1].count < 0xFFFFFFFF {
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{1, d})
	}
	b := bytes.NewBuffer(nil)
	put32(b, uint32(len(runs)))
	for _, r := range runs {
		put32(b, r.count)
		put32(b, r.delta)
	}
	return box("stts", fullBox(0, b.Bytes()))
}

func encodeStsc() []byte {
	b := bytes.NewBuffer(nil)
	put32(b, 1) // entry_count
	put32(b, 1) // first_chunk
	put32(b, 1) // one sample per chunk
	put32(b, 1) // sample_description_index
	return b.Bytes()
}

func encodeStsz(sizes []uint32) []byte {
	b := bytes.NewBuffer(nil)
	put32(b, 0) // sample_size: variable
	put32(b, uint32(len(sizes)))
	for _, s := range sizes {
		put32(b, s)
	}
	return box("stsz", fullBox(0, b.Bytes()))
}

func encodeStco(offsets []uint64) []byte {
	b := bytes.NewBuffer(nil)
	put32(b, uint32(len(offsets)))
	for _, o := range offsets {
		put32(b, uint32(o))
	}
	return b.Bytes()
}

// TruncateSTBL rewrites the stbl box header to declare a payload shorter than
// its real bytes, so go-mp4's traversal reports a truncated box while the
// underlying data remains readable for localization tests.
func TruncateSTBL(mp4Bytes []byte) ([]byte, error) {
	idx, hdr, err := findBox(mp4Bytes, "stbl")
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), mp4Bytes...)
	// Shrink declared size by half the stbl payload (minimum one child box).
	newSize := hdr/2 + 4
	binary.BigEndian.PutUint32(out[idx:], uint32(newSize))
	return out, nil
}

// CorruptCRC flips one payload byte inside the mdat of sample sampleIndex,
// forcing a CRC mismatch at a known (offset, track, sample) location.
func CorruptCRC(mp4Bytes []byte, sampleIndex int) ([]byte, int64, error) {
	idx, _, err := findBox(mp4Bytes, "mdat")
	if err != nil {
		return nil, 0, err
	}
	payloadStart := int64(idx + 8)
	// Locate the sample via the container's own offset table for precision.
	off, size, err := sampleExtent(mp4Bytes, sampleIndex)
	if err != nil {
		return nil, 0, err
	}
	_ = payloadStart
	out := append([]byte(nil), mp4Bytes...)
	out[off+size/2] ^= 0xFF
	return out, off, nil
}

// sampleExtent resolves a sample's absolute offset and size from stco/stsz
// without a full Scanner round trip (used by the corruption tooling).
func sampleExtent(mp4Bytes []byte, sampleIndex int) (int64, int64, error) {
	stcoIdx, _, err := findBox(mp4Bytes, "stco")
	if err != nil {
		return 0, 0, err
	}
	payload := mp4Bytes[stcoIdx+8+4:] // skip full-box version/flags
	count := int(binary.BigEndian.Uint32(payload))
	if sampleIndex >= count {
		return 0, 0, clock.NewError(clock.ErrIllegalOffset, "sample index beyond stco")
	}
	off := int64(binary.BigEndian.Uint32(payload[4+4*sampleIndex:]))
	stszIdx, _, err := findBox(mp4Bytes, "stsz")
	if err != nil {
		return 0, 0, err
	}
	sp := mp4Bytes[stszIdx+8+4:]
	sampleCount := int(binary.BigEndian.Uint32(sp[4:]))
	_ = sampleCount
	size := int64(binary.BigEndian.Uint32(sp[8+4*sampleIndex:]))
	return off, size, nil
}

// FindBoxForTest exposes the recursive box locator for external tooling such
// as the corruption demonstration. It returns the box header offset and size.
func FindBoxForTest(data []byte, typ string) (int, int, error) {
	return findBox(data, typ)
}

// findBox performs a recursive pre-order search for a box type, returning the
// absolute offset of its header and its declared total size.
func findBox(data []byte, typ string) (int, int, error) {
	var walk func(region []byte, base int) (int, int, bool)
	walk = func(region []byte, base int) (int, int, bool) {
		for off := 0; off+8 <= len(region); {
			size := int(binary.BigEndian.Uint32(region[off:]))
			if size < 8 || off+size > len(region) {
				return 0, 0, false
			}
			if string(region[off+4:off+8]) == typ {
				return base + off, size, true
			}
			if hit, sz, ok := walk(region[off+8:off+size], base+off+8); ok {
				return hit, sz, true
			}
			off += size
		}
		return 0, 0, false
	}
	if off, size, ok := walk(data, 0); ok {
		return off, size, nil
	}
	return 0, 0, clock.NewError(clock.ErrTruncatedBox, "box "+typ+" not found")
}
