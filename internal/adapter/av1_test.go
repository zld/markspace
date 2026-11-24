package adapter

import (
	"testing"

	"archive-recovery/internal/media"
)

// encodeLEB128 produces the canonical unsigned LEB128 encoding of v.
func encodeLEB128(v int) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			out = append(out, b)
			return out
		}
	}
}

// buildOBU wraps a payload into an AV1 OBU with a size field.
func buildOBU(obuType int, payload []byte) []byte {
	h := byte(obuType)<<3 | 0x02 // has_size_field=1
	out := []byte{h}
	out = append(out, encodeLEB128(len(payload))...)
	return append(out, payload...)
}

func TestDecodeLEB128(t *testing.T) {
	cases := []struct {
		in     []byte
		want   int64
		wantN  int
		wantOK bool
	}{
		{[]byte{0x00}, 0, 1, true},
		{[]byte{0x7f}, 127, 1, true},
		{[]byte{0x80, 0x01}, 128, 2, true},
		{[]byte{0xe5, 0x8e, 0x26}, 624485, 3, true},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
			1<<56 - 1, 8, true}, // maximum value expressible in 8 LEB128 bytes
		{[]byte{0x80}, 0, 0, false},       // truncated continuation
		{[]byte{0x80, 0x00}, 0, 0, false}, // non-canonical overlong
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
			0, 0, false}, // 9th continuation byte: overlong length field
	}
	for _, tc := range cases {
		v, n, ok := decodeLEB128(tc.in)
		if ok != tc.wantOK || (ok && (v != tc.want || n != tc.wantN)) {
			t.Errorf("decodeLEB128(%v) = (%d,%d,%v) want (%d,%d,%v)",
				tc.in, v, n, ok, tc.want, tc.wantN, tc.wantOK)
		}
	}
}

func TestParseOBUHeader(t *testing.T) {
	if _, _, _, e := parseOBUHeader([]byte{0x80}); e == nil {
		t.Fatal("forbidden bit must be rejected")
	}
	n, typ, hasSize, e := parseOBUHeader([]byte{byte(obuSeqHeader)<<3 | 0x02})
	if e != nil || n != 1 || typ != obuSeqHeader || !hasSize {
		t.Fatalf("basic header parse wrong: %d %d %v %v", n, typ, hasSize, e)
	}
	ext := byte(obuMetadata)<<3 | 0x04 | 0x02
	if n, _, _, e := parseOBUHeader([]byte{ext, 0x00}); e != nil || n != 2 {
		t.Fatalf("extension header must consume 2 bytes: n=%d err=%v", n, e)
	}
}

func TestAV1SplitterFullStream(t *testing.T) {
	stream := append(buildOBU(obuSeqHeader, []byte{0xaa, 0xbb}),
		buildOBU(obuTemporalDelim, nil)...)
	stream = append(stream, buildOBU(obuFrame, []byte{0x00, 0x11})...) // key frame
	stream = append(stream, buildOBU(obuFrame, []byte{0x40, 0x12})...) // P frame
	stream = append(stream, buildOBU(obuTileGroup, []byte{0x33})...)   // not a frame
	stream = append(stream, buildOBU(obuFrameHeader, []byte{0x00})...) // key header

	sp := AV1Adapter{}.NewSplitter()
	units, uerrs, err := sp.Write(stream)
	if err != nil || len(uerrs) != 0 {
		t.Fatalf("unexpected errors: %v %v", err, uerrs)
	}
	tail, dangling := sp.Close()
	if dangling != nil {
		t.Fatal(dangling)
	}
	units = append(units, tail...)
	if len(units) != 6 {
		t.Fatalf("want 6 OBUs, got %d", len(units))
	}
	if !units[2].IsFrame || !units[2].IsKeyframe {
		t.Fatalf("unit 2 must be keyframe: %+v", units[2])
	}
	if !units[3].IsFrame || units[3].IsKeyframe {
		t.Fatalf("unit 3 must be non-key frame: %+v", units[3])
	}
	if units[4].IsFrame {
		t.Fatalf("tile group must not be a frame: %+v", units[4])
	}
	if !units[5].IsKeyframe {
		t.Fatalf("frame header OBU must carry keyframe flag: %+v", units[5])
	}
}

