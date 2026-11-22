package bitstream

import "testing"

// nal builds one Annex B NAL unit.
func nal(nalType byte, payload ...byte) []byte {
	out := []byte{0, 0, 0, 1, 0x60 | nalType}
	return append(out, payload...)
}

func sample() []byte {
	var b []byte
	b = append(b, nal(7, 0x11, 0x22)...)       // SPS
	b = append(b, nal(8, 0x33)...)             // PPS
	b = append(b, nal(5, 0x44, 0x55, 0x66)...) // IDR
	b = append(b, nal(1, 0x77)...)             // slice
	b = append(b, nal(1, 0x88, 0x99)...)       // slice
	b = append(b, nal(5, 0xAA)...)             // IDR
	return b
}

func TestScanFindsNALsAndKeyframes(t *testing.T) {
	res := Scan(sample(), 0, true)
	if len(res.NALs) != 6 {
		t.Fatalf("want 6 NALs, got %d", len(res.NALs))
	}
	if len(res.Issues) != 0 {
		t.Fatalf("unexpected issues: %+v", res.Issues)
	}
	if len(res.Keyframes) != 2 || res.Keyframes[0] != 2 || res.Keyframes[1] != 5 {
		t.Fatalf("keyframes = %v, want [2 5]", res.Keyframes)
	}
	if res.NALs[0].Type != NALTypeSPS || res.NALs[1].Type != NALTypePPS {
		t.Fatalf("SPS/PPS types wrong: %d %d", res.NALs[0].Type, res.NALs[1].Type)
	}
	if res.RecoverableBoundary != int64(len(sample())) {
		t.Fatalf("boundary = %d, want %d", res.RecoverableBoundary, len(sample()))
	}
}

func TestScanTruncatedTail(t *testing.T) {
	full := sample()
	cut := full[:len(full)-1] // final IDR loses its payload tail
	res := Scan(cut, 0, false)
	last := res.NALs[len(res.NALs)-1]
	if !last.Truncated {
		t.Fatal("final NAL not flagged truncated")
	}
	if res.RecoverableBoundary != last.Offset {
		t.Fatalf("boundary = %d, want %d", res.RecoverableBoundary, last.Offset)
	}
	found := false
	for _, is := range res.Issues {
		if is.Reason == "truncated NAL at end of input" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing truncation issue: %+v", res.Issues)
	}
	// A clean tail is not truncated.
	res = Scan(full, 0, true)
	if res.NALs[len(res.NALs)-1].Truncated {
		t.Fatal("clean tail flagged truncated")
	}
}

func TestScanEmptyNAL(t *testing.T) {
	var b []byte
	b = append(b, nal(1, 0x11)...)
	b = append(b, 0, 0, 0, 1) // bare start code: empty NAL
	b = append(b, nal(1, 0x22)...)
	res := Scan(b, 0, true)
	if len(res.Issues) != 1 || res.Issues[0].Reason != "empty NAL unit (invalid length)" {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if len(res.NALs) != 2 {
		t.Fatalf("want 2 real NALs, got %d", len(res.NALs))
	}
}

func TestIndexVerifyAndIntervals(t *testing.T) {
	res := Scan(sample(), 0, true)
	idx := Build(res)
	if len(idx.Offsets) != 2 {
		t.Fatalf("want 2 index entries, got %d", len(idx.Offsets))
	}
	hits, missing := idx.Verify(res)
	if hits != 2 || len(missing) != 0 {
		t.Fatalf("hits=%d missing=%v", hits, missing)
	}
	if r := idx.HitRate(res); r != 1.0 {
		t.Fatalf("hit rate = %v, want 1.0", r)
	}
	iv := idx.Intervals()
	if len(iv) != 1 || iv[0] != idx.Offsets[1]-idx.Offsets[0] {
		t.Fatalf("intervals = %v", iv)
	}

	// Drop the second entry: rescan finds a keyframe the index missed.
	idx.Offsets = idx.Offsets[:1]
	hits, _ = idx.Verify(res)
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if r := idx.HitRate(res); r != 1.0 {
		t.Fatalf("HitRate counts entries only: got %v", r)
	}
	// A bogus offset is reported missing.
	bogus := Index{Offsets: []int64{idx.Offsets[0], 9999}}
	hits, missing = bogus.Verify(res)
	if hits != 1 || len(missing) != 1 || missing[0] != 9999 {
		t.Fatalf("bogus verify: hits=%d missing=%v", hits, missing)
	}
}

func TestIsMP4(t *testing.T) {
	if IsMP4(sample()) {
		t.Fatal("Annex B sample misdetected as MP4")
	}
	// Minimal ftyp box.
	ftyp := []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0, 0, 0, 0, 'i', 's', 'o', 'm', 0, 0, 0, 0}
	if !IsMP4(ftyp) {
		t.Fatal("ftyp box not detected as MP4")
	}
}

func TestFindStartCode(t *testing.T) {
	b := []byte{9, 0, 0, 1, 5, 0, 0, 0, 1, 7}
	idx, size := FindStartCode(b, 0)
	if idx != 1 || size != 3 {
		t.Fatalf("3-byte code: idx=%d size=%d", idx, size)
	}
	idx, size = FindStartCode(b, 4)
	if idx != 5 || size != 4 {
		t.Fatalf("4-byte code: idx=%d size=%d", idx, size)
	}
	if idx, _ := FindStartCode([]byte{1, 2, 3}, 0); idx != -1 {
		t.Fatal("found code in empty input")
	}
}
