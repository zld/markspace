package analyze

import (
	"bytes"
	"encoding/binary"
	"testing"

	"mediamux/media"
)

// box builds one ISO-BMFF box.
func box(typ string, payload ...[]byte) []byte {
	body := concat(payload...)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

func concat(chunks ...[]byte) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func full(version byte, flags uint32, payload ...[]byte) []byte {
	b := make([]byte, 4)
	b[0] = version
	b[1] = byte(flags >> 16)
	b[2] = byte(flags >> 8)
	b[3] = byte(flags)
	return append(b, concat(payload...)...)
}

// syntheticSegment builds a small but structurally valid fragmented MP4 with
// one video track, a constant 3000-tick sample duration at 90000Hz and a
// keyframe every five samples.
func syntheticSegment(samples int) []byte {
	mdhd := box("mdhd", full(0, 0,
		u32(0), u32(0), // creation/modification
		u32(90000),                     // timescale
		u32(uint32(samples*3000)),      // duration
		[]byte{0x55, 0xc4, 0x00, 0x00}, // language + pre_defined
	))
	tkhd := box("tkhd", full(0, 7,
		u32(0), u32(0), u32(1), // times, track id 1
		u32(0), u32(0), // reserved, duration
		u32(0), u32(0), // reserved
		u32(0), u32(0), // layer/alt group/volume/reserved
		make([]byte, 36), // matrix
		u32(1280<<16), u32(720<<16),
	))
	hdlr := box("hdlr", full(0, 0, u32(0), []byte("vide"), u32(0), u32(0), u32(0), []byte{0}))
	// One stts entry per sample keeps deltas explicit.
	var sttsEntries []byte
	for i := 0; i < samples; i++ {
		sttsEntries = append(sttsEntries, concat(u32(1), u32(3000))...)
	}
	stts := box("stts", full(0, 0, u32(uint32(samples)), sttsEntries))
	var sizes []byte
	for i := 0; i < samples; i++ {
		sizes = append(sizes, u32(uint32(100+i))...)
	}
	stsz := box("stsz", full(0, 0, u32(0), u32(uint32(samples)), sizes))
	var sync []byte
	var count uint32
	for i := 1; i <= samples; i += 5 {
		sync = append(sync, u32(uint32(i))...)
		count++
	}
	stss := box("stss", full(0, 0, u32(count), sync))
	mediaPayload := make([]byte, samples*64)
	for i := range mediaPayload {
		mediaPayload[i] = byte(i % 251)
	}
	mdat := box("mdat", mediaPayload)
	moov := box("moov", box("trak", tkhd, box("mdia", mdhd, hdlr, box("minf", box("stbl", stts, stsz, stss)))))
	return concat(box("styp", []byte("msdh"), u32(0), []byte("msdh")), moov, mdat)
}

// truncate cuts a segment short, simulating a partially written fragment.
func truncate(b []byte, keep int) []byte {
	if keep >= len(b) {
		return b
	}
	return b[:keep]
}

// corruptBoxLength overwrites the size field of the first box with a value
// larger than the container, which the scanner must reject and recover from.
func corruptBoxLength(b []byte, at int) []byte {
	out := append([]byte(nil), b...)
	binary.BigEndian.PutUint32(out[at:at+4], uint32(len(out))+4096)
	return out
}

func TestSyntheticSegmentParses(t *testing.T) {
	data := syntheticSegment(10)
	res := AnalyzeMP4(0, "seg-0", "edge-a", data, 0, MP4Options{TrackBoxes: true})
	if res.Corrupt {
		t.Fatalf("unexpected damage: %+v", res.Damaged)
	}
	if !res.HasIndex || len(res.Samples) != 10 {
		t.Fatalf("samples=%d hasIndex=%v", len(res.Samples), res.HasIndex)
	}
	if len(res.Tracks) != 1 || res.Tracks[0].Timescale != 90000 || res.Tracks[0].ID != 1 {
		t.Fatalf("tracks=%+v", res.Tracks)
	}
	if len(res.Keyframes) != 2 {
		t.Fatalf("keyframes=%v", res.Keyframes)
	}
	// Five samples of 3000/90000s is 166.67ms between the two keyframes;
	// the report rounds to nearest, so 167.
	if len(res.KeyframeGaps) != 1 || res.KeyframeGaps[0] != 167 {
		t.Fatalf("gaps=%v", res.KeyframeGaps)
	}
	if res.Duration.Value != 90000*10/30 {
		t.Fatalf("duration=%d", res.Duration.Value)
	}
	if res.SHA256 == "" || res.Bytes != int64(len(data)) {
		t.Fatalf("digest/bytes missing: %+v", res)
	}
	if res.BytesOffset != 0 {
		t.Fatalf("offset=%d", res.BytesOffset)
	}
}

func TestTruncatedSegmentIsLocated(t *testing.T) {
	data := syntheticSegment(10)
	// Cut inside the sample table, leaving the media payload short.
	cut := truncate(data, len(data)/2)
	res := AnalyzeMP4(1, "seg-1", "edge-b", cut, 4096, MP4Options{})
	if !res.Corrupt {
		t.Fatal("truncated segment must be marked corrupt")
	}
	if len(res.Damaged) == 0 {
		t.Fatal("truncation must yield a damaged region")
	}
	if res.Damaged[0].Offset < 4096 {
		t.Fatalf("damage offset %d must be absolute", res.Damaged[0].Offset)
	}
	if res.SHA256 == "" {
		t.Fatal("digest must be recorded even when corrupt")
	}
}

func TestBadBoxLengthRecoversAtNextBoundary(t *testing.T) {
	data := syntheticSegment(10)
	// Corrupt the size field of the moov box (the second top-level box).
	moovAt := bytes.Index(data, []byte("moov")) - 4
	if moovAt < 0 {
		t.Fatal("fixture has no moov")
	}
	bad := corruptBoxLength(data, moovAt)
	res := AnalyzeMP4(2, "seg-2", "edge-c", bad, 0, MP4Options{})
	if !res.Corrupt || len(res.Damaged) == 0 {
		t.Fatalf("bad length not detected: %+v", res.Damaged)
	}
	if res.Damaged[0].Code != media.CodeBoxOverflow {
		t.Fatalf("code=%s", res.Damaged[0].Code)
	}
	if res.Damaged[0].Offset != int64(moovAt) {
		t.Fatalf("offset=%d want %d", res.Damaged[0].Offset, moovAt)
	}
}

func TestBadBoxLengthDamagedSpanStopsAtRecovery(t *testing.T) {
	data := syntheticSegment(10)
	moovAt := bytes.Index(data, []byte("moov")) - 4
	res := AnalyzeMP4(0, "s", "n", corruptBoxLength(data, moovAt), 0, MP4Options{})
	if len(res.Damaged) == 0 {
		t.Fatal("no damage recorded")
	}
	// The corrupted moov header is 8 bytes; scanning resumes at the next
	// aligned box (trak) and the reported span covers only those 8 bytes.
	if got := res.Damaged[0].Length; got != 8 {
		t.Fatalf("damaged length=%d want 8", got)
	}
	if len(res.Samples) != 10 {
		t.Fatalf("recovery did not find the sample table: samples=%d", len(res.Samples))
	}
}