func TestAV1TruncatedOBUNotIndexed(t *testing.T) {
	good := buildOBU(obuFrame, []byte{0x00, 1, 2, 3})
	// Header + LEB claiming 50 payload bytes, then only 3.
	bad := []byte{byte(obuFrame)<<3 | 0x02, 50, 1, 2, 3}
	stream := append(append([]byte{}, good...), bad...)

	sp := AV1Adapter{}.NewSplitter()
	first, uerrs, err := sp.Write(stream)
	if err != nil || len(uerrs) != 0 {
		t.Fatalf("unexpected immediate errors: %v %v", err, uerrs)
	}
	tail, dangling := sp.Close()
	all := append(first, tail...)
	if len(all) != 1 || !all[0].IsKeyframe {
		t.Fatalf("only the good unit may be indexed, got %+v", all)
	}
	if dangling == nil || dangling.Class != media.ClassRecoverable {
		t.Fatalf("want recoverable dangling OBU, got %+v", dangling)
	}
	if dangling.Offset != int64(len(good)) {
		t.Fatalf("dangling offset=%d want %d", dangling.Offset, len(good))
	}
}

func TestAV1ForbiddenBitResync(t *testing.T) {
	good := buildOBU(obuFrame, []byte{0x00})
	stream := append([]byte{0x80}, good...) // forbidden header, then valid OBU
	sp := AV1Adapter{}.NewSplitter()
	units, uerrs, err := sp.Write(stream)
	if err != nil {
		t.Fatal(err)
	}
	if _, dangling := sp.Close(); dangling != nil {
		t.Fatal(dangling)
	}
	if len(units) != 1 || !units[0].IsKeyframe || units[0].Offset != 1 {
		t.Fatalf("parser must resync after bad byte: %+v", units)
	}
	if len(uerrs) != 1 || uerrs[0].Class != media.ClassRecoverable {
		t.Fatalf("want one recoverable header error, got %+v", uerrs)
	}
}

func TestAV1OversizeOBURejected(t *testing.T) {
	// LEB128 encoding of av1MaxOBUSize+1 (16 MiB + 1).
	oversize := av1MaxOBUSize + 1
	stream := []byte{byte(obuFrame)<<3 | 0x02}
	rem := oversize
	for {
		b := byte(rem & 0x7f)
		rem >>= 7
		if rem != 0 {
			stream = append(stream, b|0x80)
		} else {
			stream = append(stream, b)
			break
		}
	}
	sp := AV1Adapter{}.NewSplitter()
	_, _, err := sp.Write(stream)
	if err == nil || media.ClassOf(err) != media.ClassRecoverable {
		t.Fatalf("oversize OBU must raise recoverable error, got %v", err)
	}
}

func TestAV1ByteAtATimeInvariant(t *testing.T) {
	stream := append(buildOBU(obuSeqHeader, []byte{1}),
		buildOBU(obuFrame, []byte{0x00, 1, 2})...)
	stream = append(stream, buildOBU(obuFrame, []byte{0x40})...)
	sp := AV1Adapter{}.NewSplitter()
	var n, keys int
	for i := 0; i < len(stream); i++ {
		units, _, err := sp.Write(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range units {
			n++
			if u.IsKeyframe {
				keys++
			}
		}
	}
	if _, dangling := sp.Close(); dangling != nil {
		t.Fatal(dangling)
	}
	if n != 3 || keys != 1 {
		t.Fatalf("want 3 OBUs / 1 keyframe regardless of chunking, got %d / %d", n, keys)
	}
}
