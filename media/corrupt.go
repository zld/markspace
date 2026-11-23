package media

import (
	"encoding/binary"
	"fmt"
	"io"
)

// FaultKind names one of the generated corruption scenarios.
type FaultKind string

const (
	FaultTruncatedBox  FaultKind = "truncated_box"
	FaultInvalidSample FaultKind = "invalid_sample_offset"
	FaultUnknownBox    FaultKind = "unknown_required_box"
	FaultZeroTimescale FaultKind = "zero_timescale"
	FaultDuplicateStss FaultKind = "duplicate_sync_sample"
	FaultBadChunkCount FaultKind = "chunk_sample_mismatch"
)

// Corrupt applies a fault to a valid fixture and returns the damaged bytes and
// the byte offset that was changed. Every fault targets a real structure, so
// the error class and offset reported by the parser are predictable.
func Corrupt(raw []byte, kind FaultKind) ([]byte, int64, error) {
	out := append([]byte(nil), raw...)
	switch kind {
	case FaultTruncatedBox:
		if len(out) < 24 {
			return nil, 0, fmt.Errorf("fixture too small to truncate")
		}
		// Cut the file inside the moov payload; the declared size then exceeds
		// the bytes actually present.
		top, err := scanBoxList(&byteSeeker{b: out}, 0, int64(len(out)), 0)
		if err != nil {
			return nil, 0, err
		}
		for _, b := range top {
			if b.Type == "moov" {
				cut := b.Offset + b.Size/2
				return out[:cut], cut, nil
			}
		}
		return nil, 0, fmt.Errorf("no moov box")
	case FaultInvalidSample:
		// Point the first stco entry into the middle of the file header so the
		// sample lies outside mdat.
		off, err := findBoxOffset(out, "stco")
		if err != nil {
			return nil, 0, err
		}
		// stco: size(4) type(4) version/flags(4) count(4) entry(4)
		entryAt := off + 16
		if entryAt+4 > int64(len(out)) {
			return nil, 0, fmt.Errorf("stco entry offset out of range")
		}
		binary.BigEndian.PutUint32(out[entryAt:], 12)
		return out, entryAt, nil
	case FaultUnknownBox:
		// Replace the ftyp major brand with an unsupported top-level box name
		// appended at the end so the scanner must reject it explicitly.
		unknown := box("zzzz", nil)
		pos := int64(len(out))
		out = append(out, unknown...)
		return out, pos, nil
	case FaultZeroTimescale:
		off, err := findBoxOffset(out, "mdhd")
		if err != nil {
			return nil, 0, err
		}
		// mdhd v0: size(8) ver/flags(4) ctime(4) mtime(4) timescale(4)
		tsAt := off + 20
		if tsAt+4 > int64(len(out)) {
			return nil, 0, fmt.Errorf("mdhd offset out of range")
		}
		binary.BigEndian.PutUint32(out[tsAt:], 0)
		return out, tsAt, nil
	case FaultDuplicateStss:
		off, err := findBoxOffset(out, "stss")
		if err != nil {
			return nil, 0, err
		}
		countAt := off + 12
		firstAt := off + 16
		if firstAt+8 > int64(len(out)) {
			return nil, 0, fmt.Errorf("stss too small")
		}
		first := binary.BigEndian.Uint32(out[firstAt:])
		binary.BigEndian.PutUint32(out[firstAt+4:], first)
		_ = countAt
		return out, firstAt + 4, nil
	case FaultBadChunkCount:
		// Inflate the stsc samples-per-chunk so chunks describe too many samples.
		off, err := findBoxOffset(out, "stsc")
		if err != nil {
			return nil, 0, err
		}
		// stsc: size(8) ver/flags(4) count(4) first_chunk(4) spc(4) desc(4)
		spcAt := off + 8 + 4 + 4 + 4
		if spcAt+4 > int64(len(out)) {
			return nil, 0, fmt.Errorf("stsc entry out of range")
		}
		binary.BigEndian.PutUint32(out[spcAt:], 99)
		return out, spcAt, nil
	default:
		return nil, 0, fmt.Errorf("unknown fault kind %q", kind)
	}
}

// byteSeeker adapts a byte slice to io.ReadSeeker for internal scans.
type byteSeeker struct {
	b   []byte
	pos int64
}

func (b *byteSeeker) Read(p []byte) (int, error) {
	if b.pos >= int64(len(b.b)) {
		return 0, io.EOF
	}
	n := copy(p, b.b[b.pos:])
	b.pos += int64(n)
	return n, nil
}

func (b *byteSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = offset
	case io.SeekCurrent:
		b.pos += offset
	case io.SeekEnd:
		b.pos = int64(len(b.b)) + offset
	}
	return b.pos, nil
}

// findBoxOffset locates the first occurrence of a box type at any depth. The
// returned offset points at the box size field.
func findBoxOffset(raw []byte, typ string) (int64, error) {
	if len(raw) < 8 {
		return 0, fmt.Errorf("input too small")
	}
	var walk func(pos, end int64) (int64, bool)
	walk = func(pos, end int64) (int64, bool) {
		for pos+8 <= end {
			sz := int64(binary.BigEndian.Uint32(raw[pos:]))
			t := string(raw[pos+4 : pos+8])
			if sz == 1 {
				if pos+16 > end {
					return 0, false
				}
				sz = int64(binary.BigEndian.Uint64(raw[pos+8:]))
			} else if sz == 0 {
				sz = end - pos
			}
			if sz < 8 || pos+sz > end {
				return 0, false
			}
			if t == typ {
				return pos, true
			}
			if boxContainers[t] {
				skip := int64(0)
				if t == "dref" {
					skip = 8
				}
				if off, ok := walk(pos+8+skip, pos+sz); ok {
					return off, true
				}
			}
			pos += sz
		}
		return 0, false
	}
	if off, ok := walk(0, int64(len(raw))); ok {
		return off, nil
	}
	return 0, fmt.Errorf("box %q not found", typ)
}
