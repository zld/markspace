package container

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediascan/internal/errs"
)

// box writes a minimal MP4 box header+payload.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// fullBox prefixes a version/flags full-box header.
func fullBox(typ string, version uint8, payload []byte) []byte {
	hdr := []byte{version, 0, 0, 0}
	return box(typ, append(hdr, payload...))
}

// buildValidMP4 constructs a tiny but structurally valid fMP4-ish file:
// ftyp + moov(trak(tkhd,mdia(mdhd,hdlr,minf(stbl(stsd,stts,stsc,stsz,stco)))))
// + mdat. Offsets are chosen so every sample span stays inside mdat.
func buildValidMP4(t *testing.T) []byte {
	t.Helper()
	ftyp := box("ftyp", []byte("isom\x00\x00\x00\x00isom"))

	// Full v0 tkhd payload: 80 bytes after the version/flags prefix;
	// go-mp4 expects the whole box at 92 bytes.
	tkhdPayload := make([]byte, 80)
	binary.BigEndian.PutUint32(tkhdPayload[8:12], 1) // track_ID = 1
	tkhd := fullBox("tkhd", 0, tkhdPayload)

	// Full v0 mdhd payload: 20 bytes after the version/flags prefix.
	mdhdPayload := make([]byte, 20)
	binary.BigEndian.PutUint32(mdhdPayload[8:12], 90000) // timescale
	binary.BigEndian.PutUint32(mdhdPayload[12:16], 9000) // duration (0.1s)
	mdhd := fullBox("mdhd", 0, mdhdPayload)

	// hdlr: pre_defined(4) + handler_type(4) + reserved(12) + name string.
	hdlrPayload := append(make([]byte, 20), 0)
	copy(hdlrPayload[4:8], "vide")
	hdlr := fullBox("hdlr", 0, hdlrPayload)

	sttsPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(sttsPayload[0:4], 1) // entry_count
	binary.BigEndian.PutUint32(sttsPayload[4:8], 4) // sample_count=4
	binary.BigEndian.PutUint32(sttsPayload[8:12], 2250) // sample_delta
	stts := fullBox("stts", 0, sttsPayload)

	stscPayload := make([]byte, 4+12)
	binary.BigEndian.PutUint32(stscPayload[0:4], 1)
	binary.BigEndian.PutUint32(stscPayload[4:8], 1)  // first_chunk
	binary.BigEndian.PutUint32(stscPayload[8:12], 4) // samples_per_chunk
	stsc := fullBox("stsc", 0, stscPayload)

	// 4 samples, constant size 10 bytes -> 40 bytes of media.
	stszPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(stszPayload[0:4], 10) // sample_size (constant)
	binary.BigEndian.PutUint32(stszPayload[4:8], 4)  // sample_count
	stsz := fullBox("stsz", 0, stszPayload)

	// stss: full-box payload = count(4) + one sample_number(4).
	stssPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(stssPayload[0:4], 1) // one keyframe
	binary.BigEndian.PutUint32(stssPayload[4:8], 1) // sample 1
	stss := fullBox("stss", 0, stssPayload)

	// stco: full-box payload = count(4) + one offset(4), patched later.
	stcoPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(stcoPayload[0:4], 1) // entry_count
	stco := fullBox("stco", 0, stcoPayload)

	// stsd: full-box header + entry_count(4)=0.
	stsd := fullBox("stsd", 0, make([]byte, 4))
	stbl := box("stbl", bytes.Join([][]byte{stsd, stts, stsc, stsz, stss, stco}, nil))
	minf := box("minf", stbl)
	mdia := box("mdia", bytes.Join([][]byte{mdhd, hdlr, minf}, nil))
	trak := box("trak", bytes.Join([][]byte{tkhd, mdia}, nil))
	moov := box("moov", trak)
	mdat := box("mdat", bytes.Repeat([]byte{0xAA}, 40))

	raw := bytes.Join([][]byte{ftyp, moov, mdat}, nil)
	// Locate boxes by their aligned type tag; bytes.Index can match inside
	// another box's binary payload.
	mdatType := indexBox(raw, "mdat")
	if mdatType < 0 {
		t.Fatal("mdat not found")
	}
	// Type tag is 4 bytes in; the 8-byte header ends 4 bytes after it.
	payloadOff := uint32(mdatType + 4)
	stcoPos := indexBox(raw, "stco")
	if stcoPos < 0 {
		t.Fatal("stco not found")
	}
	// Layout relative to the "stco" type tag:
	//   [0,4) type  [4,8) fullbox  [8,12) count  [12,16) entry
	binary.BigEndian.PutUint32(raw[stcoPos+12:stcoPos+16], payloadOff)

	if got := int64(len(raw)) - int64(payloadOff); got != 40 {
		t.Fatalf("mdat payload = %d bytes, want 40", got)
	}
	return raw
}

// indexBox returns the offset of a box's type tag by scanning aligned
// top-level/child headers (type tags always follow a 4-byte size field).
func indexBox(raw []byte, typ string) int {
	for i := 4; i+4 <= len(raw); i++ {
		if string(raw[i:i+4]) == typ {
			return i
		}
	}
	return -1
}

