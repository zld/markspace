package container

import (
	"fmt"

	"archivist/internal/model"
)

// ParseContainer parses generated (or archival) bytes into normalized tracks.
func ParseContainer(data []byte, opts ParseOptions) (*ParsedFile, error) {
	var (
		res *ParseResult
		err error
	)
	if opts.AllowTruncatedRecovery {
		res, err = ParseLenient(data)
	} else {
		res, err = Parse(data)
	}
	if err != nil {
		return nil, err
	}
	pf := &ParsedFile{Root: res}
	ftyp := FindFirst(res.Boxes, BoxFtyp)
	if ftyp != nil && len(ftyp.Payload) >= 4 {
		pf.Brand = string(ftyp.Payload[0:4])
	}
	moov := FindFirst(res.Boxes, BoxMoov)
	if moov == nil {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0, "missing moov")
	}
	mvhd := FindFirst(moov.Children, BoxMvhd)
	if mvhd == nil {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0, "missing mvhd")
	}
	if err := parseMvhd(pf, mvhd); err != nil {
		return nil, err
	}
	for _, trak := range FindAll(moov.Children, BoxTrak) {
		tr, err := parseTrak(trak, res.FileLength, opts)
		if err != nil {
			return nil, err
		}
		pf.Tracks = append(pf.Tracks, tr)
		switch {
		case tr.IndexMode == "rebuilt":
			pf.Recovery = append(pf.Recovery, RecoveryNote{
				TrackID: tr.ID, Kind: "rebuilt-keyframe-index",
				Detail: "stss absent; keyframes recovered from bitstream markers",
			})
		case containsNote(tr.IndexMode, "trimmed"):
			pf.Recovery = append(pf.Recovery, RecoveryNote{
				TrackID: tr.ID, Kind: "truncated-box",
				Detail: "dangling tail samples trimmed after bounded offset check",
			})
		}
	}
	if len(pf.Tracks) > model.MaxTracks {
		return nil, model.AtTrack(model.StageContainer, model.ErrLimitExceeded, 0,
			fmt.Sprintf("tracks %d > %d", len(pf.Tracks), model.MaxTracks))
	}
	return pf, nil
}

func parseMvhd(pf *ParsedFile, box *Box) error {
	fb, ok := AsFullBox(box.Payload)
	if !ok {
		return model.AtTrack(model.StageContainer, model.ErrCorruptSampleTable, 0, "bad mvhd fullbox")
	}
	body := fb.Body
	if fb.Version == 1 {
		if len(body) < 32 {
			return model.AtTrack(model.StageContainer, model.ErrCorruptSampleTable, 0, "mvhd v1 short")
		}
		pf.MovieTimescale = U32(body[20:24])
		pf.MovieDuration = U64(body[24:32])
		return nil
	}
	if len(body) < 20 {
		return model.AtTrack(model.StageContainer, model.ErrCorruptSampleTable, 0, "mvhd v0 short")
	}
	pf.MovieTimescale = U32(body[12:16])
	pf.MovieDuration = uint64(U32(body[16:20]))
	if pf.MovieTimescale == 0 {
		return model.AtTrack(model.StageTimeline, model.ErrZeroTimescale, 0, "mvhd timescale")
	}
	return nil
}

func parseTrak(trak *Box, fileLen uint64, opts ParseOptions) (*model.Track, error) {
	mdia := FindFirst(trak.Children, BoxMdia)
	if mdia == nil {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0, "trak missing mdia")
	}
	tr := &model.Track{}
	mdhd := FindFirst(mdia.Children, BoxMdhd)
	if mdhd == nil {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, 0, "missing mdhd")
	}
	if err := parseMdhd(tr, mdhd); err != nil {
		return nil, err
	}
	if hdlr := FindFirst(mdia.Children, BoxHdlr); hdlr != nil {
		parseHdlr(tr, hdlr)
	}
	if edts := FindFirst(trak.Children, BoxEdts); edts != nil {
		if err := parseEdts(tr, edts); err != nil {
			return nil, err
		}
	}
	minf := FindFirst(mdia.Children, BoxMinf)
	if minf == nil {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, tr.ID, "missing minf")
	}
	stbl := FindFirst(minf.Children, BoxStbl)
	if stbl == nil {
		return nil, model.AtTrack(model.StageContainer, model.ErrUnsupportedBox, tr.ID, "missing stbl")
	}
	if err := parseStbl(tr, stbl, fileLen, opts); err != nil {
		return nil, err
	}
	if tr.Kind == model.KindVideo {
		parseColor(tr, stbl)
	} else {
		parseAudio(tr, stbl)
	}
	return tr, nil
}

