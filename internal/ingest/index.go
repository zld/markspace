package ingest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// boxKIDX is the custom key-frame index box type carried inside an MP4
// container.  The payload layout is big-endian:
//
//	uint32 codec fourcc ('avc1'|'hvc1'|'av01')
//	uint32 time-base numerator, uint32 time-base denominator
//	uint32 entry count, then per entry:
//	  uint32 unit index, uint64 source tick, uint64 byte offset, uint32 payload bytes
const boxKIDX = "kidx"

// IndexEntry is one key-frame reference in the source time base.
type IndexEntry struct {
	UnitIndex uint32
	Tick      int64
	Offset    int64
	Size      uint32
	TimeBase  clock.Rational // per-file base, shared by all entries
	Seconds   clock.Rational // exact converted timestamp, original tick retained
}

// IndexFile is a parsed key-frame index plus the codec it describes.
type IndexFile struct {
	Codec   Codec
	Base    clock.Rational
	Entries []IndexEntry
}

func codecToFourCC(c Codec) ([]byte, error) {
	switch c {
	case CodecH264:
		return []byte("avc1"), nil
	case CodecH265:
		return []byte("hvc1"), nil
	case CodecAV1:
		return []byte("av01"), nil
	default:
		return nil, fmt.Errorf("ingest: unknown codec %q", c)
	}
}

func fourCCToCodec(b []byte) (Codec, error) {
	switch string(b) {
	case "avc1":
		return CodecH264, nil
	case "hvc1":
		return CodecH265, nil
	case "av01":
		return CodecAV1, nil
	default:
		return "", fmt.Errorf("ingest: unknown index fourcc %q", string(b))
	}
}

// WriteIndex serialises an index into a single-box MP4 container using the
// go-mp4 low level writer.
func WriteIndex(f *IndexFile) ([]byte, error) {
	fourcc, err := codecToFourCC(f.Codec)
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	payload.Write(fourcc)
	if err := binary.Write(&payload, binary.BigEndian, uint32(f.Base.Num)); err != nil {
		return nil, err
	}
	if err := binary.Write(&payload, binary.BigEndian, uint32(f.Base.Den)); err != nil {
		return nil, err
	}
	if err := binary.Write(&payload, binary.BigEndian, uint32(len(f.Entries))); err != nil {
		return nil, err
	}
	for _, e := range f.Entries {
		if err := binary.Write(&payload, binary.BigEndian, e.UnitIndex); err != nil {
			return nil, err
		}
		if err := binary.Write(&payload, binary.BigEndian, uint64(e.Tick)); err != nil {
			return nil, err
		}
		if err := binary.Write(&payload, binary.BigEndian, uint64(e.Offset)); err != nil {
			return nil, err
		}
		if err := binary.Write(&payload, binary.BigEndian, e.Size); err != nil {
			return nil, err
		}
	}

	sb := &seekBuffer{}
	w := mp4.NewWriter(sb)
	bi := &mp4.BoxInfo{Type: mp4.StrToBoxType(boxKIDX), Size: uint64(payload.Len()) + 8}
	if _, err := w.StartBox(bi); err != nil {
		return nil, err
	}
	if _, err := w.Write(payload.Bytes()); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return sb.buf, nil
}

// ParseIndex reads an index container and validates every entry against the
// scanned units.  Out-of-range references, non-unique time-base conversions
// and duplicate keys are reported as faults instead of aborting the parse.
func ParseIndex(container []byte, units []Unit, lim Limits, report *fault.Report) (*IndexFile, error) {
	if report == nil {
		return nil, errors.New("ingest: nil fault report")
	}
	r := bytes.NewReader(container)
	var raw []byte
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.Path[0] == mp4.StrToBoxType(boxKIDX) {
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			raw = buf.Bytes()
		}
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: index container parse: %w", err)
	}
	if raw == nil {
		return nil, errors.New("ingest: no " + boxKIDX + " box in index container")
	}
	return decodeIndex(raw, units, lim, report)
}

