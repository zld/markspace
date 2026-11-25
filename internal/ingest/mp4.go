package ingest

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/recover"
	"github.com/zld/mediacore/internal/trace"
)

// Document is the result of reading an MP4 container. Tracks contain only
// complete confirmed samples; Report and Damages retain partial state.
type Document struct {
	Report         *recover.ScanReport `json:"report"`
	Tracks         []*model.Track      `json:"-"`
	Damages        []model.Damage      `json:"damages,omitempty"`
	MovieTimescale uint32              `json:"movie_timescale"`
	IndexPresent   bool                `json:"index_present"`
}

// ParseMP4 reads track metadata with github.com/abema/go-mp4 and sample
// boundaries with the dependency-free recovery scanner. A non-nil document is
// often returned together with a located corruption error.
func ParseMP4(data []byte) (*Document, error) {
	rep, scanErr := recover.ScanRootBoxes(data)
	doc := &Document{Report: rep, MovieTimescale: 1000}

	var moov *recover.Box
	for i := range rep.Boxes {
		if rep.Boxes[i].Type == "moov" && rep.Boxes[i].Complete {
			moov = &rep.Boxes[i]
			break
		}
	}
	if moov == nil {
		if scanErr != nil {
			doc.Tracks, doc.Damages = recover.RebuildTracks(data, rep, nil)
			return doc, trace.Wrap(trace.StageIngest, scanErr)
		}
		err := trace.E(trace.CodeUnsupported, trace.StageIngest, "MP4 moov box not found")
		return doc, err
	}

	metas, indexPresent, movieScale, err := readMoovMeta(data, *moov)
	if err != nil {
		return doc, err
	}
	doc.MovieTimescale = movieScale
	doc.IndexPresent = indexPresent
	doc.Tracks, doc.Damages = recover.RebuildTracks(data, rep, metas)

	if scanErr != nil {
		return doc, trace.Wrap(trace.StageIngest, scanErr)
	}
	return doc, nil
}

// TrackByID returns a parsed track.
func (d *Document) TrackByID(id int) *model.Track {
	for _, tr := range d.Tracks {
		if tr.ID == id {
			return tr
		}
	}
	return nil
}

func readMoovMeta(data []byte, moov recover.Box) ([]recover.TrackMeta, bool, uint32, error) {
	moovBytes := data[moov.Offset:moov.DeclaredEnd]
	r := bytes.NewReader(moovBytes)
	movieScale := uint32(1000)

	mvhdBoxes, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()})
	if err != nil {
		return nil, false, 0, trace.E(trace.CodeBoxLengthOutOfBounds, trace.StageIngest, "parse mvhd: %v", err).WithOffset(moov.Offset)
	}
	if len(mvhdBoxes) > 0 {
		mvhd, ok := mvhdBoxes[0].Payload.(*mp4.Mvhd)
		if !ok {
			return nil, false, 0, trace.E(trace.CodeUnsupported, trace.StageIngest, "mvhd has unexpected payload %T", mvhdBoxes[0].Payload)
		}
		if mvhd.Timescale != 0 {
			movieScale = mvhd.Timescale
		}
	}

	tkhdBoxes, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()})
	if err != nil {
		return nil, false, 0, trace.E(trace.CodeBoxLengthOutOfBounds, trace.StageIngest, "parse tkhd: %v", err)
	}
	mdhdBoxes, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()})
	if err != nil {
		return nil, false, 0, trace.E(trace.CodeBoxLengthOutOfBounds, trace.StageIngest, "parse mdhd: %v", err)
	}
	hdlrBoxes, _ := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()})
	elstBoxes, _ := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeEdts(), mp4.BoxTypeElst()})
	sttsBoxes, _ := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()})

	metas := make([]recover.TrackMeta, 0, len(tkhdBoxes))
	for i, box := range tkhdBoxes {
		tkhd, ok := box.Payload.(*mp4.Tkhd)
		if !ok {
			return nil, false, 0, trace.E(trace.CodeUnsupported, trace.StageIngest, "tkhd payload type %T", box.Payload)
		}
		meta := recover.TrackMeta{ID: int(tkhd.TrackID), TimeScale: 1000, IndexPresent: len(sttsBoxes) == len(tkhdBoxes)}
		if i < len(mdhdBoxes) {
			if mdhd, ok := mdhdBoxes[i].Payload.(*mp4.Mdhd); ok && mdhd.Timescale != 0 {
				meta.TimeScale = mdhd.Timescale
			}
		}
		if i < len(hdlrBoxes) {
			if hdlr, ok := hdlrBoxes[i].Payload.(*mp4.Hdlr); ok {
				meta.Handler = string(hdlr.HandlerType[:])
				meta.Kind = handlerKind(hdlr.HandlerType)
			}
		}
		if i < len(elstBoxes) {
			if elst, ok := elstBoxes[i].Payload.(*mp4.Elst); ok {
				off, err := emptyEditOffset(elst, movieScale)
				if err != nil {
					return nil, false, 0, err
				}
				meta.OffsetNS = off
			}
		}
		metas = append(metas, meta)
	}
	if len(metas) == 0 {
		return nil, len(sttsBoxes) > 0, movieScale, trace.E(trace.CodeEmptyTrack, trace.StageIngest, "moov contains no track headers")
	}
	return metas, len(sttsBoxes) > 0, movieScale, nil
}

func handlerKind(h [4]byte) string {
	switch string(h[:]) {
	case "vide":
		return "video"
	case "soun":
		return "audio"
	default:
		return fmt.Sprintf("%s", h)
	}
}

func emptyEditOffset(elst *mp4.Elst, movieScale uint32) (model.NS, error) {
	var ticks uint64
	for i := range elst.Entries {
		if elst.GetMediaTime(i) >= 0 {
			break
		}
		ticks += elst.GetSegmentDuration(i)
	}
	if ticks == 0 {
		return 0, nil
	}
	ns, err := model.TicksToNS(ticks, movieScale)
	if err != nil {
		return 0, trace.Wrap(trace.StageIngest, err)
	}
	return ns, nil
}
