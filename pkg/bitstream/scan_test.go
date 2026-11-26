package bitstream_test

import (
	"bytes"
	"testing"

	"github.com/zld/medcheck/pkg/bitstream"
	"github.com/zld/medcheck/pkg/index"
	"github.com/zld/medcheck/pkg/media"
)

func annexBNAL(nalType byte, payload ...byte) []byte {
	hdr := nalType & 0x1f
	out := []byte{0, 0, 0, 1, hdr}
	return append(out, payload...)
}

func TestH264KeyframeOffsetsIndexRoundTripAndFloor(t *testing.T) {
	var stream []byte
	var wantOffsets []int64
	pts := []int64{0, 3003, 6006} // 29.97 fps in 90 kHz ticks

	for i := 0; i < 3; i++ {
		stream = append(stream, annexBNAL(7, 0x67, 0x42, 0x00, 0x1f)...)
		stream = append(stream, annexBNAL(8, 0x68, 0xce, 0x38, 0x80)...)
		wantOffsets = append(wantOffsets, int64(len(stream)))
		stream = append(stream, annexBNAL(5, 0x65, byte(i+1), 0x88, 0x84, 0x00)...)
		stream = append(stream, annexBNAL(1, 0x41, 0x9a, 0x00, 0x12)...)
	}

	res, err := bitstream.Scan(bytes.NewReader(stream), bitstream.CodecH264, "cam0/video")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(res.Keyframes()); got != 3 {
		t.Fatalf("keyframes=%d, want 3; issues=%v", got, res.Issues)
	}

	idx, err := index.Build("cam0/video", bitstream.CodecH264, res, index.WithTimestamps(media.Clock90k, pts))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := idx.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := index.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Records) != 3 {
		t.Fatalf("roundtrip records=%d", len(back.Records))
	}
	for i, want := range wantOffsets {
		r := back.Records[i]
		if !r.Keyframe || r.TrackID != "cam0/video" || r.Offset != want {
			t.Fatalf("record %d mismatch: %+v want offset %d", i, r, want)
		}
		if r.PTS != pts[i] || r.PTSUS == 0 && i > 0 {
			t.Fatalf("record %d timestamp not preserved: %+v", i, r)
		}
		hit, ok := back.Floor(want + 7)
		if !ok || hit.Ordinal != i {
			t.Fatalf("Floor(%d) = %+v,%v want ordinal %d", want+7, hit, ok, i)
		}
	}

	segs, err := back.Segments(int64(len(stream)))
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 3 {
		t.Fatalf("segments=%d want 3", len(segs))
	}
	for _, s := range segs {
		if h := index.HashSegmentBytes(stream, s); len(h) != 64 {
			t.Fatalf("segment %d hash length %d", s.Ordinal, len(h))
		}
	}
}

func TestH265AndAV1KeyframeDetection(t *testing.T) {
	var h265 []byte
	for _, typ := range []byte{33, 19, 1, 20, 1, 21} {
		h0 := (typ << 1) & 0x7e
		h265 = append(h265, 0, 0, 1, h0, 0x01, 0xaa, 0xbb)
	}
	res, err := bitstream.Scan(bytes.NewReader(h265), bitstream.CodecH265, "cam1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keyframes()) != 3 {
		t.Fatalf("h265 keyframes=%d units=%+v issues=%v", len(res.Keyframes()), res.Units, res.Issues)
	}

	var av1 []byte
	av1 = appendOBU(av1, 1, []byte{0x01, 0x02, 0x03})
	av1 = appendOBU(av1, 6, []byte{0x00, 0x10, 0x20}) // KEY_FRAME
	av1 = appendOBU(av1, 6, []byte{0x20, 0x10})       // INTER_FRAME
	av1 = appendOBU(av1, 3, []byte{0x00, 0x40})       // key frame header
	res, err = bitstream.Scan(bytes.NewReader(av1), bitstream.CodecAV1, "cam2")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keyframes()) != 2 {
		t.Fatalf("av1 keyframes=%d units=%+v issues=%v", len(res.Keyframes()), res.Units, res.Issues)
	}
}

func appendOBU(dst []byte, typ byte, payload []byte) []byte {
	hdr := (typ << 3) | 0x02 // has size field
	dst = append(dst, hdr, byte(len(payload)))
	return append(dst, payload...)
}

func TestCorruptNALAndOBUAreReportedButParserReturnsPriorUnits(t *testing.T) {
	good := annexBNAL(5, 0x65, 0x80, 0x12)
	truncated := []byte{0, 0, 0, 1, 0x05}
	data := append(append([]byte{}, good...), truncated...)
	res, err := bitstream.Scan(bytes.NewReader(data), bitstream.CodecH264, "broken/h264")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keyframes()) != 1 {
		t.Fatalf("expected one usable keyframe, got %+v", res.Units)
	}
	if !res.Issues.Has(media.CatTruncatedNAL) {
		t.Fatalf("missing truncated NAL issue: %+v", res.Issues)
	}
	e := firstCategory(res.Issues, media.CatTruncatedNAL)
	if e.TrackID != "broken/h264" || e.Offset < 0 {
		t.Fatalf("issue lacks track/offset: %+v", e)
	}

	illegal := []byte{(5 << 3) | 0x02}
	for i := 0; i < 10; i++ {
		illegal = append(illegal, 0x80)
	}
	obuRes, err := bitstream.Scan(bytes.NewReader(illegal), bitstream.CodecAV1, "broken/av1-length")
	if err != nil {
		t.Fatal(err)
	}
	if !obuRes.Issues.Has(media.CatIllegalLength) {
		t.Fatalf("missing illegal length issue: %+v", obuRes.Issues)
	}

	truncOBU := []byte{(5 << 3) | 0x02, 10, 0xaa, 0xbb}
	obuRes, err = bitstream.Scan(bytes.NewReader(truncOBU), bitstream.CodecAV1, "broken/av1-trunc")
	if err != nil {
		t.Fatal(err)
	}
	if !obuRes.Issues.Has(media.CatTruncatedOBU) {
		t.Fatalf("missing truncated OBU issue: %+v", obuRes.Issues)
	}
}

func firstCategory(is media.Issues, cat media.Category) *media.Error {
	for _, e := range is {
		if e.Category == cat {
			return e
		}
	}
	return nil
}
