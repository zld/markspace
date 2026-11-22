package fixtures

import (
	"encoding/binary"
	"fmt"
)

// Five fault classes are exercised end to end. Each helper mutates a valid
// generated file in place, so the damage is real bytes on disk rather than a
// simulated flag, and the pipeline must locate it from the container alone.

// DamageKind names a fault class.
type DamageKind string

const (
	DamageTruncatedBox   DamageKind = "truncated_box"
	DamageSampleSize     DamageKind = "bad_sample_size"
	DamageClockWrap      DamageKind = "timestamp_wrap"
	DamageMissingSync    DamageKind = "missing_sync_frame"
	DamageTornCheckpoint DamageKind = "torn_checkpoint"
)

// InjectDamage rewrites a valid file for the requested fault class.
func InjectDamage(raw []byte, kind DamageKind) ([]byte, error) {
	switch kind {
	case DamageTruncatedBox:
		off, size, ok := findBox(raw, "mdat")
		if !ok || size < 64 {
			return nil, fmt.Errorf("mdat not found")
		}
		// Cut inside the payload so the index stays intact and only the last
		// samples become unreadable.
		cut := off + size - 333
		if cut <= off+8 {
			cut = off + size/2
		}
		return raw[:cut], nil
	case DamageSampleSize:
		return corruptStszEntry(raw, 7)
	case DamageClockWrap:
		return patchSttsWrap(raw, 6)
	case DamageMissingSync:
		return dropSyncEntries(raw, 6)
	case DamageTornCheckpoint:
		// Not a container fault; callers append a torn journal record.
		return raw, nil
	default:
		return nil, fmt.Errorf("unknown damage kind %q", kind)
	}
}

// findBox returns the offset and declared size of the first box of a type,
// searching nested containers.
func findBox(raw []byte, typ string) (int64, int64, bool) {
	return findIn(raw, typ, 0, int64(len(raw)))
}

func findIn(raw []byte, typ string, start, end int64) (int64, int64, bool) {
	for off := start; off+8 <= end; {
		size := int64(binary.BigEndian.Uint32(raw[off:]))
		header := int64(8)
		if size == 1 {
			if off+16 > end {
				return 0, 0, false
			}
			size = int64(binary.BigEndian.Uint64(raw[off+8:]))
			header = 16
		}
		if size < header || off+size > end {
			return 0, 0, false
		}
		t := string(raw[off+4 : off+8])
		if t == typ {
			return off, size, true
		}
		if isContainerType(t) {
			if o, sz, ok := findIn(raw, typ, off+header, off+size); ok {
				return o, sz, ok
			}
		}
		off += size
	}
	return 0, 0, false
}

// corruptStszEntry makes one sample claim a size the file cannot hold.
func corruptStszEntry(raw []byte, index int) ([]byte, error) {
	out := append([]byte(nil), raw...)
	off, size, ok := findBox(out, "stsz")
	if !ok || size < 20 {
		return nil, fmt.Errorf("stsz not found")
	}
	// fullbox(4) + sample_size(4) + count(4) then entries.
	count := binary.BigEndian.Uint32(out[off+16 : off+20])
	if uint32(index) >= count {
		index = 0
	}
	entry := off + 20 + int64(index*4)
	if entry+4 > off+size || entry+4 > int64(len(out)) {
		return nil, fmt.Errorf("stsz entry out of range")
	}
	binary.BigEndian.PutUint32(out[entry:], 0x00FFFFFF)
	return out, nil
}

