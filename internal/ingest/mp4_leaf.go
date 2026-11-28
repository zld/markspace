package ingest

import (
	"bytes"
	"encoding/binary"

	"github.com/abema/go-mp4"

	"mediapack/internal/model"
)

func (s *mp4State) onVisualEntry(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"visual sample entry incomplete", err)
	}
	vse, ok := box.(*mp4.VisualSampleEntry)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected visual sample entry payload")
	}
	t := s.track
	if t == nil {
		return nil, nil
	}
	t.Width = vse.Width
	t.Height = vse.Height
	t.Sources["dimensions"] = model.SourceOnly(s.file, path+"/width,height", off)
	if vse.Width > MaxDimension || vse.Height > MaxDimension {
		return nil, model.OutOfRange(s.file, off, path+"/width,height",
			"dimensions exceed 4096x4096 subset limit")
	}
	// Child boxes (colr, clap, ...) still need descending.
	return h.Expand()
}

func (s *mp4State) onAudioEntry(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"audio sample entry incomplete", err)
	}
	ase, ok := box.(*mp4.AudioSampleEntry)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected audio sample entry payload")
	}
	t := s.track
	if t == nil {
		return nil, nil
	}
	t.Channels = ase.ChannelCount
	t.SampleSize = ase.SampleSize
	t.SampleRate = ase.SampleRate >> 16 // 16.16 fixed point
	t.Sources["audio"] = model.SourceOnly(s.file, path+"/channelcount,samplerate", off)
	return h.Expand()
}

func (s *mp4State) onColr(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, model.WrapField(model.KindTruncated, s.file, off, path,
			"colr payload incomplete", err)
	}
	colr, ok := box.(*mp4.Colr)
	if !ok {
		return nil, model.NewFieldError(model.KindInvalidInput, s.file, off, path,
			"unexpected colr payload type")
	}
	t := s.track
	if t == nil {
		return nil, nil
	}
	t.Colors = append(t.Colors, ColorDesc{
		Type:      string(colr.ColourType[:]),
		Primaries: colr.ColourPrimaries,
		Transfer:  colr.TransferCharacteristics,
		Matrix:    colr.MatrixCoefficients,
		FullRange: colr.FullRangeFlag,
		Source:    model.SourceOnly(s.file, path, off),
	})
	return nil, nil
}

// onClap decodes the 32-byte clean aperture payload manually because go-mp4
// v1.4.1 does not define the clap box. A legal crop must describe a non-empty
// rectangle inside the frame; otherwise the full aperture wins.
func (s *mp4State) onClap(h *mp4.ReadHandle, off int64, path string) (interface{}, error) {
	var sb bytes.Buffer
	n, err := h.ReadData(&sb)
	if err != nil || n < 32 {
		return nil, model.Truncated(s.file, off, path,
			"clap payload shorter than 32 bytes")
	}
	raw := sb.Bytes()
	c := &Crop{
		CleanWidthN:  binary.BigEndian.Uint32(raw[0:4]),
		CleanWidthD:  binary.BigEndian.Uint32(raw[4:8]),
		CleanHeightN: binary.BigEndian.Uint32(raw[8:12]),
		CleanHeightD: binary.BigEndian.Uint32(raw[12:16]),
		HorizOffN:    int32(binary.BigEndian.Uint32(raw[16:20])),
		HorizOffD:    int32(binary.BigEndian.Uint32(raw[20:24])),
		VertOffN:     int32(binary.BigEndian.Uint32(raw[24:28])),
		VertOffD:     int32(binary.BigEndian.Uint32(raw[28:32])),
	}
	t := s.track
	if t == nil {
		return nil, nil
	}
	x, y, w, hgt := c.EffectiveRect(uint32(t.Width), uint32(t.Height))
	legal := w > 0 && hgt > 0 &&
		x >= 0 && y >= 0 &&
		x+w <= int64(t.Width) && y+hgt <= int64(t.Height)
	if !legal {
		s.out.Warnings = append(s.out.Warnings, model.NewFieldError(
			model.KindInvalidInput, s.file, off, path,
			"clap rectangle outside frame; falling back to full aperture"))
		return nil, nil
	}
	t.Crop = c
	t.Sources["crop"] = model.SourceOnly(s.file, path, off)
	return nil, nil
}

// finalizeTrack applies cross-field rules after the walk completes.
func finalizeTrack(t *Track, c *Container) {
	if t.Kind == TrackVideo && !matrixIsRotation(t.Matrix) {
		c.Warnings = append(c.Warnings, model.NewFieldError(
			model.KindUnsupported, t.Source("matrix").File,
			t.Source("matrix").Offset, "tkhd/matrix",
			"non 90-degree matrix not supported; applying identity rotation"))
		t.Rotation = 0
	}
	if len(t.Colors) > 1 {
		first := t.Colors[0]
		for _, d := range t.Colors[1:] {
			if d.Type != first.Type || d.Primaries != first.Primaries ||
				d.Transfer != first.Transfer || d.Matrix != first.Matrix ||
				d.FullRange != first.FullRange {
				t.ColorUnresolved = true
				c.Warnings = append(c.Warnings, model.NewFieldError(
					model.KindConflict, d.Source.File, d.Source.Offset,
					d.Source.Path,
					"conflicting color descriptions retained as unresolved"))
				break
			}
		}
	}
}
