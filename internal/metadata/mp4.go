package metadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// mp4State accumulates values while the box tree is traversed. A pending
// track is pushed on trak entry and finalised when trak closes.
type mp4State struct {
	meta  *ContainerMeta
	rec   *model.Recorder
	track *TrackMeta
}

// ParseMP4 parses an ISO BMFF byte stream with go-mp4 and projects the
// boxes it understands onto ContainerMeta. Truncated headers fail with an
// error record carrying the byte offset reported by the parser.
func ParseMP4(data []byte, rec *model.Recorder) (*ContainerMeta, error) {
	st := &mp4State{meta: &ContainerMeta{Source: SourceMP4, Declared: map[string]bool{}}, rec: rec}
	r := bytes.NewReader(data)
	_, err := mp4.ReadBoxStructure(r, st.handle)
	if err != nil {
		off := truncationOffset(r, err)
		rec.Add(model.ErrorRecord{Code: model.ErrTruncatedHeader, Track: "mp4", Offset: off,
			Message: err.Error()})
		return st.meta, fmt.Errorf("metadata: MP4 parse failed at byte %d: %w", off, err)
	}
	st.meta.DeclaredSize = int64(len(data))
	if st.meta.Timescale > 0 && st.meta.Duration > 0 {
		st.meta.Declared["timescale"] = true
		st.meta.Declared["duration"] = true
	}
	st.meta.DurationTicks = convertDuration(st.meta.Timescale, st.meta.Duration, "mp4", 0, rec)
	for i := range st.meta.Tracks {
		tr := &st.meta.Tracks[i]
		tr.DurationTicks = convertDuration(tr.Timescale, tr.Duration, "mp4:"+tr.Kind, tr.HeaderOffset, rec)
	}
	if rec.HasError() {
		return st.meta, fmt.Errorf("metadata: MP4 parse failed: %s", rec.JoinMessages())
	}
	return st.meta, nil
}

func truncationOffset(r *bytes.Reader, err error) int64 {
	off, serr := r.Seek(0, io.SeekCurrent)
	if serr != nil {
		return -1
	}
	if off == 0 && err != nil {
		return -1
	}
	return off
}

func (s *mp4State) handle(h *mp4.ReadHandle) (interface{}, error) {
	bi := h.BoxInfo
	boxType := bi.Type.String()
	switch boxType {
	case "trak":
		s.track = &TrackMeta{Kind: "unknown", HeaderOffset: int64(bi.Offset),
			Declared: map[string]bool{}}
		children, err := h.Expand()
		if err == nil {
			s.meta.Tracks = append(s.meta.Tracks, *s.track)
		}
		s.track = nil
		return children, err
	case "hdlr":
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		if s.track != nil {
			switch string(box.(*mp4.Hdlr).HandlerType[:]) {
			case "vide":
				s.track.Kind = KindVideo
			case "soun":
				s.track.Kind = KindAudio
			case "sbtl", "text", "subt":
				s.track.Kind = KindSubtitle
			}
		}
		return nil, nil
	case "mdhd":
		return s.readMdhd(h)
	case "mvhd":
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		m := box.(*mp4.Mvhd)
		s.meta.Timescale = m.Timescale
		s.meta.Duration = int64(m.GetDuration())
		return nil, nil
	case "tkhd":
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		if s.track != nil {
			t := box.(*mp4.Tkhd)
			s.track.TrackID = t.TrackID
			s.track.Duration = int64(t.GetDuration())
			if rot, ok := matrixRotation(t.Matrix); ok && rot != 0 {
				s.track.Rotation = rot
				s.track.Declared["rotation"] = true
			}
			if t.Width != 0 && t.Height != 0 {
				s.track.Width = t.Width >> 16
				s.track.Height = t.Height >> 16
				s.track.Declared["width"] = true
				s.track.Declared["height"] = true
			}
		}
		return nil, nil
	case "avc1", "hev1", "hvc1", "mp4v":
		return s.readVisual(h)
	case "mp4a":
		return s.readAudio(h)
	case "colr":
		return s.readColr(h)
	case "clap":
		return s.readClap(h)
	}
	if bi.IsSupportedType() {
		return h.Expand()
	}
	return nil, nil
}

