package ingest

import (
	"bytes"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediapack/internal/model"
)

// mp4State carries the mutable walk state through the go-mp4 handler.
type mp4State struct {
	file  string
	out   *Container
	track *Track // track currently being descended
	traks int    // number of trak boxes entered
}

// ParseMP4 parses an MP4 file of the project subset. Boundary validation runs
// first (so truncated/oversized boxes fail with an exact offset and path),
// then go-mp4 decodes the retained payload fields.
func ParseMP4(path string) (*Container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.WrapField(model.KindInvalidInput, path, -1, "<file>",
			"cannot open input", err)
	}
	defer f.Close()
	return ParseMP4Reader(f, path)
}

// ParseMP4Reader parses an MP4 stream under a logical file name. The stream
// is fully buffered first, which lets a corrupt in-memory input be rejected
// before anything is written to an output directory.
func ParseMP4Reader(r io.Reader, name string) (*Container, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, model.WrapField(model.KindInvalidInput, name, -1, "<file>",
			"cannot read input", err)
	}
	sr := bytes.NewReader(data)
	if _, err := scanBoxBoundaries(sr, name, 0, uint64(len(data)), ""); err != nil {
		return nil, err
	}
	if _, err := sr.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	st := &mp4State{file: name, out: &Container{Format: "mp4", FileName: name}}
	if _, err := mp4.ReadBoxStructure(sr, st.handle); err != nil {
		if fe, ok := model.AsField(err); ok {
			if fe.File == "" {
				fe.File = name
			}
			return nil, fe
		}
		return nil, model.WrapField(model.KindInvalidInput, name, -1, "<root>",
			"box payload decode failed", err)
	}
	for _, t := range st.out.Tracks {
		finalizeTrack(t, st.out)
	}
	return st.out, nil
}

// handle is the ReadBoxStructure callback. Container boxes are expanded;
// leaf boxes are decoded only when the subset needs them.
func (s *mp4State) handle(h *mp4.ReadHandle) (interface{}, error) {
	typ := h.BoxInfo.Type.String()
	off := int64(h.BoxInfo.Offset)
	path := boxPath(h)

	switch typ {
	case "trak":
		s.traks++
		s.track = &Track{
			Index:   s.traks - 1,
			TrackID: uint32(s.traks),
			Sources: map[string]model.SourceRef{},
		}
		s.out.Tracks = append(s.out.Tracks, s.track)
		return h.Expand()
	case "moov", "mdia", "minf", "stbl", "stsd", "edts", "dinf", "udta":
		return h.Expand()
	case "tkhd":
		return s.onTkhd(h, off, path)
	case "mdhd":
		return s.onMdhd(h, off, path)
	case "hdlr":
		return s.onHdlr(h, off, path)
	case "stts":
		return s.onStts(h, off, path)
	case "avc1":
		return s.onVisualEntry(h, off, path)
	case "mp4a":
		return s.onAudioEntry(h, off, path)
	case "colr":
		return s.onColr(h, off, path)
	case "clap":
		return s.onClap(h, off, path)
	default:
		return nil, nil
	}
}

func boxPath(h *mp4.ReadHandle) string {
	out := ""
	for _, bt := range h.Path {
		out += "/" + bt.String()
	}
	return out
}

func (s *mp4State) onTkhd(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"tkhd payload incomplete", err)
	}
	tkhd, ok := box.(*mp4.Tkhd)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected tkhd payload type")
	}
	t := s.track
	if t == nil {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"tkhd outside trak")
	}
	t.TrackID = tkhd.TrackID
	t.Matrix = tkhd.Matrix
	t.Rotation = deriveRotation(tkhd.Matrix)
	t.Sources["matrix"] = model.SourceOnly(s.file, path+"/matrix", off)
	return nil, nil
}

func (s *mp4State) onMdhd(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"mdhd payload incomplete", err)
	}
	mdhd, ok := box.(*mp4.Mdhd)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected mdhd payload type")
	}
	t := s.track
	if t == nil {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"mdhd outside trak")
	}
	t.Timescale = mdhd.Timescale
	t.DurationSrc = mdhd.GetDuration()
	t.Sources["timescale"] = model.SourceOnly(s.file, path+"/timescale", off)
	return nil, nil
}

func (s *mp4State) onHdlr(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"hdlr payload incomplete", err)
	}
	hdlr, ok := box.(*mp4.Hdlr)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected hdlr payload type")
	}
	t := s.track
	if t == nil {
		return nil, nil
	}
	t.Handler = string(hdlr.HandlerType[:])
	switch t.Handler {
	case "vide":
		t.Kind = TrackVideo
	case "soun":
		t.Kind = TrackAudio
	default:
		t.Kind = TrackKind(t.Handler)
	}
	t.Sources["handler"] = model.SourceOnly(s.file, path+"/handler_type", off)
	return nil, nil
}

func (s *mp4State) onStts(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"stts payload incomplete", err)
	}
	stts, ok := box.(*mp4.Stts)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected stts payload type")
	}
	t := s.track
	if t == nil {
		return nil, nil
	}
	idx := 0
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			t.Samples = append(t.Samples, Sample{Index: idx, Delta: e.SampleDelta})
			idx++
		}
	}
	t.Sources["samples"] = model.SourceOnly(s.file, path, off)
	return nil, nil
}

// deriveRotation decodes the 16.16 fixed-point tkhd matrix into clockwise
// degrees. Non 90-multiple matrices return 0 so downstream rendering keeps a
// defined behavior; callers record a warning when that happens.
func deriveRotation(m [9]int32) int {
	const one = 0x00010000
	switch {
	case m[0] == one && m[4] == one:
		return 0
	case m[0] == 0 && m[1] == one && m[3] == -one && m[4] == 0:
		return 90
	case m[0] == -one && m[4] == -one:
		return 180
	case m[0] == 0 && m[1] == -one && m[3] == one && m[4] == 0:
		return 270
	default:
		return 0
	}
}

// matrixIsRotation reports whether the matrix is one of the four supported
// pure 90-degree rotations (including identity).
func matrixIsRotation(m [9]int32) bool {
	const one = 0x00010000
	return (m[0] == one && m[4] == one) ||
		(m[0] == -one && m[4] == -one) ||
		(m[0] == 0 && m[4] == 0 && (m[1] == one || m[1] == -one) &&
			(m[3] == one || m[3] == -one))
}
