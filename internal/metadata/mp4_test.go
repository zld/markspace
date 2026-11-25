package metadata

import (
	"encoding/binary"
	"testing"

	"mediasync/internal/model"
)

// box builds one ISO BMFF box from type and raw payload/children.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// fullBox prepends version+flags to a payload.
func fullBox(ver uint8, body []byte) []byte {
	return append([]byte{ver, 0, 0, 0}, body...)
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

// concat joins fixed-size byte blocks without nested append calls.
func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func videoMatrix(rot int) [9]int32 {
	const unit = 1 << 16
	switch rot {
	case 90:
		return [9]int32{0, unit, 0, -unit, 0, 0, 0, 0, unit}
	case 180:
		return [9]int32{-unit, 0, 0, 0, -unit, 0, 0, 0, unit}
	case 270:
		return [9]int32{0, -unit, 0, unit, 0, 0, 0, 0, unit}
	default:
		return [9]int32{unit, 0, 0, 0, unit, 0, 0, 0, unit}
	}
}

// sampleMP4 builds a minimal moov with mvhd and two traks (video with clap +
// colr, audio). Timescales/durations are deliberately simple.
func sampleMP4(t *testing.T) []byte {
	t.Helper()
	mvhdBody := make([]byte, 0, 96)
	mvhdBody = append(mvhdBody, make([]byte, 8)...) // creation/modification
	mvhdBody = append(mvhdBody, u32(1000)...)        // timescale
	mvhdBody = append(mvhdBody, u32(2000)...)        // duration
	mvhdBody = append(mvhdBody, make([]byte, 80)...) // rate..next track id
	mvhd := box("mvhd", fullBox(0, mvhdBody))

	tkhdBody := make([]byte, 0, 84)
	tkhdBody = append(tkhdBody, make([]byte, 8)...)
	tkhdBody = append(tkhdBody, u32(1)...)                 // track id
	tkhdBody = append(tkhdBody, u32(0)...)                 // reserved
	tkhdBody = append(tkhdBody, u32(60)...)                // duration
	tkhdBody = append(tkhdBody, make([]byte, 16)...)       // reserved/layer/alt/volume/reserved
	mat := videoMatrix(90)
	for _, v := range mat {
		tkhdBody = append(tkhdBody, u32(uint32(v))...)
	}
	tkhdBody = append(tkhdBody, concat(u32(1920<<16), u32(1080<<16))...)
	tkhd := box("tkhd", fullBox(0, tkhdBody))

	mdhdBody := concat(make([]byte, 8), u32(30), u32(60), make([]byte, 4))
	mdhd := box("mdhd", fullBox(0, mdhdBody))

	hdlrBody := make([]byte, 4)
	hdlrBody = append(hdlrBody, []byte("vide")...)
	hdlrBody = append(hdlrBody, make([]byte, 12)...)
	hdlrBody = append(hdlrBody, []byte("VideoHandler\x00")...)
	hdlr := box("hdlr", fullBox(0, hdlrBody))

	// avc1 visual sample entry: 6 reserved + dref index, then fixed fields.
	avc1 := make([]byte, 78)
	binary.BigEndian.PutUint16(avc1[24:26], 1920)
	binary.BigEndian.PutUint16(avc1[26:28], 1080)
	clapRaw := make([]byte, 32)
	binary.BigEndian.PutUint32(clapRaw[16:], 30) // horiz offset num
	binary.BigEndian.PutUint32(clapRaw[20:], 1)  // denom
	binary.BigEndian.PutUint32(clapRaw[24:], 10) // vert offset num
	binary.BigEndian.PutUint32(clapRaw[28:], 1)
	clap := box("clap", clapRaw)
	colrBody := append([]byte("nclx"), u16(9)...)
	colrBody = append(colrBody, u16(16)...)
	colrBody = append(colrBody, u16(9)...)
	colrBody = append(colrBody, 0x80, 0x00) // full range flag + reserved
	colrBox := box("colr", colrBody)
	avc1Children := concat(avc1, clap, colrBox)
	stsd := box("stsd", fullBox(0, concat(u32(1), box("avc1", avc1Children))))
	minf := box("minf", box("stbl", stsd))
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	videoTrak := box("trak", append(tkhd, mdia...))

	aTkhd := concat(make([]byte, 8), u32(2), u32(0), u32(96000), make([]byte, 60))
	aMdhdBody := concat(make([]byte, 8), u32(48000), u32(96000), make([]byte, 4))
	aHdlrBody := make([]byte, 4)
	aHdlrBody = append(aHdlrBody, []byte("soun")...)
	aHdlrBody = append(aHdlrBody, make([]byte, 12)...)
	aHdlrBody = append(aHdlrBody, []byte("SoundHandler\x00")...)
	mp4a := make([]byte, 36)
	binary.BigEndian.PutUint16(mp4a[16:18], 2)         // channels (after 8-byte SampleEntry + version/reserved)
	binary.BigEndian.PutUint32(mp4a[24:28], 48000<<16) // sample rate fixed-point 16.16
	aStsd := box("stsd", fullBox(0, concat(u32(1), box("mp4a", mp4a))))
	aMdia := box("mdia", concat(box("mdhd", fullBox(0, aMdhdBody)),
		box("hdlr", fullBox(0, aHdlrBody)), box("minf", box("stbl", aStsd))))
	audioTrak := box("trak", append(box("tkhd", fullBox(0, aTkhd)), aMdia...))

	moov := box("moov", concat(mvhd, videoTrak, audioTrak))
	return concat(box("ftyp", concat([]byte("isom"), make([]byte, 4))), moov)
}

func TestParseMP4HappyPath(t *testing.T) {
	rec := model.NewRecorder()
	meta, err := ParseMP4(sampleMP4(t), rec)
	if err != nil || rec.HasError() {
		t.Fatalf("ParseMP4 failed: %v records=%v", err, rec.Records())
	}
	if meta.Source != SourceMP4 || meta.Timescale != 1000 || meta.Duration != 2000 {
		t.Fatalf("movie header wrong: %+v", meta)
	}
	if meta.DurationTicks != 180000 {
		t.Fatalf("movie duration ticks = %d, want 180000", meta.DurationTicks)
	}
	if len(meta.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(meta.Tracks))
	}
	v := meta.Tracks[0]
	if v.Kind != KindVideo || v.Rotation != 90 || !v.Declared["rotation"] {
		t.Fatalf("video rotation wrong: %+v", v)
	}
	if v.Width != 1920 || v.Height != 1080 {
		t.Fatalf("video dimensions wrong: %dx%d", v.Width, v.Height)
	}
	if v.Crop.Top != 10 || v.Crop.Left != 30 || !v.Declared["crop"] {
		t.Fatalf("clap crop wrong: %+v", v.Crop)
	}
	if v.Color.Primaries != 9 || v.Color.Transfer != 16 || v.Color.Matrix != 9 || !v.Color.FullRange {
		t.Fatalf("colr wrong: %+v", v.Color)
	}
	// 60 frames @ 30 fps = 2 s.
	if v.DurationTicks != 180000 {
		t.Fatalf("video ticks = %d, want 180000", v.DurationTicks)
	}
	a := meta.Tracks[1]
	if a.Kind != KindAudio || a.Channels != 2 || a.SampleRate != 48000 {
		t.Fatalf("audio track wrong: %+v", a)
	}
	if a.DurationTicks != 180000 {
		t.Fatalf("audio ticks = %d, want 180000", a.DurationTicks)
	}
}

