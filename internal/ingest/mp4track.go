package ingest

import (
	"bytes"
	"io"

	"github.com/abema/go-mp4"

	"example.com/mediatimeline/internal/model"
)

// rawTrack is the box-level view of one trak before sample-table assembly.
type rawTrack struct {
	trak      *mp4.BoxInfo
	handler   string
	timescale uint32
	codec     string
	avc1      *mp4.VisualSampleEntry
	mp4a      *mp4.AudioSampleEntry
	color     *model.ColorDeclaration
	elst      *mp4.Elst
	stts      *mp4.Stts
	stsc      *mp4.Stsc
	stsz      *mp4.Stsz
	stco      *mp4.Stco
	co64      *mp4.Co64
	stss      *mp4.Stss
}

// ReadMP4 recovers one track from a complete in-memory MP4 file. Exact
// rational time is preserved: sample times are converted from the media
// timescale without float accumulation, and edit-list offsets become repair
// decisions rather than silent shifts.
func ReadMP4(data []byte, opts MP4Options) (MP4Track, error) {
	if opts.Track == "" {
		opts.Track = model.TrackVideoMain
	}
	var out MP4Track
	out.Track = opts.Track
	r := bytes.NewReader(data)

	traks, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()}})
	if err != nil {
		return out, model.WrapError(opts.Track, model.StageParse, model.CodeMalformedMP4, false, err,
			"MP4 trak extraction failed")
	}
	if len(traks) == 0 {
		return out, model.NewError(opts.Track, model.StageParse, model.CodeMalformedMP4, false,
			"MP4 contains no trak box")
	}

	var chosen *rawTrack
	for _, bi := range traks {
		rt, err := readRawTrack(r, bi)
		if err != nil {
			return out, model.WrapError(opts.Track, model.StageParse, model.CodeMalformedMP4, false, err,
				"MP4 trak could not be parsed")
		}
		if chosen == nil {
			chosen = rt
		}
		want := handlerForKind(opts.Kind)
		if want != "" && rt.handler == want {
			chosen = rt
			break
		}
	}
	return assembleTrack(r, data, chosen, opts)
}

func handlerForKind(k model.MediaKind) string {
	switch k {
	case model.KindVideo:
		return "vide"
	case model.KindAudio:
		return "soun"
	case model.KindText:
		return "sbtl"
	case model.KindChapter:
		return "text"
	default:
		return ""
	}
}

func readRawTrack(r io.ReadSeeker, trak *mp4.BoxInfo) (*rawTrack, error) {
	rt := &rawTrack{trak: trak}
	bips, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{
		{mp4.BoxTypeTkhd()},
		{mp4.BoxTypeEdts(), mp4.BoxTypeElst()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1(), mp4.BoxTypeColr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
	})
	if err != nil {
		return nil, err
	}
	for _, bip := range bips {
		switch p := bip.Payload.(type) {
		case *mp4.Elst:
			rt.elst = p
		case *mp4.Mdhd:
			rt.timescale = p.Timescale
		case *mp4.Hdlr:
			rt.handler = string(p.HandlerType[:])
		case *mp4.VisualSampleEntry:
			rt.avc1 = p
			rt.codec = "avc1"
		case *mp4.Colr:
			rt.color = colorFromColr(p)
		case *mp4.AudioSampleEntry:
			rt.mp4a = p
			rt.codec = "mp4a"
		case *mp4.Stts:
			rt.stts = p
		case *mp4.Stsc:
			rt.stsc = p
		case *mp4.Stsz:
			rt.stsz = p
		case *mp4.Stco:
			rt.stco = p
		case *mp4.Co64:
			rt.co64 = p
		case *mp4.Stss:
			rt.stss = p
		}
	}
	if rt.timescale == 0 {
		return nil, model.NewError("", model.StageParse, model.CodeMalformedMP4, false,
			"trak has no mdhd timescale")
	}
	if rt.stts == nil || rt.stsc == nil || (rt.stco == nil && rt.co64 == nil) {
		return nil, model.NewError("", model.StageParse, model.CodeMalformedMP4, false,
			"trak %s lacks stts/stsc/stco", rt.handler)
	}
	return rt, nil
}

func colorFromColr(colr *mp4.Colr) *model.ColorDeclaration {
	if string(colr.ColourType[:]) != "nclx" && string(colr.ColourType[:]) != "nclc" {
		return nil
	}
	return &model.ColorDeclaration{
		Source:    model.ColorFromContainer,
		Primaries: colr.ColourPrimaries,
		Transfer:  colr.TransferCharacteristics,
		Matrix:    colr.MatrixCoefficients,
		FullRange: colr.FullRangeFlag,
	}
}