func TestParseValidMP4(t *testing.T) {
	raw := buildValidMP4(t)
	c, err := Parse("good.mp4", raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(c.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(c.Tracks))
	}
	tk := c.Tracks[0]
	if tk.TrackID != 1 || tk.Timescale != 90000 || tk.Duration != 9000 || tk.Samples != 4 {
		t.Fatalf("track fields wrong: %s", tk)
	}
	if tk.Handler != "vide" || len(tk.Keyframes) != 1 || tk.Keyframes[0] != 1 {
		t.Fatalf("handler/keyframes wrong: %+v", tk)
	}
	if len(tk.SampleRanges) != 4 {
		t.Fatalf("sample spans = %d, want 4", len(tk.SampleRanges))
	}
	// Last sample must end exactly at the mdat payload end (file end here,
	// since mdat is the final box).
	mdatType := indexBox(raw, "mdat")
	wantEnd := int64(mdatType) + 4 + 40 // 4 header bytes after type tag + 40 payload
	if int64(len(raw)) != wantEnd {
		t.Fatalf("file size %d, want mdat payload end %d", len(raw), wantEnd)
	}
	last := tk.SampleRanges[3]
	if last.End() != wantEnd {
		t.Fatalf("last sample ends at %d, want mdat payload end %d", last.End(), wantEnd)
	}
}

func TestTruncatedBoxReportsFirstOffset(t *testing.T) {
	raw := buildValidMP4(t)
	// Keep a valid ftyp, then claim a moov-sized header the bytes cannot satisfy.
	ftypEnd := func() int {
		var pos int
		hdr, total, ok := readBoxBounds(raw[pos:])
		if !ok {
			t.Fatal("ftyp bounds unreadable")
		}
		_ = hdr
		return pos + int(total)
	}()
	truncated := append(raw[:ftypEnd], box("moov", make([]byte, 8))...)
	// Shrink the declared moov payload beyond what exists: rewrite size bigger.
	binary.BigEndian.PutUint32(truncated[ftypEnd:ftypEnd+4], 500)
	_, err := Parse("trunc.mp4", truncated)
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindContainer {
		t.Fatalf("want KindContainer error, got %v", err)
	}
	if se.Offset != int64(ftypEnd) {
		t.Fatalf("offset = %d, want first bad box offset %d", se.Offset, ftypEnd)
	}
}

func TestTruncatedHeader(t *testing.T) {
	raw := buildValidMP4(t)
	// Cut the file 3 bytes into the moov header: the size/type header
	// itself cannot be read, so the first bad byte is the moov offset.
	moovPos := indexBox(raw, "moov") - 4
	_, err := Parse("cut.mp4", raw[:moovPos+3])
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindContainer || se.Offset != int64(moovPos) {
		t.Fatalf("want KindContainer at offset %d, got %v", moovPos, err)
	}
}

func TestTruncatedInsideMoov(t *testing.T) {
	raw := buildValidMP4(t)
	// Keep the complete moov header but cut its payload before any trak
	// child is complete: validation must reject rather than index.
	moovPos := indexBox(raw, "moov") - 4
	_, err := Parse("cutmoov.mp4", raw[:moovPos+12])
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindContainer {
		t.Fatalf("want KindContainer for truncated moov, got %v", err)
	}
	if se.Offset < int64(moovPos) {
		t.Fatalf("reported offset %d precedes moov start %d", se.Offset, moovPos)
	}
}

func TestMissingFtyp(t *testing.T) {
	raw := box("free", []byte("x"))
	_, err := Parse("noftyp.mp4", raw)
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindContainer {
		t.Fatalf("want container error for missing ftyp, got %v", err)
	}
}

func TestSampleRangeOutOfFile(t *testing.T) {
	raw := buildValidMP4(t)
	// Corrupt the stco entry (12 bytes after the "stco" type tag) so the
	// chunk starts past EOF.
	stcoPos := indexBox(raw, "stco")
	// Entry is at type+12: fullbox(4) + count(4) + entry(4).
	binary.BigEndian.PutUint32(raw[stcoPos+12:stcoPos+16], uint32(len(raw)+100))
	_, err := Parse("oor.mp4", raw)
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindTrack || se.Offset == errs.Unknown {
		t.Fatalf("want located KindTrack error, got %v", err)
	}
}

func TestOversizedFileRejected(t *testing.T) {
	_, err := Parse("big.mp4", make([]byte, MaxFileSize+1))
	se, _ := errs.AsError(err)
	if se == nil || se.Kind != errs.KindContainer {
		t.Fatalf("want size-limit error, got %v", err)
	}
}

func TestReadBoxBoundsSizes(t *testing.T) {
	cases := []struct {
		name string
		box  []byte
		ok   bool
	}{
		{"normal", box("free", []byte("1234")), true},
		{"short header", []byte{0, 0, 0}, false},
		{"bad small size", append([]byte{0, 0, 0, 4}, []byte("free")...), false},
	}
	for _, c := range cases {
		_, _, ok := readBoxBounds(c.box)
		if ok != c.ok {
			t.Fatalf("%s: ok=%v want %v", c.name, ok, c.ok)
		}
	}
}
