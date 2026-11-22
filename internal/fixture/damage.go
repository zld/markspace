package fixture

import (
	"encoding/binary"
	"fmt"
)

// Damage helpers mutate an intact file by semantic role. Each one is small and
// asserts the bytes it touches, so a builder change fails loudly instead of
// silently producing a fixture that no longer tests what it claims.

func applyDamage(b []byte, m *Media, lay *layout) ([]byte, error) {
	switch m.Plan.Kind {
	case KindClean, KindWrap:
		return b, nil
	case KindNoMoov:
		return b, nil
	case KindNoIndex:
		return damageNoIndex(b, m)
	case KindTruncated:
		return damageTruncated(b, m, lay)
	case KindBadRange:
		return damageBadRange(b, lay)
	case KindDepCycle:
		return b, nil
	case KindGarbled:
		return damageGarbled(b)
	default:
		return nil, fmt.Errorf("fixture: unknown kind %q", m.Plan.Kind)
	}
}

// damageNoIndex removes every sample-table entry but keeps the track, so a
// scanner sees a valid moov with a missing index rather than a missing track.
func damageNoIndex(b []byte, m *Media) ([]byte, error) {
	// Strip stsz, stsc, stco and stts from stbl by replacing each box with a
	// zero-entry version of itself.
	for _, typ := range []string{"stsz", "stsc", "stco", "stts"} {
		pos := findBox(b, typ, 0)
		if pos < 0 {
			return nil, fmt.Errorf("fixture: no-index: %s not found", typ)
		}
		size := int(binary.BigEndian.Uint32(b[pos:]))
		zero := zeroEntryBox(typ)
		if len(zero) > size {
			return nil, fmt.Errorf("fixture: no-index: %s too small to replace", typ)
		}
		copy(b[pos:], zero)
		// Reclaim the leftover bytes as a free box so sizes stay consistent.
		rest := size - len(zero)
		binary.BigEndian.PutUint32(b[pos:], uint32(len(zero)))
		if rest >= 8 {
			off := pos + len(zero)
			binary.BigEndian.PutUint32(b[off:], uint32(rest))
			copy(b[off+4:], "free")
		}
	}
	return b, nil
}

func zeroEntryBox(typ string) []byte {
	w := newWriter()
	w.full(typ, 0, 0)
	if typ == "stsz" {
		w.u32(0)
		w.u32(0)
	} else {
		w.u32(0)
	}
	w.close()
	return w.buf.Bytes()
}

// damageTruncated cuts the file a little past the first chunk so the moov is
// intact but stco points into bytes that no longer exist.
func damageTruncated(b []byte, m *Media, lay *layout) ([]byte, error) {
	if len(lay.stcoOffsets) < 2 {
		return nil, fmt.Errorf("fixture: truncated: need multiple chunks")
	}
	first := int(binary.BigEndian.Uint32(b[lay.stcoOffsets[0]:]))
	end := first + 4096
	if end >= len(b) {
		end = len(b) - 1024
	}
	if end <= first {
		return nil, fmt.Errorf("fixture: truncated: mdat too small")
	}
	return b[:end], nil
}

// damageBadRange rewrites one chunk offset to point past EOF and makes one
// sample size absurdly large, exercising range validation rather than
// structural truncation.
func damageBadRange(b []byte, lay *layout) ([]byte, error) {
	if len(lay.stcoOffsets) < 3 {
		return nil, fmt.Errorf("fixture: bad-range: need three chunks")
	}
	// Leave the first chunk valid so a provable prefix survives.
	bad := lay.stcoOffsets[2]
	binary.BigEndian.PutUint32(b[bad:], uint32(len(b)+1<<20))

	stsz := findBox(b, "stsz", 0)
	if stsz < 0 {
		return nil, fmt.Errorf("fixture: bad-range: no stsz")
	}
	// stsz: size(4) type(4) version+flags(4) sample_size(4) count(4) entries
	count := int(binary.BigEndian.Uint32(b[stsz+16:]))
	if count < 1 {
		return nil, fmt.Errorf("fixture: bad-range: stsz empty")
	}
	entry := stsz + 20
	binary.BigEndian.PutUint32(b[entry:], 0x7ffffff0)
	return b, nil
}

