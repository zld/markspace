package mp4

import (
	"bytes"
	"testing"

	"mediatool/internal/timeline"
)

func asME(err error) *timeline.MediaError { return timeline.AsMediaError(err) }

// buildInit creates a minimal ftyp+moov with one video and one audio trak.
func buildInit() []byte {
	ftyp := box("ftyp", append([]byte("iso5"), 0, 0, 0, 1, 'i', 's', 'o', '5'))

	// tkhd: fullbox + creation(4) + modification(4) + trackID(4)
	tkhdV := full("tkhd", fullBox(0, 3, bytesJoin(u32(0), u32(0), u32(1))))
	mdhdV := full("mdhd", fullBox(0, 0, bytesJoin(u32(0), u32(0), u32(90000))))
	hdlrV := full("hdlr", fullBox(0, 0,
		bytesJoin(u32(0), []byte("vide"), []byte("VideoHandler\x00"))))
	stsdEntry := box("avc1", []byte("config-bytes"))
	stsd := full("stsd", bytesJoin(fullBox(0, 0, nil), u32(1), stsdEntry))
	mdiaV := box("mdia", bytesJoin(mdhdV, hdlrV, box("minf", box("stbl", stsd))))
	trakV := box("trak", bytesJoin(tkhdV, mdiaV))

	tkhdA := full("tkhd", fullBox(0, 3, bytesJoin(u32(0), u32(0), u32(2))))
	mdhdA := full("mdhd", fullBox(0, 0, bytesJoin(u32(0), u32(0), u32(48000))))
	hdlrA := full("hdlr", fullBox(0, 0,
		bytesJoin(u32(0), []byte("soun"), []byte("Sound\x00"))))
	stsdAEntry := box("mp4a", []byte("audio-config"))
	stsdA := full("stsd", bytesJoin(fullBox(0, 0, nil), u32(1), stsdAEntry))
	mdiaA := box("mdia", bytesJoin(mdhdA, hdlrA, box("minf", box("stbl", stsdA))))
	trakA := box("trak", bytesJoin(tkhdA, mdiaA))

	moov := box("moov", bytesJoin(trakV, trakA))
	return append(ftyp, moov...)
}

func bytesJoin(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestParseInitTwoTracks(t *testing.T) {
	tracks, err := ParseInit(bytes.NewReader(buildInit()))
	if err != nil {
		t.Fatalf("parse init failed: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(tracks))
	}
	v := tracks[0]
	if v.TrackID != 1 || v.Timescale != 90000 || v.MediaType != "video" || v.CodecKey != "avc1" {
		t.Fatalf("video track wrong: %+v", v)
	}
	a := tracks[1]
	if a.TrackID != 2 || a.Timescale != 48000 || a.MediaType != "audio" || a.CodecKey != "mp4a" {
		t.Fatalf("audio track wrong: %+v", a)
	}
}

func TestParseInitNoMoov(t *testing.T) {
	data := box("ftyp", []byte("iso5"))
	_, err := ParseInit(bytes.NewReader(data))
	if me := asME(err); me == nil || me.Class != timeline.ClassFieldMissing {
		t.Fatalf("want field_missing for no moov, got %v", err)
	}
}

func TestParseInitTruncatedTkhd(t *testing.T) {
	tkhd := full("tkhd", []byte{0, 0, 0, 0, 1, 2}) // too short
	mdia := box("mdia",
		full("mdhd", fullBox(0, 0, bytesJoin(u32(0), u32(0), u32(90000)))))
	trak := box("trak", bytesJoin(tkhd, mdia))
	data := box("moov", trak)
	_, err := ParseInit(bytes.NewReader(data))
	if me := asME(err); me == nil || me.Class != timeline.ClassBoxTruncated {
		t.Fatalf("want truncated error for short tkhd, got %v", err)
	}
}
