package demux

import (
	"bytes"

	"github.com/abema/go-mp4"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/track"
)

// ParseMP4 demuxes a complete MP4/fMP4 byte slice.  All returned sample data
// aliases data.
func ParseMP4(data []byte, opts Options) (*Media, error) {
	top, cut, err := parseBoxes(data, 0, int64(len(data)), "mp4", opts.TolerateTruncated)
	if err != nil {
		return nil, err
	}
	media := &Media{Format: "mp4", Cut: cut}
	if ftyp := findTop(top, "ftyp"); ftyp != nil {
		var f mp4.Ftyp
		payload := ftyp.payload()
		if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &f, mp4.Context{}); err == nil {
			media.MajorBrand = string(f.MajorBrand[:])
		}
	}
	if moov := findTop(top, "moov"); moov != nil {
		tracks, err := parseMoov(moov)
		if err != nil {
			return nil, err
		}
		media.Tracks = tracks
	}
	moofs := findTops(top, "moof")
	fragmented := len(moofs) > 0
	if fragmented && !opts.AllowMissingIndex && !hasIndex(top) {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, moofs[0].off,
			"fragmented input has neither sidx nor mfra/tfra index")
	}
	if fragmented {
		if err := appendFragmentSamples(media, top, data, opts); err != nil {
			return nil, err
		}
	}
	events, err := collectEmsgEvents(top)
	if err != nil {
		return nil, err
	}
	if opts.SampleEvents {
		sampleEvents, err := deriveSampleEvents(media)
		if err != nil {
			return nil, err
		}
		events = append(events, sampleEvents...)
	}
	if len(media.Cut) > 0 {
		// Keep events whose source byte range is still present.
		kept := events[:0]
		for _, e := range events {
			if e.SourceOffset < media.Cut[0].Start {
				kept = append(kept, e)
			}
		}
		events = kept
	}
	media.Events = events
	for i := range media.Events {
		media.Events[i].Seq = i
	}
	return media, nil
}

func findTop(boxes []*rawBox, typ string) *rawBox {
	for _, b := range boxes {
		if b.typ == typ {
			return b
		}
	}
	return nil
}

func findTops(boxes []*rawBox, typ string) []*rawBox {
	var out []*rawBox
	for _, b := range boxes {
		if b.typ == typ {
			out = append(out, b)
		}
	}
	return out
}

func hasIndex(top []*rawBox) bool {
	if findTop(top, "sidx") != nil {
		return true
	}
	mfra := findTop(top, "mfra")
	if mfra == nil {
		return false
	}
	return mfra.find("tfra") != nil
}

