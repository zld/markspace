package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"mediapack/internal/model"
)

// SMPTE 377M KLV universal labels all begin with this 4-byte prefix.
var klvPrefix = [4]byte{0x06, 0x0e, 0x2b, 0x34}

const (
	ulKeySize       = 16
	tagHeaderPack   = 0x0101 // header partition pack marker
	tagPreface      = 0x0201
	tagTrack        = 0x0301
	tagTimeline     = 0x0401
	tagColor        = 0x0501
	tagRotation     = 0x0502
	tagCrop         = 0x0503
	tagEssenceLabel = 0x0601
)

// mxfTrackSet is the in-file track set layout produced by the fixture.
// All multi-byte integers are big endian; the layout is fixed and explicit so
// the scanner stays fully local with no registry dependencies.
type mxfTrackSet struct {
	ID            uint32
	Kind          [4]byte
	TimeScale     uint32
	SampleCount   uint32
	DurationTS    uint64
	Width         uint32
	Height        uint32
	RTPClockRate  uint32
	RTPBaseTS     uint32
	HasRTPAnchor  uint8
	ColorPrim     uint16
	ColorTransfer uint16
	ColorMatrix   uint16
}

const mxfTrackSetSize = 4 + 4 + 4 + 4 + 8 + 4 + 4 + 4 + 4 + 1 + 2 + 2 + 2 // 47 bytes

// ScanMXF scans the simplified SMPTE-style KLV stream used by this pipeline.
// Every reported error is anchored to the absolute offset of the bad KLV item.
func ScanMXF(path string, lim Limits) (*FileMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size > lim.MaxInputBytes {
		var es model.Errors
		es.Newf(model.StageProbe, model.CodeLimitExceeded, path, 0,
			ErrorRange(0, size), "input %d exceeds limit %d", size, lim.MaxInputBytes)
		return nil, es.Err()
	}
	sum, err := sha256File(f, size)
	if err != nil {
		return nil, err
	}
	var errs model.Errors
	out := &FileMeta{Path: path, Format: "mxf", Size: size, SHA256: sum, ScanVer: ScanVersion}
	seenPartition := false
	trackCount := 0
	off := int64(0)
	for off < size {
		itemStart := off
		key := make([]byte, ulKeySize)
		if n, rerr := f.ReadAt(key, off); rerr != nil || n != ulKeySize {
			errs.Newf(model.StageProbe, model.CodeTruncatedBox, path, 0,
				ErrorRange(itemStart, size-itemStart),
				"truncated KLV key at offset %d", itemStart)
			return nil, errs.Err()
		}
		if key[0] != klvPrefix[0] || key[1] != klvPrefix[1] ||
			key[2] != klvPrefix[2] || key[3] != klvPrefix[3] {
			errs.Newf(model.StageProbe, model.CodeIllegalLength, path, 0,
				ErrorRange(itemStart, ulKeySize),
				"not a SMPTE KLV universal label: % x", key[:4])
			return nil, errs.Err()
		}
		length, lsz, lerr := readBERLength(f, off+ulKeySize, size)
		if lerr != nil {
			errs.Newf(model.StageProbe, model.CodeIllegalLength, path, 0,
				ErrorRange(off+ulKeySize, 8), "illegal BER length: %v", lerr)
			return nil, errs.Err()
		}
		valueOff := off + ulKeySize + int64(lsz)
		if valueOff+int64(length) > size || length > uint64(lim.MaxInputBytes) {
			errs.Newf(model.StageProbe, model.CodeTruncatedBox, path, 0,
				ErrorRange(itemStart, size-itemStart),
				"KLV value length=%d overruns input at offset %d", length, itemStart)
			return nil, errs.Err()
		}
		tag := binary.BigEndian.Uint16(key[14:16])
		switch tag {
		case tagHeaderPack:
			seenPartition = true
			out.MXFOP = string(readAt(f, valueOff, min64(int64(length), 16)))
		case tagPreface:
			// Operational pattern preface; retained lightly.
		case tagTrack:
			trackCount++
			if trackCount > lim.MaxTracks {
				errs.Newf(model.StageProbe, model.CodeLimitExceeded, path, 0,
					ErrorRange(itemStart, size-itemStart),
					"track count exceeds %d", lim.MaxTracks)
				return nil, errs.Err()
			}
			tm, terr := parseMXFTrack(f, valueOff, int64(length), itemStart, path)
			if terr != nil {
				errs.Add(terr)
				return nil, errs.Err()
			}
			out.Tracks = append(out.Tracks, tm)
		case tagColor, tagRotation, tagCrop, tagTimeline, tagEssenceLabel:
			// Global metadata; track-level equivalents take precedence.
		}
		out.BoxCount++
		off = valueOff + int64(length)
		if off <= itemStart {
			errs.Newf(model.StageProbe, model.CodeIllegalLength, path, 0,
				ErrorRange(itemStart, size-itemStart), "non-advancing KLV item")
			return nil, errs.Err()
		}
	}
	if !seenPartition {
		errs.Newf(model.StageProbe, model.CodeMissingTrack, path, 0,
			ErrorRange(0, size), "no MXF header partition pack found")
		return nil, errs.Err()
	}
	if len(out.Tracks) == 0 {
		errs.Newf(model.StageProbe, model.CodeMissingTrack, path, 0,
			ErrorRange(0, size), "no playable tracks found")
		return nil, errs.Err()
	}
	return out, nil
}

