package ingest

import (
	"bytes"
	"encoding/binary"
	"testing"

	"rtpreplay/media"
)

// Minimal in-memory MP4 box builder, sufficient for the fixture scope:
// ftyp/moov/trak/tkhd/mdhd/hdlr/colr with version-0 full boxes.

type boxBuilder struct{ buf bytes.Buffer }

func (b *boxBuilder) rawBox(typ string, payload []byte) []byte {
	var out bytes.Buffer
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	out.Write(hdr)
	out.Write(payload)
	return out.Bytes()
}

func fullBox(typ string, flags uint32, body []byte) []byte {
	payload := make([]byte, 4+len(body))
	payload[3] = byte(flags)
	copy(payload[4:], body)
	return (&boxBuilder{}).rawBox(typ, payload)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func tkhd(id uint32, dur uint32, matrix [9]int32) []byte {
	var body bytes.Buffer
	body.Write(make([]byte, 8)) // creation/modification
	body.Write(u32(id))
	body.Write(u32(0)) // reserved
	body.Write(u32(dur))
	body.Write(make([]byte, 8)) // reserved
	body.Write(make([]byte, 4)) // layer (2) + alternate group (2)
	body.Write(make([]byte, 4)) // volume (2) + reserved (2)
	for _, m := range matrix {
		body.Write(u32(uint32(m)))
	}
	body.Write(u32(320 << 16)) // width
	body.Write(u32(240 << 16)) // height
	return fullBox("tkhd", 7, body.Bytes())
}

func mdhd(scale uint32, dur uint32) []byte {
	var body bytes.Buffer
	body.Write(make([]byte, 8))
	body.Write(u32(scale))
	body.Write(u32(dur))
	body.Write(make([]byte, 4)) // language+pre-defined
	return fullBox("mdhd", 0, body.Bytes())
}

func hdlr(handler string) []byte {
	var body bytes.Buffer
	body.Write(make([]byte, 4)) // pre-defined
	body.Write([]byte(handler))
	body.Write(make([]byte, 12)) // name
	return fullBox("hdlr", 0, body.Bytes())
}

func colrNCLX(cp, tc, mc uint16, full bool) []byte {
	var body bytes.Buffer
	body.Write([]byte("nclx"))
	body.Write(u16(cp))
	body.Write(u16(tc))
	body.Write(u16(mc))
	flag := byte(0)
	if full {
		flag = 0x80
	}
	body.Write([]byte{flag})
	return (&boxBuilder{}).rawBox("colr", body.Bytes())
}

func sampleMP4(t *testing.T) []byte {
	t.Helper()
	var mdia bytes.Buffer
	mdia.Write(mdhd(90000, 90000))
	mdia.Write(hdlr("vide"))
	mdia.Write(colrNCLX(9, 16, 9, true))

	var trak bytes.Buffer
	identity := [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	trak.Write(tkhd(1, 90000, identity))
	trak.Write((&boxBuilder{}).rawBox("mdia", mdia.Bytes()))

	var moov bytes.Buffer
	moov.Write((&boxBuilder{}).rawBox("trak", trak.Bytes()))

	var out bytes.Buffer
	out.Write((&boxBuilder{}).rawBox("ftyp", append([]byte("isom"), make([]byte, 8)...)))
	out.Write(moov.Bytes())
	return out.Bytes()
}

func TestReadMP4VideoTrack(t *testing.T) {
	data := sampleMP4(t)
	md, err := ReadMP4(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(md.Tracks))
	}
	tr := md.Tracks[0]
	if tr.TrackID != 1 || tr.Handler != "vide" || tr.Timescale != 90000 || tr.Duration != 90000 {
		t.Fatalf("track basics wrong: %+v", tr)
	}
	if tr.Width != 320 || tr.Height != 240 {
		t.Fatalf("dimensions = %fx%f", tr.Width, tr.Height)
	}
	var sawRot, sawColor bool
	for _, c := range tr.Candidates {
		if c.Field == "rotation" && c.Rotation.Degrees == 0 {
			sawRot = true
		}
		if c.Field == "color" && c.Color != nil && c.Color.ColourPrimaries == 9 && c.Color.FullRange {
			sawColor = true
		}
	}
	if !sawRot || !sawColor {
		t.Fatalf("candidates missing: %+v", tr.Candidates)
	}
}

func TestReadMP4Rotation90(t *testing.T) {
	var mdia bytes.Buffer
	mdia.Write(mdhd(90000, 1000))
	mdia.Write(hdlr("vide"))
	rot90 := [9]int32{0, 0x10000, 0, -0x10000, 0, 0, 0, 0, 0x40000000}
	var trak bytes.Buffer
	trak.Write(tkhd(2, 1000, rot90))
	trak.Write((&boxBuilder{}).rawBox("mdia", mdia.Bytes()))
	var out bytes.Buffer
	out.Write((&boxBuilder{}).rawBox("moov", (&boxBuilder{}).rawBox("trak", trak.Bytes())))

	md, err := ReadMP4(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	cands := md.Tracks[0].Candidates
	if cands[0].Rotation == nil || cands[0].Rotation.Degrees != 90 {
		t.Fatalf("rotation candidates wrong: %+v", cands)
	}
}

func TestReadMP4TruncatedBox(t *testing.T) {
	data := sampleMP4(t)
	// Declare an oversized top-level box by extending the ftyp length field.
	corrupt := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(corrupt[:4], uint32(len(corrupt))+100)
	_, err := ReadMP4(bytes.NewReader(corrupt), int64(len(corrupt)))
	le, ok := media.AsLocatable(err)
	if !ok {
		t.Fatalf("want locatable error, got %v", err)
	}
	if le.Code != "TRUNCATED_BOX" && le.Code != "MP4_PARSE_FAILED" {
		t.Fatalf("unexpected code %s: %v", le.Code, err)
	}
}

func TestReadMP4SizeCap(t *testing.T) {
	_, err := ReadMP4(bytes.NewReader(nil), media.MaxInputBytes+1)
	if le, ok := media.AsLocatable(err); !ok || le.Code != "INPUT_TOO_LARGE" {
		t.Fatalf("got %v", err)
	}
}

func TestReadMP4NoTracks(t *testing.T) {
	// A structurally complete file (valid ftyp + empty moov) with no trak boxes.
	moov := (&boxBuilder{}).rawBox("moov", nil)
	data := append((&boxBuilder{}).rawBox("ftyp", append([]byte("isom"), make([]byte, 8)...)), moov...)
	_, err := ReadMP4(bytes.NewReader(data), int64(len(data)))
	if le, ok := media.AsLocatable(err); !ok || le.Code != "NO_TRACKS" {
		t.Fatalf("got %v", err)
	}
}

func TestReadMP4LoneFtypIsNotTruncated(t *testing.T) {
	// A lone ftyp box is structurally valid; it simply contains no tracks.
	data := (&boxBuilder{}).rawBox("ftyp", []byte("isom"))
	_, err := ReadMP4(bytes.NewReader(data), int64(len(data)))
	if le, ok := media.AsLocatable(err); !ok || le.Code != "NO_TRACKS" {
		t.Fatalf("lone ftyp must be a valid trackless file, got %v", err)
	}
}