func TestParseMP4Truncation(t *testing.T) {
	full := sampleMP4(t)
	// Cut the stream inside the moov box; go-mp4 must surface an error and
	// we must record a byte-addressable truncated-header record.
	cut := full[:len(full)-40]
	rec := model.NewRecorder()
	if _, err := ParseMP4(cut, rec); err == nil {
		t.Fatal("truncated MP4 must fail")
	}
	var found bool
	for _, r := range rec.Records() {
		if r.Code == model.ErrTruncatedHeader && r.Offset >= 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("want located TRUNCATED_HEADER, got %v", rec.Records())
	}
}

func TestParseMP4IllegalTimescale(t *testing.T) {
	// mvhd with timescale 0 and a non-zero duration is an invalid timestamp.
	mvhdBody := append(append(make([]byte, 8), u32(0)...), u32(100)...)
	mvhdBody = append(mvhdBody, make([]byte, 80)...)
	moov := box("moov", box("mvhd", fullBox(0, mvhdBody)))
	rec := model.NewRecorder()
	if _, err := ParseMP4(moov, rec); err == nil {
		t.Fatal("zero timescale must fail")
	}
	var found bool
	for _, r := range rec.Records() {
		if r.Code == model.ErrInvalidTimestamp {
			found = true
		}
	}
	if !found {
		t.Fatalf("want INVALID_TIMESTAMP, got %v", rec.Records())
	}
}

func TestMatrixRotation(t *testing.T) {
	for rot := 0; rot < 360; rot += 90 {
		got, ok := matrixRotation(videoMatrix(rot))
		if !ok || got != rot {
			t.Fatalf("rotation %d -> %d ok=%v", rot, got, ok)
		}
	}
	if _, ok := matrixRotation([9]int32{1, 2, 3, 4, 5, 6, 7, 8, 9}); ok {
		t.Fatal("non-axis matrix must not be interpreted as rotation")
	}
}
