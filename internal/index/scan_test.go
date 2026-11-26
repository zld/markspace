package index

import (
	"errors"
	"testing"

	"liveregress/internal/fault"
)

func TestScanAnnexBH264(t *testing.T) {
	idr := []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}
	nonIDR := []byte{0, 0, 1, 0x41, 0x9a, 0x02}
	if ok, _ := ScanSample(CodecH264, idr); !ok {
		t.Fatal("IDR not detected")
	}
	if ok, _ := ScanSample(CodecH264, nonIDR); ok {
		t.Fatal("non-IDR reported as keyframe")
	}
	// A start code inside a slice payload must not be mistaken for an IDR.
	garbage := []byte{0, 0, 1, 0x41, 0, 0, 0, 1, 0x05, 0x88}
	if ok, _ := ScanSample(CodecH264, garbage); !ok {
		t.Fatalf("expected IDR after slice header, got false")
	}
}

func TestScanAnnexBH265(t *testing.T) {
	idr := []byte{0, 0, 0, 1, 0x26, 0x01} // nal type 19
	cra := []byte{0, 0, 1, 0x2A, 0x01}    // nal type 21
	trail := []byte{0, 0, 1, 0x02, 0x01}  // nal type 1
	if ok, _ := ScanSample(CodecH265, idr); !ok {
		t.Fatal("H.265 IDR not detected")
	}
	if ok, _ := ScanSample(CodecH265, cra); !ok {
		t.Fatal("H.265 CRA not detected")
	}
	if ok, _ := ScanSample(CodecH265, trail); ok {
		t.Fatal("H.265 TRAIL reported as keyframe")
	}
}

func TestScanLengthPrefixed(t *testing.T) {
	au := []byte{0, 0, 0, 2, 0x67, 0x42, 0, 0, 0, 2, 0x65, 0x88}
	ok, err := LengthPrefixed(au, 4, false)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestUnknownCodecFails(t *testing.T) {
	_, err := ScanSample(Codec("vp9"), nil)
	if !errors.Is(err, fault.ErrUnsupported) {
		t.Fatalf("want unsupported, got %v", err)
	}
}

func TestRebuildFromSamples(t *testing.T) {
	samples := []SampleRef{
		{Time: 0, Offset: 10, Size: 5, TrackID: 1},
		{Time: 3000, Offset: 20, Size: 5, TrackID: 1},
		{Time: 6000, Offset: 30, Size: 5, TrackID: 1},
	}
	payloads := map[int64][]byte{
		10: {0, 0, 0, 1, 0x65, 1},
		20: {0, 0, 0, 1, 0x41, 1},
		30: {0, 0, 1, 0x65, 1},
	}
	read := func(s SampleRef) ([]byte, error) { return payloads[s.Offset], nil }
	idx, err := Rebuild("v0", 90000, CodecH264, samples, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 2 || idx.Entries[1].Time != 6000 {
		t.Fatalf("entries = %+v", idx.Entries)
	}
	if idx.Source != "rebuilt" {
		t.Fatalf("source = %q", idx.Source)
	}
}

func TestRebuildNoKeyframes(t *testing.T) {
	samples := []SampleRef{{Time: 0, Offset: 0, Size: 4, TrackID: 1}}
	read := func(SampleRef) ([]byte, error) { return []byte{0, 0, 1, 0x41}, nil }
	_, err := Rebuild("v0", 90000, CodecH264, samples, read)
	if !errors.Is(err, fault.ErrIndex) {
		t.Fatalf("want index fault, got %v", err)
	}
}
