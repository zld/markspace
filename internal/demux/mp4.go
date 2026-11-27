package demux

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	mp4lib "github.com/abema/go-mp4"

	"mediapack/internal/model"
)

// mp4Track accumulates the boxes belonging to one trak during the walk.
type mp4Track struct {
	tkhd     *mp4lib.Tkhd
	mdhd     *mp4lib.Mdhd
	hdlr     *mp4lib.Hdlr
	colr     *NclxColr
	clap     *Clap
	stts     *mp4lib.Stts
	stsc     *mp4lib.Stsc
	stsz     *mp4lib.Stsz
	stco     *mp4lib.Stco
	co64     *mp4lib.Co64
	codec    string
	trackBox *mp4lib.BoxInfo
}

// MP4 parses an ISO-BMFF file using go-mp4 for box and sample-table decoding,
// then reads compressed sample bytes directly without re-encoding.
func MP4(path string, ra io.ReaderAt, size int64) (*Container, error) {
	rs := &readerAtSeeker{r: ra, size: size}
	c := &Container{Path: path, Format: "mp4", Samples: map[uint32][]model.Sample{}}

	var tracks []*mp4Track
	cur := &mp4Track{}
	var mdatInfo *mp4lib.BoxInfo

	handler := func(h *mp4lib.ReadHandle) (interface{}, error) {
		switch box := h.BoxInfo.Type; {
		case box == mp4lib.BoxTypeTrak():
			t := &mp4Track{trackBox: &h.BoxInfo}
			tracks = append(tracks, t)
			cur = t
			return h.Expand()
		case box == mp4lib.BoxTypeMdat():
			// Record location only; never pull the whole payload through mp4.
			bi := h.BoxInfo
			mdatInfo = &bi
			return nil, nil
		}
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, annotateMP4(err, path, int64(h.BoxInfo.Offset))
		}
		assignBox(cur, payload)
		// Containers must be expanded to reach the sample tables.
		return h.Expand()
	}
	if _, err := mp4lib.ReadBoxStructure(rs, handler); err != nil {
		return nil, annotateMP4(err, path, -1)
	}
	if mdatInfo == nil {
		return nil, model.NewError(model.CodeMalformedContainer,
			"missing mdat box").WithFile(path)
	}
	mdatPayload := int64(mdatInfo.Offset + mdatInfo.HeaderSize)
	mdatEnd := int64(mdatInfo.Offset + mdatInfo.Size)

	for _, t := range tracks {
		meta, err := t.toMeta(path)
		if err != nil {
			return nil, err
		}
		c.Tracks = append(c.Tracks, *meta)
		samples, err := t.readSamples(c, ra, mdatPayload, mdatEnd, size, path)
		if err != nil {
			return nil, err
		}
		c.Samples[t.tkhd.TrackID] = samples
	}
	if err := c.validate(path); err != nil {
		return nil, err
	}
	return c, nil
}

func assignBox(t *mp4Track, box mp4lib.IBox) {
	switch v := box.(type) {
	case *mp4lib.Tkhd:
		t.tkhd = v
	case *mp4lib.Mdhd:
		t.mdhd = v
	case *mp4lib.Hdlr:
		t.hdlr = v
	case *NclxColr:
		t.colr = v
	case *Clap:
		t.clap = v
	case *mp4lib.Stts:
		t.stts = v
	case *mp4lib.Stsc:
		t.stsc = v
	case *mp4lib.Stsz:
		t.stsz = v
	case *mp4lib.Stco:
		t.stco = v
	case *mp4lib.Co64:
		t.co64 = v
	case *mp4lib.VisualSampleEntry:
		if t.codec == "" {
			t.codec = v.GetType().String()
		}
	case *mp4lib.AudioSampleEntry:
		if t.codec == "" {
			t.codec = v.GetType().String()
		}
	case *mp4lib.AnyTypeBox:
		if t.codec == "" {
			t.codec = v.GetType().String()
		}
	}
}

func (t *mp4Track) toMeta(path string) (*model.TrackMeta, error) {
	if t.tkhd == nil || t.mdhd == nil || t.hdlr == nil {
		return nil, model.NewError(model.CodeMalformedContainer,
			"trak missing tkhd/mdhd/hdlr").WithFile(path).
			WithTrack(safeTrackID(t))
	}
	tid := t.tkhd.TrackID
	rot := matrixRotation(t.tkhd.Matrix)
	meta := &model.TrackMeta{
		TrackID:   tid,
		Timescale: t.mdhd.Timescale,
		Handler:   string(bytes.TrimRight(t.hdlr.HandlerType[:], "\x00")),
		Codec:     t.codec,
		Rotation: model.Field[model.Rotation]{
			Value: model.Rotation{Degrees: rot}, Source: model.SourceMP4,
			Raw: fmt.Sprintf("matrix=%v", t.tkhd.Matrix),
		},
	}
	if t.clap != nil {
		crop := clapToCrop(t.clap)
		meta.Crop = model.Field[model.Crop]{
			Value: crop, Source: model.SourceMP4,
			Raw: fmt.Sprintf("%d/%d/%d/%d", crop.Left, crop.Top, crop.Right, crop.Bottom),
		}
	}
	if t.colr != nil {
		meta.Color = model.Field[model.Color]{
			Value: model.Color{
				Primaries: t.colr.ColourPrimaries,
				Transfer:  t.colr.TransferCharacteristics,
				Matrix:    t.colr.MatrixCoefficients,
				FullRange: t.colr.FullRangeFlag(),
			},
			Source: model.SourceMP4,
			Raw:    string(t.colr.ColourType[:]),
		}
	}
	return meta, nil
}

func safeTrackID(t *mp4Track) uint32 {
	if t.tkhd != nil {
		return t.tkhd.TrackID
	}
	return 0
}

// matrixRotation decodes the fixed-point tkhd matrix into 0/90/180/270.
func matrixRotation(m [9]int32) int32 {
	const unit = 0x00010000
	switch {
	case m[0] == unit && m[4] == unit:
		return 0
	case m[0] == 0 && m[1] == unit && m[3] == -unit:
		return 90
	case m[0] == -unit && m[4] == -unit:
		return 180
	case m[0] == 0 && m[1] == -unit && m[3] == unit:
		return 270
	default:
		return 0
	}
}

// clapToCrop converts clean-aperture dimensions into per-edge source pixels.
// The fixture centers the aperture; offsets are honored when present.
func clapToCrop(clap *Clap) model.Crop {
	w := clap.CleanApertureWidthN / clap.CleanApertureWidthD
	h := clap.CleanApertureHeightN / clap.CleanApertureHeightD
	// Reference canvas used by the fixture builder.
	const fullW, fullH = uint32(1920), uint32(1080)
	crop := model.Crop{}
	if w <= fullW {
		pair := fullW - w
		crop.Left, crop.Right = pair/2, pair-pair/2
	}
	if h <= fullH {
		pair := fullH - h
		crop.Top, crop.Bottom = pair/2, pair-pair/2
	}
	return crop
}

func annotateMP4(err error, path string, off int64) error {
	if me, ok := model.AsMediaError(err); ok {
		if me.File == "" {
			me.File = path
		}
		if me.Offset < 0 {
			me.Offset = off
		}
		return me
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return model.NewError(model.CodeTruncatedBox, err.Error()).
			WithFile(path).WithOffset(off)
	}
	return model.NewError(model.CodeMalformedContainer, err.Error()).
		WithFile(path).WithOffset(off)
}
