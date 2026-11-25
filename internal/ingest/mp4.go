// Package ingest turns project-generated MP4/RTP fixtures into timeline
// Samples. Parsing is deliberately strict: truncated boxes, illegal box
// lengths and missing tracks surface as StageError values carrying the
// failing stage, byte offset and a recoverable flag, instead of being
// skipped silently.
package ingest

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediavault/internal/timeline"
)

// BoxType is a four-character MP4 box type.
type BoxType [4]byte

var (
	boxFtyp = BoxType{'f', 't', 'y', 'p'}
	boxMoov = BoxType{'m', 'o', 'o', 'v'}
	boxTrak = BoxType{'t', 'r', 'a', 'k'}
	boxMdia = BoxType{'m', 'd', 'i', 'a'}
	boxMinf = BoxType{'m', 'i', 'n', 'f'}
	boxMdat = BoxType{'m', 'd', 'a', 't'}
	boxMdhd = BoxType{'m', 'd', 'h', 'd'}
	boxHdlr = BoxType{'h', 'd', 'l', 'r'}
	boxStbl = BoxType{'s', 't', 'b', 'l'}
	boxStts = BoxType{'s', 't', 't', 's'}
	boxStsz = BoxType{'s', 't', 's', 'z'}
	boxStsc = BoxType{'s', 't', 's', 'c'}
	boxStco = BoxType{'s', 't', 'c', 'o'}
	boxCo64 = BoxType{'c', 'o', '6', '4'}
)

// BoxHeader is one parsed MP4 box header with its absolute file offset.
type BoxHeader struct {
	Type       BoxType
	Offset     int64 // absolute offset of the box start
	HeaderSize int64 // 8 for 32-bit sizes, 16 for largesize
	Size       int64 // total box size including the header
	PayloadOff int64 // absolute offset where payload begins
}

// rawBox reads exactly one box header at the current seek position.
func rawBox(r io.ReadSeeker) (*BoxHeader, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, &timeline.StageError{
				Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
				Offset: start, Recoverable: false,
				Detail: "box header truncated at end of file", Err: err,
			}
		}
		return nil, err
	}
	var bt BoxType
	copy(bt[:], head[4:8])
	size32 := int64(binary.BigEndian.Uint32(head[:4]))
	hdr := &BoxHeader{Type: bt, Offset: start, HeaderSize: 8, Size: size32}
	switch size32 {
	case 1: // 64-bit largesize
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, &timeline.StageError{
				Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
				Offset: start, Recoverable: false,
				Detail: "largesize box header truncated", Err: err,
			}
		}
		hdr.HeaderSize = 16
		hdr.Size = int64(binary.BigEndian.Uint64(ext[:]))
	case 0: // box extends to EOF; resolved by caller
	}
	if hdr.Size != 0 && hdr.Size < hdr.HeaderSize {
		return nil, &timeline.StageError{
			Stage: timeline.StageIngest, Kind: timeline.KindIllegalBoxLength,
			Offset: start, Recoverable: false,
			Detail: fmt.Sprintf("box %s declares size %d smaller than its %d-byte header", bt, hdr.Size, hdr.HeaderSize),
		}
	}
	hdr.PayloadOff = start + hdr.HeaderSize
	return hdr, nil
}

// walkBoxes scans sibling boxes from offset up to limit (or EOF when limit
// is negative). Container boxes named in recurse are descended into.
func walkBoxes(r io.ReadSeeker, offset, limit int64, recurse map[BoxType]bool, visit func(h *BoxHeader, payloadLimit int64) error) error {
	if _, err := r.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	pos := offset
	for limit < 0 || pos < limit {
		h, err := rawBox(r)
		if err != nil {
			// A clean EOF between sibling boxes ends the scan; a header
			// that started but is shorter than 8 bytes is real truncation.
			if se, ok := err.(*timeline.StageError); ok && se.Kind == timeline.KindTruncatedBox && limit < 0 {
				if atEOF(r, pos) {
					return nil
				}
			}
			return err
		}
		boxEnd := int64(-1)
		if h.Size > 0 {
			boxEnd = h.Offset + h.Size
			if limit >= 0 && boxEnd > limit {
				return &timeline.StageError{
					Stage: timeline.StageIngest, Kind: timeline.KindTruncatedBox,
					Offset: h.Offset, Recoverable: false,
					Detail: fmt.Sprintf("box %s size %d overruns parent/end at %d", h.Type, h.Size, limit),
				}
			}
			// At the top level a box may declare an end past a truncated
			// file EOF; clamp it so the structural scan survives and let
			// sample reads report the precise truncation offset.
			if limit < 0 {
				if eof := realEOF(r); boxEnd > eof {
					boxEnd = eof
				}
			}
		}
		if recurse[h.Type] {
			// Visit the container itself (callers collect trak/moov),
			// then descend; leaf visits happen inside the recursion.
			if err := visit(h, boxEnd); err != nil {
				return err
			}
			childLimit := boxEnd
			if h.Size == 0 {
				childLimit = limit
			}
			if err := walkBoxes(r, h.PayloadOff, childLimit, recurse, visit); err != nil {
				return err
			}
		} else if err := visit(h, boxEnd); err != nil {
			return err
		}
		if h.Size == 0 {
			return nil // EOF-anchored box terminates the scan
		}
		pos = boxEnd
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return err
		}
	}
	return nil
}

// mp4Track collects the tables needed to linearize one track.
type mp4Track struct {
	index      uint32
	kind       timeline.TrackKind
	timescale  uint32
	duration   uint64
	stts       []mp4.SttsEntry
	sizes      []uint32 // per-sample sizes; empty means uniform sampleSize
	uniform    uint32
	stsc       []mp4.StscEntry
	offsets    []uint64 // one absolute chunk offset per chunk
	offsetWide bool     // true when sourced from co64
	mdat       *BoxHeader
}

// MP4Result is the parsed representation of one MP4 fixture.
type MP4Result struct {
	Source string
	Tracks []*ParsedTrack
}

// ParsedTrack holds one track's samples and originating metadata.
type ParsedTrack struct {
	ID        timeline.TrackID
	Timescale uint32
	Samples   []timeline.Sample
	Errors    []*timeline.StageError
}

// atEOF reports whether pos is exactly the current file length. A clean
// EOF is valid between boxes; any partial header is real truncation.
func atEOF(r io.ReadSeeker, pos int64) bool {
	end := realEOF(r)
	if end < 0 {
		return false
	}
	_, _ = r.Seek(pos, io.SeekStart)
	return end == pos
}

// realEOF returns the file length in bytes, or -1 on seek failure.
func realEOF(r io.ReadSeeker) int64 {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return -1
	}
	return end
}

// containerBoxes are the structural boxes we descend into while scanning.
var containerBoxes = map[BoxType]bool{
	boxMoov: true, boxTrak: true, boxMdia: true, boxMinf: true, boxStbl: true,
}