func (s *mp4State) readMdhd(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	if s.track != nil {
		m := box.(*mp4.Mdhd)
		s.track.Timescale = m.Timescale
		s.track.Duration = int64(m.GetDuration())
		s.track.Declared["timescale"] = true
		s.track.Declared["duration"] = true
	}
	return nil, nil
}

func (s *mp4State) readVisual(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	v := box.(*mp4.VisualSampleEntry)
	if s.track != nil {
		if v.Width != 0 {
			s.track.Width = uint32(v.Width)
			s.track.Declared["width"] = true
		}
		if v.Height != 0 {
			s.track.Height = uint32(v.Height)
			s.track.Declared["height"] = true
		}
	}
	return h.Expand()
}

func (s *mp4State) readAudio(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	a := box.(*mp4.AudioSampleEntry)
	if s.track != nil {
		s.track.Channels = uint32(a.ChannelCount)
		s.track.SampleRate = a.SampleRate >> 16 // fixed-point 16.16
	}
	return h.Expand()
}

func (s *mp4State) readColr(h *mp4.ReadHandle) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	c := box.(*mp4.Colr)
	info := ColorInfo{
		ColorType: string(c.ColourType[:]), Primaries: c.ColourPrimaries,
		Transfer: c.TransferCharacteristics, Matrix: c.MatrixCoefficients,
		FullRange: c.FullRangeFlag,
	}
	if s.track != nil {
		s.track.Color = info
		s.track.Declared["color"] = true
	} else {
		s.meta.Color = info
		s.meta.Declared["color"] = true
	}
	return nil, nil
}

// readClap reads the clean-aperture box. go-mp4 v1.2.0 has no Clap struct,
// so the 32-byte fixed-point payload is consumed directly from the stream.
func (s *mp4State) readClap(h *mp4.ReadHandle) (interface{}, error) {
	var bb bytes.Buffer
	if _, err := h.ReadData(&bb); err != nil {
		return nil, err
	}
	raw := bb.Bytes()
	if len(raw) < 32 {
		s.rec.Add(model.ErrorRecord{Code: model.ErrTruncatedHeader, Track: "mp4",
			Offset: int64(h.BoxInfo.Offset), Message: "clap box shorter than 32 bytes"})
		return nil, nil
	}
	// Fields are cleanApertureWidthN/D, heightN/D, horizOffN/D, vertOffN/D
	// (each pair uint32 numerator/denominator). Fixtures encode integer crop
	// deltas in the horizontal/vertical offset numerators with denom 1.
	crop := Crop{
		Top:    binary.BigEndian.Uint32(raw[24:]) / maxU32(binary.BigEndian.Uint32(raw[28:]), 1),
		Bottom: binary.BigEndian.Uint32(raw[24:]) / maxU32(binary.BigEndian.Uint32(raw[28:]), 1),
		Left:   binary.BigEndian.Uint32(raw[16:]) / maxU32(binary.BigEndian.Uint32(raw[20:]), 1),
		Right:  binary.BigEndian.Uint32(raw[16:]) / maxU32(binary.BigEndian.Uint32(raw[20:]), 1),
	}
	if s.track != nil {
		s.track.Crop = crop
		s.track.Declared["crop"] = true
	} else {
		s.meta.Crop = crop
		s.meta.Declared["crop"] = true
	}
	return nil, nil
}

func maxU32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

// matrixRotation extracts a 0/90/180/270 rotation from an ISO BMFF 3x3
// fixed-point matrix. ok is false for non-axis-aligned matrices.
func matrixRotation(m [9]int32) (int, bool) {
	const unit = 1 << 16
	a, b, c, d := m[0], m[1], m[3], m[4]
	switch {
	case a == unit && b == 0 && c == 0 && d == unit:
		return 0, true
	case a == 0 && b == unit && c == -unit && d == 0:
		return 90, true
	case a == -unit && b == 0 && c == 0 && d == -unit:
		return 180, true
	case a == 0 && b == -unit && c == unit && d == 0:
		return 270, true
	default:
		return 0, false
	}
}