func decodeIndex(raw []byte, units []Unit, lim Limits, report *fault.Report) (*IndexFile, error) {
	zero := clock.MustRational(0, 1)
	need := 12
	if len(raw) < need {
		report.Addf(fault.CodeIndexOutOfBounds, 0, zero, zero,
			"index header truncated: %d bytes", len(raw))
		return nil, errors.New("ingest: truncated index header")
	}
	codec, err := fourCCToCodec(raw[0:4])
	if err != nil {
		report.Addf(fault.CodeIndexOutOfBounds, 0, zero, zero, "%v", err)
		return nil, err
	}
	base, err := clock.NewRational(int64(binary.BigEndian.Uint32(raw[4:8])),
		int64(binary.BigEndian.Uint32(raw[8:12])))
	if err != nil {
		report.Addf(fault.CodeIndexBadTimeBase, 0, zero, zero, "%v", err)
		return nil, err
	}
	count := binary.BigEndian.Uint32(raw[12:16])
	if int(count) > lim.MaxIndexEntries {
		report.Addf(fault.CodeLimitExceeded, 12, zero, zero,
			"index entry count %d exceeds limit %d", count, lim.MaxIndexEntries)
		return nil, errors.New("ingest: index entry limit exceeded")
	}
	const entrySize = 24
	pos := 16
	if len(raw) < pos+int(count)*entrySize {
		report.Addf(fault.CodeIndexOutOfBounds, int64(pos), zero, zero,
			"index declares %d entries but container has %d bytes", count, len(raw))
		return nil, errors.New("ingest: truncated index entries")
	}

	f := &IndexFile{Codec: codec, Base: base}
	seen := make(map[uint32]struct{}, count)
	for i := uint32(0); i < count; i++ {
		eOff := int64(pos)
		unitIdx := binary.BigEndian.Uint32(raw[pos:])
		tick := int64(binary.BigEndian.Uint64(raw[pos+4:]))
		byteOff := int64(binary.BigEndian.Uint64(raw[pos+12:]))
		size := binary.BigEndian.Uint32(raw[pos+20:])
		pos += entrySize

		if _, dup := seen[unitIdx]; dup {
			report.Addf(fault.CodeIndexDuplicate, eOff, zero, zero,
				"duplicate key-frame unit index %d", unitIdx)
			continue
		}
		seen[unitIdx] = struct{}{}

		if int(unitIdx) >= len(units) {
			report.Addf(fault.CodeIndexOutOfBounds, eOff, zero, zero,
				"unit index %d references missing frame (have %d units)",
				unitIdx, len(units))
			continue
		}
		u := units[unitIdx]
		if byteOff != u.Offset || int64(size) != int64(len(u.Payload)) {
			report.Addf(fault.CodeIndexOutOfBounds, eOff, zero, zero,
				"offset/size %d:%d does not match unit %d at %d:%d",
				byteOff, size, unitIdx, u.Offset, len(u.Payload))
			continue
		}
		tb, err := clock.NewTimeBase(tick, base.Num, base.Den)
		if err != nil {
			report.Addf(fault.CodeIndexBadTimeBase, eOff, zero, zero, "%v", err)
			continue
		}
		f.Entries = append(f.Entries, IndexEntry{
			UnitIndex: unitIdx, Tick: tick, Offset: byteOff, Size: size,
			TimeBase: base, Seconds: tb.ToSeconds(),
		})
	}
	return f, nil
}

// UnifyTimeBase re-expresses every entry in dstBase ticks.  Entries that cannot
// be mapped uniquely get an INDEX_TIME_BASE_CONFLICT fault and are returned
// with Unresolved set; callers must not shift them silently.
func UnifyTimeBase(f *IndexFile, dstBase clock.Rational, report *fault.Report) []IndexEntry {
	out := make([]IndexEntry, 0, len(f.Entries))
	for _, e := range f.Entries {
		tb, _ := clock.NewTimeBase(e.Tick, f.Base.Num, f.Base.Den)
		if _, err := tb.Convert(dstBase); err != nil {
			report.Addf(fault.CodeIndexBadTimeBase, e.Offset, e.Seconds, e.Seconds,
				"tick %d in %s has no unique representation in %s",
				e.Tick, f.Base, dstBase)
			continue
		}
		out = append(out, e)
	}
	return out
}

// seekBuffer adapts an in-memory byte slice to io.WriteSeeker so go-mp4 can
// rewrite the box header once the final size is known.
type seekBuffer struct {
	buf []byte
	pos int64
}

func (s *seekBuffer) Write(p []byte) (int, error) {
	end := s.pos + int64(len(p))
	if end > int64(len(s.buf)) {
		s.buf = append(s.buf, make([]byte, end-int64(len(s.buf)))...)
	}
	copy(s.buf[s.pos:end], p)
	s.pos = end
	return len(p), nil
}

func (s *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.pos + offset
	case io.SeekEnd:
		target = int64(len(s.buf)) + offset
	}
	if target < 0 {
		return 0, errors.New("seekBuffer: negative position")
	}
	s.pos = target
	return target, nil
}
