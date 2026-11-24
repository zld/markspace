package meta

import (
	"bytes"
	"io"

	go_mp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
	"mediad/internal/model"
)

// isContainerBox reports whether a box only groups children and must be
// expanded for the walk to descend.
func isContainerBox(t go_mp4.BoxType) bool {
	switch t {
	case go_mp4.BoxTypeMoov(), go_mp4.BoxTypeTrak(), go_mp4.BoxTypeMdia(),
		go_mp4.BoxTypeMinf(), go_mp4.BoxTypeStbl(), go_mp4.BoxTypeStsd(),
		go_mp4.BoxTypeDinf(), go_mp4.BoxTypeDref(), go_mp4.BoxTypeEdts(),
		// Sample entries are themselves containers for clap/colr/avcC.
		go_mp4.BoxTypeAvc1(), go_mp4.BoxTypeHev1(), go_mp4.BoxTypeHvc1(),
		go_mp4.BoxTypeMp4a(), go_mp4.BoxTypeEnca(), go_mp4.BoxTypeEncv():
		return true
	}
	return false
}

// clapBox is a custom definition for the ISO clean-aperture box, which
// go-mp4 v1.3.0 does not model. Eight uint32 fields: clean width/height as
// 16.16 fixed point, then their offsets, then pixel aspect ratio.
type clapBox struct {
	go_mp4.AnyTypeBox
	CleanWidth     uint32 `mp4:"0,size=32"`
	CleanHeight    uint32 `mp4:"1,size=32"`
	CleanWidthOff  uint32 `mp4:"2,size=32"`
	CleanHeightOff uint32 `mp4:"3,size=32"`
}

func init() {
	go_mp4.AddAnyTypeBoxDef(&clapBox{}, go_mp4.StrToBoxType("clap"))
}

// mp4Track accumulates boxes seen while the DFS walk is inside one trak.
type mp4Track struct {
	trackNo int
	kind    string
	clock   clock.Rate
	width   uint32
	height  uint32
	matrix  [9]int32
	crop    *Crop
	color   *Color
	errs    []*model.MediaError
}

// ParseMP4 reads MP4 track metadata through go-mp4 v1.3.0. Rotation is derived
// from the tkhd display matrix, crop from clap (falling back to the full
// visual sample entry), and colour from the nclx colr box.
func ParseMP4(data []byte) *Result {
	res := &Result{}
	if len(data) < 8 {
		res.Errors = append(res.Errors, model.New(model.ErrMetaUnparseable,
			"file too short for an MP4 box header (%d bytes)", len(data)).AtOffset(int64(len(data))))
		return res
	}
	var tracks []*mp4Track
	var current *mp4Track

	handler := func(h *go_mp4.ReadHandle) (interface{}, error) {
		// Leaf boxes with payloads we consume first; containers are expanded
		// afterwards so the walk descends into their children.
		switch h.BoxInfo.Type {
		case go_mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad tkhd: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
			tkhd := box.(*go_mp4.Tkhd)
			current = &mp4Track{trackNo: int(tkhd.TrackID), matrix: tkhd.Matrix}
			tracks = append(tracks, current)
		case go_mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad mdhd: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
			if current != nil {
				current.clock = clock.Rate{Num: int64(box.(*go_mp4.Mdhd).Timescale), Den: 1}
			}
		case go_mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad hdlr: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
			if current != nil {
				switch string(box.(*go_mp4.Hdlr).HandlerType[:]) {
				case "vide":
					current.kind = "video"
				case "soun":
					current.kind = "audio"
				default:
					current.kind = "other"
				}
			}
		case go_mp4.BoxTypeAvc1(), go_mp4.BoxTypeHev1(), go_mp4.BoxTypeHvc1():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad visual sample entry: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
			if current != nil {
				vse := box.(*go_mp4.VisualSampleEntry)
				current.width = uint32(vse.Width)
				current.height = uint32(vse.Height)
			}
		case go_mp4.BoxTypeMp4a():
			if _, _, err := h.ReadPayload(); err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad audio sample entry: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
		case go_mp4.StrToBoxType("clap"):
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad clap: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
			if current != nil {
				c := box.(*clapBox)
				current.crop = &Crop{
					X:      c.CleanWidthOff >> 16,
					Y:      c.CleanHeightOff >> 16,
					Width:  c.CleanWidth >> 16,
					Height: c.CleanHeight >> 16,
					Source: SourceMP4,
				}
			}
		case go_mp4.BoxTypeColr():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "bad colr: %v", err).
					AtOffset(int64(h.BoxInfo.Offset))
			}
			if current != nil {
				colr := box.(*go_mp4.Colr)
				current.color = &Color{
					Primaries: nclxName(colr.ColourPrimaries),
					Transfer:  nclxName(colr.TransferCharacteristics),
					Matrix:    nclxName(colr.MatrixCoefficients),
					Source:    SourceMP4,
				}
			}
		default:
			if !h.BoxInfo.IsSupportedType() {
				// Consume unknown leaf boxes so the walk can continue past them.
				if _, err := h.ReadData(io.Discard); err != nil {
					return nil, model.New(model.ErrMetaUnparseable, "unparseable box %q: %v",
						h.BoxInfo.Type.String(), err).AtOffset(int64(h.BoxInfo.Offset))
				}
			}
		}
		// go-mp4 only descends into a box when the handler expands it.
		if isContainerBox(h.BoxInfo.Type) {
			if _, err := h.Expand(); err != nil {
				return nil, model.New(model.ErrMetaUnparseable, "box %q: %v",
					h.BoxInfo.Type.String(), err).AtOffset(int64(h.BoxInfo.Offset))
			}
		}
		return nil, nil
	}

	if _, err := go_mp4.ReadBoxStructure(bytes.NewReader(data), handler); err != nil {
		if me := model.AsMediaError(err); me != nil {
			res.Errors = append(res.Errors, me)
			return res
		}
		// Library-level failures (truncation, oversized boxes) are structural:
		// point at the end of the input where the missing bytes were expected.
		res.Errors = append(res.Errors, model.New(model.ErrMetaUnparseable, "%v", err).
			AtOffset(int64(len(data))))
		return res
	}

	for _, tr := range tracks {
		meta, perr := finalizeMP4Track(tr)
		if perr != nil {
			res.Errors = append(res.Errors, perr)
			continue
		}
		res.Tracks = append(res.Tracks, meta)
	}
	return res
}

