package demux

import (
	"mediapipeline/internal/fault"
	"mediapipeline/internal/track"
)

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