// patchSttsWrap rebuilds stts with a 2^31 jump so the 32-bit decode clock
// crosses 2^32 and wraps back to a small value. Grow-ing a moov-first stts
// moves mdat, so chunk offsets are rebased by the same delta.
func patchSttsWrap(raw []byte, after int) ([]byte, error) {
	out := append([]byte(nil), raw...)
	off, size, ok := findBox(out, "stts")
	if !ok {
		return nil, fmt.Errorf("stts not found")
	}
	mdatOff, _, _ := findBox(out, "mdat")
	// Sample 0's DTS is 0, so start the prefix run at the second sample and
	// let the jump land on sample 1..2.
	after = 1
	firstDelta := uint32(3000)
	var total int
	count := int(binary.BigEndian.Uint32(out[off+12 : off+16]))
	for i := 0; i < count; i++ {
		base := off + 16 + int64(i*8)
		n := int(binary.BigEndian.Uint32(out[base : base+4]))
		total += n
		if i == 0 {
			firstDelta = binary.BigEndian.Uint32(out[base+4 : base+8])
		}
	}
	if after < 1 || after >= total-1 {
		return nil, fmt.Errorf("wrap point %d outside %d samples", after, total)
	}
	var entries []byte
	entries = append(entries, u32(uint32(after))...)
	entries = append(entries, u32(firstDelta)...)
	entries = append(entries, u32(1)...)
	entries = append(entries, u32(0x80000003)...)
	if rest := total - after - 1; rest > 0 {
		entries = append(entries, u32(uint32(rest))...)
		entries = append(entries, u32(firstDelta)...)
	}
	entryCount := uint32(3)
	if total-after-1 <= 0 {
		entryCount = 2
	}
	nb := fullBox("stts", 0, 0, u32(entryCount), entries)
	delta := int64(len(nb)) - size
	out, err := replaceBox(out, off, size, nb)
	if err != nil {
		return nil, err
	}
	if delta != 0 && mdatOff > 0 && off < mdatOff {
		if err := rebaseChunkOffsets(out, delta); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rebaseChunkOffsets shifts every stco/co64 entry by delta.
func rebaseChunkOffsets(raw []byte, delta int64) error {
	for _, typ := range []string{"stco", "co64"} {
		off, size, ok := findBox(raw, typ)
		if !ok {
			continue
		}
		count := int(binary.BigEndian.Uint32(raw[off+12 : off+16]))
		width := int64(4)
		if typ == "co64" {
			width = 8
		}
		if off+16+int64(count)*width > off+size {
			return fmt.Errorf("%s entry table overruns box", typ)
		}
		for i := 0; i < count; i++ {
			base := off + 16 + int64(i)*width
			if width == 4 {
				v := binary.BigEndian.Uint32(raw[base:])
				binary.BigEndian.PutUint32(raw[base:], uint32(int64(v)+delta))
			} else {
				v := binary.BigEndian.Uint64(raw[base:])
				binary.BigEndian.PutUint64(raw[base:], uint64(int64(v)+delta))
			}
		}
		return nil
	}
	return nil
}

// replaceBox splices nb over the box at off and repairs enclosing sizes.
func replaceBox(raw []byte, off, size int64, nb []byte) ([]byte, error) {
	path := pathTo(raw, off)
	out := append(append([]byte(nil), raw[:off]...), nb...)
	out = append(out, raw[off+size:]...)
	delta := int64(len(nb)) - size
	if delta != 0 {
		for _, a := range path {
			sz := binary.BigEndian.Uint32(out[a:])
			binary.BigEndian.PutUint32(out[a:], uint32(int64(sz)+delta))
		}
	}
	return out, nil
}

// dropSyncEntries shrinks stss so later sync samples are undeclared.
func dropSyncEntries(raw []byte, keep int) ([]byte, error) {
	out := append([]byte(nil), raw...)
	off, size, ok := findBox(out, "stss")
	if !ok || size < 16 {
		return nil, fmt.Errorf("stss not found")
	}
	count := int(binary.BigEndian.Uint32(out[off+12 : off+16]))
	if keep > count {
		keep = count
	}
	// stss = header(8) + version/flags(4) + entry_count(4) + entries.
	nb := make([]byte, 16+keep*4)
	copy(nb[:16], out[off:off+16])
	binary.BigEndian.PutUint32(nb[0:4], uint32(len(nb)))
	binary.BigEndian.PutUint32(nb[12:16], uint32(keep))
	copy(nb[16:], out[off+16:off+16+int64(keep*4)])
	return replaceBox(out, off, size, nb)
}

// pathTo returns the offsets of the container boxes enclosing target,
// outermost first, by descending each container's child range. Offsets must be
// interpreted against raw before any splice.
func pathTo(raw []byte, target int64) []int64 {
	var path []int64
	var walk func(start, end int64) bool
	walk = func(start, end int64) bool {
		for off := start; off+8 <= end; {
			size := int64(binary.BigEndian.Uint32(raw[off:]))
			header := int64(8)
			if size == 1 {
				if off+16 > end {
					return false
				}
				size = int64(binary.BigEndian.Uint64(raw[off+8:]))
				header = 16
			}
			if size < header || off+size > end {
				return false
			}
			if target < off+size {
				typ := string(raw[off+4 : off+8])
				if !isContainerType(typ) {
					return false
				}
				path = append(path, off)
				childStart := off + header
				if typ == "stsd" {
					childStart += 8
				}
				return walk(childStart, off+size)
			}
			off += size
		}
		return false
	}
	walk(0, int64(len(raw)))
	return path
}

func isContainerType(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl":
		return true
	}
	return false
}

// TornJournalSuffix returns bytes that look like a half-written framed record.
func TornJournalSuffix() []byte {
	return []byte{0, 0, 0, 120, 0xde, 0xad, 0xbe, 0xef, '{', '"', 'b', 'l'}
}