func parseMdhd(tr *model.Track, box *Box) error {
	fb, ok := AsFullBox(box.Payload)
	if !ok || len(fb.Body) < 20 {
		return model.AtTrack(model.StageContainer, model.ErrCorruptSampleTable, 0, "bad mdhd")
	}
	// v0 body: creation(4) modification(4) timescale(4) duration(4) lang...
	tr.Timescale = U32(fb.Body[8:12])
	if tr.Timescale == 0 {
		return model.AtTrack(model.StageTimeline, model.ErrZeroTimescale, tr.ID, "mdhd timescale is zero")
	}
	if fb.Version == 1 && len(fb.Body) >= 32 {
		tr.Duration = U64(fb.Body[20:28])
	} else {
		tr.Duration = uint64(U32(fb.Body[12:16]))
	}
	if len(fb.Body) >= 22 {
		lang := uint16(fb.Body[16])<<8 | uint16(fb.Body[17])
		tr.Language = decodeLanguage(lang)
	}
	return nil
}

func parseHdlr(tr *model.Track, box *Box) {
	fb, ok := AsFullBox(box.Payload)
	if !ok || len(fb.Body) < 8 {
		return
	}
	// body: pre_defined(4) then handler_type(4)
	handler := string(fb.Body[4:8])
	tr.Handler = handler
	switch handler {
	case "vide":
		tr.Kind = model.KindVideo
	case "soun":
		tr.Kind = model.KindAudio
	default:
		tr.Kind = handler
	}
}

func parseEdts(tr *model.Track, box *Box) error {
	elst := FindFirst(box.Children, BoxElst)
	if elst == nil {
		return nil
	}
	fb, ok := AsFullBox(elst.Payload)
	if !ok || len(fb.Body) < 4 {
		return model.AtTrack(model.StageContainer, model.ErrCorruptSampleTable, tr.ID, "bad elst")
	}
	count := U32(fb.Body[0:4])
	off := 4
	for i := uint32(0); i < count; i++ {
		entryLen := 12
		if fb.Version == 1 {
			entryLen = 20
		}
		if len(fb.Body) < off+entryLen {
			return model.AtTrack(model.StageContainer, model.ErrCorruptSampleTable, tr.ID, "elst entry short")
		}
		var mediaTime int64
		var segDur uint64
		if fb.Version == 1 {
			segDur = U64(fb.Body[off : off+8])
			mediaTime = int64(U64(fb.Body[off+8 : off+16]))
			off += 20
		} else {
			segDur = uint64(U32(fb.Body[off : off+4]))
			mediaTime = int64(int32(U32(fb.Body[off+4 : off+8])))
			off += 12
		}
		rate, err := model.NewRat(1, 1)
		if err != nil {
			return err
		}
		tr.Edits = append(tr.Edits, model.Edit{
			TrackID: tr.ID, SegmentDur: segDur, MediaTime: mediaTime, MediaRate: rate,
		})
		if mediaTime > 0 {
			tr.StartDelay = uint64(mediaTime)
		}
	}
	return nil
}

// ensureUint32 validates that a value fits uint32 for 32-bit offset tables.
func ensureUint32(v uint64, trackID uint32, what string) (uint32, error) {
	if v > 0xFFFFFFFF {
		return 0, model.AtTrack(model.StageContainer, model.ErrOutOfBoundsOffset, trackID,
			fmt.Sprintf("%s=%d exceeds uint32", what, v))
	}
	return uint32(v), nil
}
