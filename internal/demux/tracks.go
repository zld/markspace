package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/track"
)

func parseMoov(moov *rawBox, fragmented bool, data []byte, movieTimescale uint32) ([]Track, error) {
	var out []Track
	for _, trak := range moov.findAll("trak") {
		tr, err := parseTrak(trak, fragmented, data, movieTimescale)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

func parseTrak(trak *rawBox, fragmented bool, data []byte, movieTimescale uint32) (Track, error) {
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
		if !fragmented {
			return tr, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, stbl.payloadOff(),
				"stbl without chunk offset table")
		}
		return tr, nil
	}
	samples, err := parseSampleTable(stbl, tr.Timescale, len(data))
	if err != nil {
		return tr, err
	}
	// An empty edit at the head of the edit list shifts the presentation of
	// the media timeline.  It is how a remux preserves an absolute start time
	// without rewriting sample tables, so it must be applied here too or a
	// round trip would silently move every sample.
	shift, err := editShift(trak.path("edts", "elst"), movieTimescale, tr.Timescale)
	if err != nil {
		return tr, err
	}
	for i := range samples {
		s := &samples[i]
		s.Data = data[s.Offset : s.Offset+int64(s.Size)]
		if shift != 0 {
			s.DTS.Ticks += shift
			s.CTS.Ticks += shift
		}
	}
	tr.Samples = samples
	return tr, nil
}

// editShift returns the presentation shift (in track ticks) contributed by a
// leading empty edit (media_time == -1).  Only the shape this writer emits is
// supported: an optional empty edit followed by media at time zero.  Any other
// edit list is reported instead of being silently ignored.
func editShift(elst *rawBox, movieTimescale, trackTimescale uint32) (int64, error) {
	if elst == nil {
		return 0, nil
	}
	if movieTimescale == 0 || trackTimescale == 0 {
		return 0, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, elst.payloadOff(),
			"edit list requires non-zero timescales")
	}
	r := newBinReader(elst, fault.OpIndexIncomplete)
	version, _, err := r.fullBox()
	if err != nil {
		return 0, err
	}
	n, err := r.u32()
	if err != nil {
		return 0, err
	}
	var shift int64
	seenEmpty := false
	for i := uint32(0); i < n; i++ {
		var duration uint64
		var mediaTime int64
		if version == 1 {
			if duration, err = r.u64(); err != nil {
				return 0, err
			}
			mt, err := r.u64()
			if err != nil {
				return 0, err
			}
			mediaTime = int64(mt)
		} else {
			d, err := r.u32()
			if err != nil {
				return 0, err
			}
			mt, err := r.u32()
			if err != nil {
				return 0, err
			}
			duration, mediaTime = uint64(d), int64(int32(mt))
		}
		if _, err := r.u32(); err != nil { // media rate
			return 0, err
		}
		if mediaTime == -1 {
			if seenEmpty {
				return 0, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, elst.payloadOff(),
					"multiple empty edits are not supported")
			}
			seenEmpty = true
			ticks, exact, err := clock.New(int64(duration), movieTimescale).Rescale(trackTimescale)
			if err != nil || !exact {
				return 0, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, elst.payloadOff(),
					"empty edit duration is not exact on the track timescale")
			}
			shift = ticks
			continue
		}
		if mediaTime != 0 {
			return 0, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, elst.payloadOff(),
				"edit list starting inside the media timeline is not supported")
		}
	}
	return shift, nil
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
		// Length-prefixed samples are only implied by an avcC box.  Without
		// one the stream must be Annex B, and 0 tells the event extractor to
		// use start codes instead of a length prefix.  avcC lives inside the
		// visual sample entry, not directly inside stsd.
		for _, entry := range stsd.children {
			for _, c := range entry.children {
				if c.typ == "avcC" {
					if n, ok := avcNALLength(c); ok {
						nal = n
					}
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
