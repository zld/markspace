package ingest

import (
	"context"
	"testing"

	"mediad/internal/model"
)

func annexB(hdr []byte, payload []byte) []byte {
	out := append([]byte{}, startCode4...)
	out = append(out, hdr...)
	out = append(out, payload...)
	return out
}

func TestScanH264BoundariesAndKey(t *testing.T) {
	// NAL type 1 (non-IDR): 0x41 (ref_idc=2, type=1); type 5 (IDR): 0x65.
	stream := append(annexB([]byte{0x41}, []byte{1, 2, 3}),
		annexB([]byte{0x65}, []byte{4, 5, 6, 7})...)
	sc := NewScanner(ReaderConfig{})
	res, err := sc.Scan(context.Background(), CodecH264, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 2 {
		t.Fatalf("want 2 units, got %d", len(res.Units))
	}
	if res.Units[0].IsKey || !res.Units[1].IsKey {
		t.Fatal("key classification wrong")
	}
	if res.FirstKeyOffset != res.Units[1].Offset {
		t.Fatal("first key offset wrong")
	}
	// First unit spans start code + header + 3 payload bytes.
	if res.Units[0].Length != 8 {
		t.Fatalf("unit length: got %d want 8", res.Units[0].Length)
	}
}

func TestScanH264ThreeByteStartCode(t *testing.T) {
	stream := append([]byte{0, 0, 1, 0x65, 9, 9}, []byte{0, 0, 1, 0x41, 8}...)
	res, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecH264, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 2 || res.Units[0].Offset != 0 || res.Units[1].Offset != 6 {
		t.Fatalf("3-byte start code split wrong: %+v", res.Units)
	}
}

func TestScanH265KeyTypes(t *testing.T) {
	// Type 19 IDR_W_RADL: byte0 = 19<<1 = 0x26; type 1 TRAIL_N: 0x02.
	stream := append(annexB([]byte{0x02, 0x01}, []byte{0xaa}),
		annexB([]byte{0x26, 0x01}, []byte{0xbb, 0xcc})...)
	res, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecH265, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 2 {
		t.Fatalf("want 2 units, got %d", len(res.Units))
	}
	if res.Units[0].IsKey || !res.Units[1].IsKey {
		t.Fatal("H.265 key classification wrong")
	}
}

func TestScanH265TruncatedHeader(t *testing.T) {
	// Start code then only one header byte: 2-byte header cannot be read.
	stream := append([]byte{}, startCode4...)
	stream = append(stream, 0x26)
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecH265, stream)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrTruncatedUnit || se.Codec != CodecH265 {
		t.Fatalf("want structured truncation, got %v", err)
	}
}

func TestScanIllegalHeadersReportOffset(t *testing.T) {
	cases := []struct {
		name  string
		codec string
		data  []byte
	}{
		{"h264 forbidden bit", CodecH264, annexB([]byte{0x80}, []byte{1})},
		{"h264 reserved type 0", CodecH264, annexB([]byte{0x00}, []byte{1})},
		{"h265 forbidden bit", CodecH265, annexB([]byte{0x80, 0x01}, []byte{1})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), tc.codec, tc.data)
			se, ok := model.AsStructured(err)
			if !ok || se.Code != model.ErrIllegalHeader {
				t.Fatalf("want ILLEGAL_CODEC_HEADER, got %v", err)
			}
			if se.ByteOffset != 4 {
				t.Fatalf("want offset 4 (header byte), got %d", se.ByteOffset)
			}
		})
	}
}

func TestScanTruncatedUnit(t *testing.T) {
	// A trailing start code with no NAL payload must be reported.
	stream := append(annexB([]byte{0x65}, []byte{1}), startCode4...)
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecH264, stream)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrTruncatedUnit {
		t.Fatalf("want ACCESS_UNIT_TRUNCATED, got %v", err)
	}
}

func TestScanNoStartCode(t *testing.T) {
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecH264, []byte{1, 2, 3, 4})
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrIllegalHeader || se.ByteOffset != 0 {
		t.Fatalf("want header error at offset 0, got %v", err)
	}
}

func av1OBU(obuType byte, payload []byte) []byte {
	hdr := (obuType << 3) | 0x02 // has_size=1
	out := []byte{hdr}
	// LEB128 size (payloads in tests are small, single byte).
	out = append(out, byte(len(payload)))
	out = append(out, payload...)
	return out
}

func TestScanAV1KeyAndBoundaries(t *testing.T) {
	td := av1OBU(obuTemporalDelim, nil)
	// Frame header: show_existing=0, frame_type=0 (KEY_FRAME) -> 0b000xxxxx.
	keyFH := av1OBU(obuFrameHeader, []byte{0x00, 0x11, 0x22})
	// Non-key: show_existing=0, frame_type=1 -> 0b001xxxxx = 0x40.
	interFH := av1OBU(obuFrameHeader, []byte{0x40, 0x33})
	stream := append(append(td, keyFH...), interFH...)

	res, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecAV1, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 3 {
		t.Fatalf("want 3 OBUs, got %d", len(res.Units))
	}
	if !res.Units[1].IsKey || res.Units[2].IsKey {
		t.Fatal("AV1 key classification wrong")
	}
	if res.FirstKeyOffset != res.Units[1].Offset {
		t.Fatal("first key offset wrong")
	}
}

func TestScanAV1TruncatedOBU(t *testing.T) {
	stream := []byte{(obuFrameHeader << 3) | 0x02, 10, 1, 2} // declares 10, has 2
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecAV1, stream)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrTruncatedUnit || se.ByteOffset != 0 {
		t.Fatalf("want truncation at offset 0, got %v", err)
	}
}

func TestScanAV1IllegalHeader(t *testing.T) {
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecAV1, []byte{0x80, 0x00})
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrIllegalHeader || se.ByteOffset != 0 {
		t.Fatalf("want illegal header at 0, got %v", err)
	}
}

func TestScanAV1MissingSizeFlag(t *testing.T) {
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), CodecAV1, []byte{obuFrameHeader << 3, 0x00})
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrIllegalHeader {
		t.Fatalf("want illegal header for missing size flag, got %v", err)
	}
}

func TestLeb128(t *testing.T) {
	// 300 = 0xAC 0x02 in LEB128.
	v, n, err := readLeb128([]byte{0xAC, 0x02}, 0)
	if err != nil || v != 300 || n != 2 {
		t.Fatalf("leb128: v=%d n=%d err=%v", v, n, err)
	}
	if _, _, err := readLeb128([]byte{0x80}, 0); err == nil {
		t.Fatal("truncated leb128 must error")
	}
}

func TestScanUnknownCodec(t *testing.T) {
	_, err := NewScanner(ReaderConfig{}).Scan(context.Background(), "vp9", nil)
	se, ok := model.AsStructured(err)
	if !ok || se.Code != model.ErrUnknownCodec {
		t.Fatalf("want UNKNOWN_CODEC, got %v", err)
	}
}
