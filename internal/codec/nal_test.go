package codec

import (
	"testing"
)

// annexB builds a simple Annex B stream from NAL payloads.
func annexB(payloads ...[]byte) []byte {
	var out []byte
	for _, p := range payloads {
		out = append(out, 0x00, 0x00, 0x00, 0x01)
		out = append(out, p...)
	}
	return out
}

func TestH264AnnexBBasic(t *testing.T) {
	// NAL types: 7=SPS, 5=IDR (keyframe), 1=non-IDR.
	stream := annexB(
		[]byte{0x67, 0x11}, // SPS
		[]byte{0x65, 0x22}, // IDR
		[]byte{0x41, 0x33}, // non-IDR
	)
	sc := newAnnexBScanner(H264)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	gaps := sc.Flush(int64(len(stream)), &units)
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %+v", gaps)
	}
	if len(units) != 3 {
		t.Fatalf("units=%d want 3", len(units))
	}
	wantTypes := []int{7, 5, 1}
	for i, u := range units {
		if u.Type != wantTypes[i] {
			t.Fatalf("unit %d type=%d want %d", i, u.Type, wantTypes[i])
		}
		if u.Start != u.End-int64(len(u.Raw)) {
			t.Fatal("unit offsets inconsistent")
		}
	}
	if !units[1].Keyframe || units[0].Keyframe || units[2].Keyframe {
		t.Fatalf("keyframe classification wrong: %v %v %v",
			units[0].Keyframe, units[1].Keyframe, units[2].Keyframe)
	}
	if units[1].TypeName != "idr-slice" {
		t.Fatalf("label=%s", units[1].TypeName)
	}
	// First payload starts right after the 4-byte start code.
	if units[0].Start != 4 {
		t.Fatalf("first unit start=%d want 4", units[0].Start)
	}
}

func TestH265AnnexBKeyframes(t *testing.T) {
	// HEVC NAL header byte: type in high 6 bits. type 19 (IDR_W_RADL)
	// -> first byte 19<<1 = 0x26; type 1 (TRAIL_R) -> 0x02.
	stream := annexB(
		[]byte{0x40, 0x01}, // type 32 VPS
		[]byte{0x26, 0x01}, // type 19 IDR
		[]byte{0x02, 0x01}, // type 1 TRAIL_R
	)
	sc := newAnnexBScanner(H265)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	if g := sc.Flush(int64(len(stream)), &units); len(g) != 0 {
		t.Fatalf("gaps: %+v", g)
	}
	if len(units) != 3 {
		t.Fatalf("units=%d", len(units))
	}
	want := []int{32, 19, 1}
	for i, u := range units {
		if u.Type != want[i] {
			t.Fatalf("unit %d type=%d want %d (%s)", i, u.Type, want[i], u.TypeName)
		}
	}
	if !units[1].Keyframe {
		t.Fatal("HEVC IDR must be a keyframe")
	}
	if units[0].Keyframe || units[2].Keyframe {
		t.Fatal("VPS/TRAIL must not be keyframes")
	}
}

func TestAnnexBStartCodeSplitAcrossChunks(t *testing.T) {
	stream := annexB(
		[]byte{0x65, 0xaa}, // IDR unit A
		[]byte{0x41, 0xbb}, // non-IDR unit B
	)
	// Split so the start code 00 00 00 01 is cut in several places.
	for _, cut := range []int{5, 6, 7, 8, 9} {
		sc := newAnnexBScanner(H264)
		var units []Unit
		if err := sc.Push(stream[:cut], 0, &units); err != nil {
			t.Fatal(err)
		}
		if err := sc.Push(stream[cut:], int64(cut), &units); err != nil {
			t.Fatal(err)
		}
		if g := sc.Flush(int64(len(stream)), &units); len(g) != 0 {
			t.Fatalf("cut=%d gaps: %+v", cut, g)
		}
		if len(units) != 2 {
			t.Fatalf("cut=%d units=%d want 2", cut, len(units))
		}
		if !units[0].Keyframe || units[1].Keyframe {
			t.Fatalf("cut=%d keyframe flags wrong", cut)
		}
		// Layout: SC(4) payload(2) SC(4) payload(2): second payload at 10.
		if units[1].Start != 10 {
			t.Fatalf("cut=%d second start=%d want 10", cut, units[1].Start)
		}
		if units[0].Start != 4 || units[0].End != 6 {
			t.Fatalf("cut=%d first unit offsets wrong: %+v", cut, units[0])
		}
	}
}

func TestAnnexBThreeByteStartCode(t *testing.T) {
	stream := []byte{0x00, 0x00, 0x01, 0x65, 0x77,
		0x00, 0x00, 0x01, 0x41, 0x88}
	sc := newAnnexBScanner(H264)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	if g := sc.Flush(int64(len(stream)), &units); len(g) != 0 {
		t.Fatalf("gaps: %+v", g)
	}
	if len(units) != 2 || units[0].Start != 3 || units[1].Start != 8 {
		t.Fatalf("units=%+v", units)
	}
}

func TestAnnexBTruncatedTailIsGap(t *testing.T) {
	// Two complete units, then a start code followed by a single byte: a
	// header that runs past EOF is a truncated tail, not a usable NAL.
	stream := []byte{0x00, 0x00, 0x01, 0x65, 0xaa,
		0x00, 0x00, 0x01, 0x41, 0xbb,
		0x00, 0x00, 0x01, 0x00}
	sc := newAnnexBScanner(H264)
	var units []Unit
	if err := sc.Push(stream, 0, &units); err != nil {
		t.Fatal(err)
	}
	gaps := sc.Flush(int64(len(stream)), &units)
	if len(units) != 2 {
		t.Fatalf("valid units=%d want 2", len(units))
	}
	if len(gaps) != 1 {
		t.Fatalf("gaps=%d want 1", len(gaps))
	}
	g := gaps[0]
	// Partial unit payload is the single 0x00 byte at offset 13.
	if g.Code != "parse.truncated-tail" || g.Start != 13 || g.End != 14 {
		t.Fatalf("gap wrong: %+v", g)
	}
	// Earlier valid units must remain intact and unextended by the gap.
	if units[0].Start != 3 || units[0].End != 5 {
		t.Fatalf("first unit damaged: %+v", units[0])
	}
	if units[1].Start != 8 || units[1].End != 10 {
		t.Fatalf("second unit damaged: %+v", units[1])
	}
}

func TestAnnexBByteAtATimeMatchesBulk(t *testing.T) {
	stream := annexB(
		[]byte{0x67, 0x11}, []byte{0x65, 0x22, 0x33},
		[]byte{0x41}, []byte{0x65, 0x44},
	)
	var bulk, streamed []Unit
	bulkSc := newAnnexBScanner(H264)
	if err := bulkSc.Push(stream, 0, &bulk); err != nil {
		t.Fatal(err)
	}
	bulkSc.Flush(int64(len(stream)), &bulk)

	st := newAnnexBScanner(H264)
	for i, b := range stream {
		if err := st.Push([]byte{b}, int64(i), &streamed); err != nil {
			t.Fatal(err)
		}
	}
	st.Flush(int64(len(stream)), &streamed)
	if len(streamed) != len(bulk) {
		t.Fatalf("streamed %d units vs bulk %d", len(streamed), len(bulk))
	}
	for i := range bulk {
		a, b := bulk[i], streamed[i]
		if a.Start != b.Start || a.End != b.End || a.Type != b.Type ||
			string(a.Raw) != string(b.Raw) {
			t.Fatalf("unit %d mismatch: %+v vs %+v", i, a, b)
		}
	}
}