func parseMoov(moov *rawBox) ([]Track, error) {
	var out []Track
	for _, trak := range moov.findAll("trak") {
		tr, err := parseTrak(trak)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

func parseTrak(trak *rawBox) (Track, error) {
	tr := Track{}
	tkhd := trak.path("tkhd")
	if tkhd == nil {
		return tr, fault.New(fault.StageDemux, "mp4", fault.OpBoxHeader, trak.payloadOff(), "trak without tkhd")
	}
	r := newBinReader(tkhd, fault.OpBoxHeader)
	if _, _, err := r.fullBox(); err != nil {
		return tr, err
	}
	if err := r.skip(8); err != nil { // creation, modification
		return tr, err
	}
	id, err := r.u32()
	if err != nil {
		return tr, err
	}
	tr.ID = id

	mdhd := trak.path("mdia", "mdhd")
	if mdhd == nil {
		return tr, fault.New(fault.StageDemux, "mp4", fault.OpBoxHeader, trak.payloadOff(), "trak without mdhd")
	}
	mr := newBinReader(mdhd, fault.OpBoxHeader)
	version, _, err := mr.fullBox()
	if err != nil {
		return tr, err
	}
	skip := 8
	if version == 1 {
		skip = 16
	}
	if err := mr.skip(skip); err != nil {
		return tr, err
	}
	ts, err := mr.u32()
	if err != nil {
		return tr, err
	}
	if ts == 0 {
		return tr, fault.New(fault.StageDemux, "mp4", fault.OpDemuxInvalid, mdhd.payloadOff(), "mdhd timescale is zero")
	}
	tr.Timescale = ts

	hdlr := trak.path("mdia", "hdlr")
	handler := ""
	if hdlr != nil {
		hr := newBinReader(hdlr, fault.OpBoxHeader)
		if _, _, err := hr.fullBox(); err != nil {
			return tr, err
		}
		if err := hr.skip(4); err != nil {
			return tr, err
		}
		h, err := hr.bytes(4)
		if err != nil {
			return tr, err
		}
		handler = string(h)
	}
	tr.Handler = handler
	tr.Kind = kindForHandler(handler)

	stsd := trak.path("mdia", "minf", "stbl", "stsd")
	if stsd == nil {
		return tr, fault.New(fault.StageDemux, "mp4", fault.OpBoxHeader, trak.payloadOff(), "trak without stsd")
	}
	tr.Stsd = stsd.data[stsd.off : stsd.off+stsd.size]
	codec, nalsize, err := sampleEntryInfo(stsd)
	if err != nil {
		return tr, err
	}
	tr.Codec = codec
	tr.NALLengthSize = nalsize
	if tr.Kind == track.KindUnknown {
		tr.Kind = kindForCodec(codec)
	}

	stbl := trak.path("mdia", "minf", "stbl")
	if stbl == nil {
		return tr, nil // fragmented: samples arrive via moof
	}
	if stbl.find("stco") == nil && stbl.find("co64") == nil {
		return tr, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, stbl.payloadOff(),
			"stbl without chunk offset table")
	}
	samples, err := parseSampleTable(stbl, tr.Timescale)
	if err != nil {
		return tr, err
	}
	tr.Samples = samples
	return tr, nil
}

func kindForHandler(h string) track.Kind {
	switch h {
	case "vide":
		return track.KindVideo
	case "soun":
		return track.KindAudio
	case "subt":
		return track.KindSubtitle
	case "text":
		return track.KindChapter
	case "meta":
		return track.KindMetadata
	}
	return track.KindUnknown
}

func kindForCodec(codec string) track.Kind {
	switch codec {
	case "stpp", "sbtt", "wvtt":
		return track.KindSubtitle
	case "tx3g":
		return track.KindChapter
	case "ID3 ", "id3 ":
		return track.KindMetadata
	case "avc1", "avc3", "hvc1", "hev1", "encv":
		return track.KindVideo
	case "mp4a", "enca", "Opus":
		return track.KindAudio
	}
	return track.KindUnknown
}

func sampleEntryInfo(stsd *rawBox) (string, uint8, error) {
	r := newBinReader(stsd, fault.OpBoxHeader)
	if _, _, err := r.fullBox(); err != nil {
		return "", 0, err
	}
	count, err := r.u32()
	if err != nil {
		return "", 0, err
	}
	if count == 0 {
		return "", 0, nil
	}
	if len(r.b)-r.pos < 8 {
		return "", 0, fault.New(fault.StageDemux, "mp4", fault.OpBoxHeader, r.abs(), "stsd entry truncated")
	}
	typ := string(r.b[r.pos+4 : r.pos+8])
	var nal uint8
	if typ == "avc1" || typ == "avc3" || typ == "encv" {
		nal = 4
		for _, c := range stsd.children {
			if c.typ == "avcC" {
				if n, ok := avcNALLength(c); ok {
					nal = n
				}
			}
		}
	}
	return typ, nal, nil
}

func avcNALLength(avcC *rawBox) (uint8, bool) {
	p := avcC.payload()
	if len(p) < 5 {
		return 0, false
	}
	return (p[4] & 3) + 1, true
}