func readAt(r io.ReaderAt, off, n int64) []byte {
	b := make([]byte, n)
	if _, err := r.ReadAt(b, off); err != nil {
		return nil
	}
	return b
}

// readBERLength decodes an SMPTE BER length (short or long form) and returns
// the value plus the number of bytes consumed.
func readBERLength(r io.ReaderAt, off, size int64) (uint64, int, error) {
	first := make([]byte, 1)
	if _, err := r.ReadAt(first, off); err != nil {
		return 0, 0, fmt.Errorf("length byte unreadable")
	}
	b0 := first[0]
	if b0&0x80 == 0 {
		return uint64(b0), 1, nil
	}
	n := int(b0 & 0x7f)
	if n == 0 || n > 8 || off+int64(n) > size {
		return 0, 0, fmt.Errorf("long-form length width %d invalid", n)
	}
	raw := make([]byte, n)
	if _, err := r.ReadAt(raw, off+1); err != nil {
		return 0, 0, err
	}
	var v uint64
	for _, b := range raw {
		v = v<<8 | uint64(b)
	}
	if v == 0 {
		return 0, 0, errors.New("zero BER length")
	}
	return v, n + 1, nil
}

func parseMXFTrack(r io.ReaderAt, off, length, itemStart int64, path string) (TrackMeta, *model.PipelineError) {
	if length < mxfTrackSetSize {
		return TrackMeta{}, &model.PipelineError{
			Stage: model.StageProbe, Code: model.CodeTruncatedBox, Input: path,
			ByteRange: ErrorRange(itemStart, length),
			Message:   fmt.Sprintf("track set needs %d bytes, got %d", mxfTrackSetSize, length),
		}
	}
	b := readAt(r, off, mxfTrackSetSize)
	var ts mxfTrackSet
	p := 0
	ts.ID = binary.BigEndian.Uint32(b[p:])
	p += 4
	copy(ts.Kind[:], b[p:p+4])
	p += 4
	ts.TimeScale = binary.BigEndian.Uint32(b[p:])
	p += 4
	ts.SampleCount = binary.BigEndian.Uint32(b[p:])
	p += 4
	ts.DurationTS = binary.BigEndian.Uint64(b[p:])
	p += 8
	ts.Width = binary.BigEndian.Uint32(b[p:])
	p += 4
	ts.Height = binary.BigEndian.Uint32(b[p:])
	p += 4
	ts.RTPClockRate = binary.BigEndian.Uint32(b[p:])
	p += 4
	ts.RTPBaseTS = binary.BigEndian.Uint32(b[p:])
	p += 4
	ts.HasRTPAnchor = b[p]
	p += 1
	ts.ColorPrim = binary.BigEndian.Uint16(b[p:])
	p += 2
	ts.ColorTransfer = binary.BigEndian.Uint16(b[p:])
	p += 2
	ts.ColorMatrix = binary.BigEndian.Uint16(b[p:])
	p += 2
	if ts.TimeScale == 0 {
		return TrackMeta{}, &model.PipelineError{
			Stage: model.StageProbe, Code: model.CodeIllegalLength, Input: path,
			TrackID: ts.ID, ByteRange: ErrorRange(itemStart, length),
			Message: "track declares zero time scale",
		}
	}
	tm := TrackMeta{
		ID: ts.ID, Kind: kindFromHandler(string(ts.Kind[:])),
		Handler: string(ts.Kind[:]), TimeScale: ts.TimeScale,
		SampleCount: int64(ts.SampleCount), DurationTS: int64(ts.DurationTS),
		Width: int64(ts.Width), Height: int64(ts.Height),
		RTPClockRate: ts.RTPClockRate, RTPBaseTS: ts.RTPBaseTS,
		HasRTPAnchor: ts.HasRTPAnchor == 1,
	}
	if ts.ColorPrim|ts.ColorTransfer|ts.ColorMatrix != 0 {
		tm.Color.Primaries = []SourcedValue{{Source: "mxf/ncl/primaries", Offset: itemStart, Raw: itoa(ts.ColorPrim), Value: int64(ts.ColorPrim)}}
		tm.Color.Transfer = []SourcedValue{{Source: "mxf/ncl/transfer", Offset: itemStart, Raw: itoa(ts.ColorTransfer), Value: int64(ts.ColorTransfer)}}
		tm.Color.Matrix = []SourcedValue{{Source: "mxf/ncl/matrix", Offset: itemStart, Raw: itoa(ts.ColorMatrix), Value: int64(ts.ColorMatrix)}}
	}
	return tm, nil
}

func itoa(v uint16) string { return fmt.Sprintf("%d", v) }
