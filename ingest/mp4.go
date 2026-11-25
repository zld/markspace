package ingest

import (
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"rtpreplay/media"
	"rtpreplay/meta"
)

// MP4Track is the metadata extracted for one MP4 track.
type MP4Track struct {
	TrackID    uint32
	Handler    string // "vide" or "soun"
	Timescale  uint32
	Duration   uint64
	Width      float64
	Height     float64
	Candidates []meta.Candidate
}

// MP4Metadata is the full parse result.
type MP4Metadata struct {
	Tracks []MP4Track
}

// ReadMP4File parses an MP4 file path with total-size bounds checking. Only
// the box set covered by the sample fixtures is interpreted.
func ReadMP4File(path string) (*MP4Metadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, media.NewError(media.KindBadInput, "STAT_FAILED", "mp4", -1, "%v", err)
	}
	if info.Size() > media.MaxInputBytes {
		return nil, media.NewError(media.KindBadInput, "INPUT_TOO_LARGE", "mp4", -1,
			"file %d bytes exceeds cap %d", info.Size(), media.MaxInputBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, media.NewError(media.KindBadInput, "OPEN_FAILED", "mp4", -1, "%v", err)
	}
	defer f.Close()
	return ReadMP4(f, info.Size())
}

// ReadMP4 parses MP4 bytes from a bounded stream. Truncated boxes surface as
// locatable errors carrying the offending byte offset.
func ReadMP4(r io.ReadSeeker, size int64) (*MP4Metadata, error) {
	if size > media.MaxInputBytes {
		return nil, media.NewError(media.KindBadInput, "INPUT_TOO_LARGE", "mp4", -1,
			"input %d bytes exceeds cap %d", size, media.MaxInputBytes)
	}
	md := &MP4Metadata{}
	var stack []*MP4Track   // current trak nesting, outermost last
	lastGoodEnd := int64(0) // declared end of the last fully parsed box
	var handler mp4.ReadHandler

	handler = func(h *mp4.ReadHandle) (interface{}, error) {
		off := int64(h.BoxInfo.Offset)
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, media.NewError(media.KindBadInput, "TRUNCATED_BOX", "mp4", off,
				"box %s: %v", h.BoxInfo.Type.String(), err)
		}
		// Only leaf boxes carry fully-consumed payload; container boxes
		// (moov/trak/mdia) declare an end beyond their parsed children and
		// must not count as complete when the walk truncates inside them.
		if !isContainerBox(h.BoxInfo.Type) {
			if end := off + int64(h.BoxInfo.Size); end > lastGoodEnd {
				lastGoodEnd = end
			}
		}

		trakDepth := len(stack)
		if h.BoxInfo.Type == mp4.BoxTypeTrak() {
			t := &MP4Track{}
			stack = append(stack, t)
		}

		if len(stack) > 0 {
			cur := stack[len(stack)-1]
			switch b := box.(type) {
			case *mp4.Tkhd:
				cur.TrackID = b.TrackID
				cur.Duration = b.GetDuration()
				cur.Width = b.GetWidth()
				cur.Height = b.GetHeight()
				rot := meta.RotationFromMatrix(b.Matrix)
				cur.Candidates = append(cur.Candidates, meta.Candidate{
					Field: meta.FieldRotation, Source: "mp4:tkhd",
					Priority: meta.PriorityMP4, Rotation: &rot,
				})
			case *mp4.Mdhd:
				cur.Timescale = b.Timescale
				if cur.Duration == 0 {
					cur.Duration = b.GetDuration()
				}
			case *mp4.Hdlr:
				cur.Handler = string(b.HandlerType[:])
			case *mp4.Colr:
				cur.Candidates = append(cur.Candidates, colrCandidate(b))
			}
		}

		// Expand tells the library walker to descend into container boxes.
		if _, err := h.Expand(); err != nil {
			return nil, err
		}
		// Pop the trak after all of its children have been visited.
		if h.BoxInfo.Type == mp4.BoxTypeTrak() && len(stack) > trakDepth {
			t := stack[len(stack)-1]
			md.Tracks = append(md.Tracks, *t)
			stack = stack[:len(stack)-1]
		}
		return box, nil
	}

	_, parseErr := mp4.ReadBoxStructure(r, handler)
	if parseErr != nil && parseErr != io.EOF {
		if _, ok := media.AsLocatable(parseErr); ok {
			return nil, parseErr
		}
		return nil, media.NewError(media.KindBadInput, "MP4_PARSE_FAILED", "mp4", -1, "%v", parseErr)
	}
	pos, perr := r.Seek(0, io.SeekCurrent)
	// Leaf-box accounting is the reliable truncation signal: buffered
	// readers make the raw seek position equal the declared size even when
	// the walk ended inside a container box. A zero lastGoodEnd means no leaf
	// box was ever parsed (e.g. a lone ftyp whose top-level handler the
	// library skips), which is a trackless file rather than truncation.
	if parseErr == io.EOF && lastGoodEnd > 0 && lastGoodEnd < size {
		return nil, media.NewError(media.KindBadInput, "TRUNCATED_BOX", "mp4", lastGoodEnd,
			"stream ended inside a box; last complete box ended at byte %d of %d", lastGoodEnd, size)
	}
	// Non-buffered readers may still expose the gap via the seek position.
	if perr == nil && pos < size {
		return nil, media.NewError(media.KindBadInput, "TRUNCATED_BOX", "mp4", pos,
			"stream ended %d bytes before the declared size", size-pos)
	}
	if len(md.Tracks) == 0 {
		return nil, media.NewError(media.KindBadInput, "NO_TRACKS", "mp4", -1, "no trak boxes found")
	}
	return md, nil
}

// isContainerBox reports boxes whose payload is itself a sequence of boxes.
func isContainerBox(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeUdta():
		return true
	}
	return false
}

func colrCandidate(b *mp4.Colr) meta.Candidate {
	c := &meta.Color{
		ColourType:              string(b.ColourType[:]),
		ColourPrimaries:         b.ColourPrimaries,
		TransferCharacteristics: b.TransferCharacteristics,
		MatrixCoefficients:      b.MatrixCoefficients,
		FullRange:               b.FullRangeFlag,
	}
	return meta.Candidate{
		Field: meta.FieldColor, Source: "mp4:colr:" + c.ColourType,
		Priority: meta.PriorityMP4, Color: c,
	}
}