// damageGarbled destroys every sample record and the index, leaving a file
// with no verifiable audio. Recovery must refuse it rather than invent sound.
func damageGarbled(b []byte) ([]byte, error) {
	// Remove the moov by turning its header into free space of the same size.
	pos := 0
	for i := 0; i+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[i:]))
		typ := string(b[i+4 : i+8])
		if typ == "moov" {
			copy(b[i+4:], "free")
			pos = i
			break
		}
		if size < 8 {
			return nil, fmt.Errorf("fixture: garbled: malformed box")
		}
		i += size
	}
	if pos == 0 {
		return nil, fmt.Errorf("fixture: garbled: no moov")
	}
	// Zero the mdat payload so no record magic or CRC survives.
	mdat := findBox(b, "mdat", 0)
	if mdat < 0 {
		return nil, fmt.Errorf("fixture: garbled: no mdat")
	}
	size := int(binary.BigEndian.Uint32(b[mdat:]))
	for i := mdat + 8; i < mdat+size && i < len(b); i++ {
		b[i] = 0
	}
	return b, nil
}

// findBox returns the offset of the first box of typ, descending into
// containers. Extra prefix bytes for stsd and meta are accounted for.
func findBox(b []byte, typ string, from int) int {
	return findBoxIn(b, typ, from, len(b), 0)
}

var fixtureContainers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "udta": true,
}

func findBoxIn(b []byte, typ string, from, end, depth int) int {
	if depth > 16 {
		return -1
	}
	for i := from; i+8 <= end; {
		size := int(binary.BigEndian.Uint32(b[i:]))
		t := string(b[i+4 : i+8])
		if t == typ {
			return i
		}
		if size < 8 || i+size > end {
			return -1
		}
		if fixtureContainers[t] {
			extra := 0
			if t == "stsd" {
				extra = 8
			}
			if got := findBoxIn(b, typ, i+8+extra, i+size, depth+1); got >= 0 {
				return got
			}
		}
		i += size
	}
	return -1
}

// buildMoovCycle emits a moov with two audio tracks whose cross-references
// form a dependency cycle the graph builder must reject before processing.
func buildMoovCycle(m *Media, lay *layout, sizes []uint32, offsets []int) []byte {
	w := newWriter()
	w.box("moov", func() {
		writeMvhd(w, m)
		writeCycleTrak(w, m, lay, 1, 2, sizes, offsets)
		writeCycleTrak(w, m, lay, 2, 1, sizes, offsets)
	})
	return w.buf.Bytes()
}

// writeCycleTrak writes a full audio track whose edit list has media_time
// -1 and whose udta "dep " child names the track it needs. Readers treat that
// as an explicit dependency, which produces a cycle for dep-cycle fixtures.
func writeCycleTrak(w *writer, m *Media, lay *layout, id, dep uint32, sizes []uint32, offsets []int) {
	w.box("trak", func() {
		w.fullBox("tkhd", 0, 0x3, func() {
			w.u32(0)
			w.u32(0)
			w.u32(id)
			w.u32(0)
			w.u32(uint32(m.Frames))
			w.zeros(8)
			w.u16(0)
			w.u16(0)
			w.u16(0x0100)
			w.u16(0)
			writeMatrix(w)
			w.u32(0)
			w.u32(0)
		})
		w.box("edts", func() {
			w.fullBox("elst", 0, 0, func() {
				w.u32(1)
				w.u32(uint32(m.Frames))
				w.u32(0xffffffff) // media_time = -1
				w.u16(1)
				w.u16(0)
			})
		})
		w.box("mdia", func() {
			w.fullBox("mdhd", 0, 0, func() {
				w.u32(0)
				w.u32(0)
				w.u32(m.Timescale)
				w.u32(uint32(m.Frames))
				w.u16(0x55c4)
				w.u16(0)
			})
			writeHdlr(w)
			w.box("minf", func() {
				writeSmhd(w)
				writeDinf(w)
				w.box("stbl", func() {
					writeStsd(w, m)
					writeStts(w, len(sizes))
					writeStsc(w, len(sizes), m.Plan.SamplesPerChunk)
					writeStsz(w, sizes)
					writeStco(w, offsets, lay)
				})
			})
		})
		w.box("udta", func() {
			w.box("dep ", func() {
				w.u32(dep)
			})
		})
	})
}