func finalizeMP4Track(tr *mp4Track) (*TrackMeta, *model.MediaError) {
	if !tr.clock.Valid() {
		return nil, model.New(model.ErrMetaUnparseable, "missing or invalid mdhd timescale").
			AtTrack(tr.trackNo)
	}
	rot, err := rotationFromMatrix(tr.matrix)
	if err != nil {
		return nil, err.AtTrack(tr.trackNo)
	}
	out := &TrackMeta{
		TrackNo: tr.trackNo, Kind: tr.kind, Clock: tr.clock,
		Width: tr.width, Height: tr.height,
		Rotation: rot, RotSrc: SourceMP4,
		Crop: FullFrame(tr.width, tr.height),
		Color: Color{Primaries: DefaultColorPrimaries, Transfer: DefaultColorTransfer,
			Matrix: DefaultColorMatrix, Source: SourceDefault},
	}
	if tr.crop != nil {
		out.Crop = *tr.crop
		if tr.width > 0 && tr.height > 0 {
			if cerr := validateCrop(out.Crop, tr.width, tr.height, tr.trackNo, -1); cerr != nil {
				return nil, cerr
			}
		}
	}
	if tr.color != nil {
		out.Color = *tr.color
	}
	return out, nil
}

// rotationFromMatrix only accepts the four orthogonal display matrices
// (0/90/180/270). Anything else is an unparseable track rather than a guess.
func rotationFromMatrix(m [9]int32) (int, *model.MediaError) {
	const one = 1 << 16 // 1.0 in the tkhd 16.16 fixed-point matrix
	type cell struct{ a, b int32 }
	patterns := map[[2]cell]int{
		{{one, 0}, {0, one}}:   0,
		{{0, one}, {-one, 0}}:  90,
		{{-one, 0}, {0, -one}}: 180,
		{{0, -one}, {one, 0}}:  270,
	}
	// A valid display matrix also keeps translation (m[6], m[7]) at zero and
	// the homogeneous term m[8] at 1.0.
	if m[6] != 0 || m[7] != 0 || m[8] != one {
		return 0, model.New(model.ErrMetaUnparseable, "non-orthogonal display matrix %v", m)
	}
	for p, deg := range patterns {
		if m[0] == p[0].a && m[1] == p[0].b && m[3] == p[1].a && m[4] == p[1].b {
			return deg, nil
		}
	}
	return 0, model.New(model.ErrMetaUnparseable, "unsupported rotation matrix %v", m)
}

// nclxName maps the ISO nclx colour index to a stable string token; unknown
// indices keep their numeric form so information is never silently dropped.
func nclxName(idx uint16) string {
	names := map[uint16]string{1: "bt709", 9: "bt2020", 11: "smpte2084"}
	if s, ok := names[idx]; ok {
		return s
	}
	return "nclx:" + uint16Text(idx)
}

func uint16Text(v uint16) string {
	if v == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
