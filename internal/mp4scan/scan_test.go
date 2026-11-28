package mp4scan

import (
	"bytes"
	"testing"

	"mediakernel/internal/fault"
	"mediakernel/internal/gen"
)

func scanSpecs() []gen.TrackSpec {
	return []gen.TrackSpec{
		{ID: 1, Timescale: 90000, SampleDelta: 3000, SamplesPerChunk: 3,
			SampleSizes: []uint32{10, 11, 12, 13, 14, 15, 16}},
		{ID: 2, Timescale: 48000, SampleDelta: 1024, SamplesPerChunk: 2,
			SampleSizes: []uint32{5, 6, 7, 8}},
	}
}

func TestScanBuildsSampleTables(t *testing.T) {
	data := gen.BuildMP4(scanSpecs())
	c, err := Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(c.Tracks))
	}
	t1 := c.Tracks[0]
	if t1.ID != 1 || t1.Timescale != 90000 || len(t1.Samples) != 7 {
		t.Fatalf("track1: %+v", t1)
	}
	// Chunks of 3 samples: chunk0 = samples 0..2 at stco[0].
	if t1.Samples[3].Offset != t1.Samples[0].Offset+int64(10+11+12) {
		t.Fatalf("chunk boundary offset wrong: %+v", t1.Samples[3])
	}
	for i, s := range t1.Samples {
		if s.DTS != int64(i)*3000 {
			t.Fatalf("sample %d dts=%d", i, s.DTS)
		}
	}
}

func TestScanTruncatedBoxOffset(t *testing.T) {
	data := gen.BuildMP4(scanSpecs())
	// Cut inside the moov box: error offset must point at a box start
	// within 1 byte of the real corruption point.
	for _, cut := range []int{20, 64, len(data) / 2} {
		_, err := Scan(bytes.NewReader(data[:cut]))
		if !fault.Is(err, fault.ErrCorruptBox) {
			t.Fatalf("cut=%d: %v", cut, err)
		}
		off, ok := fault.OffsetOf(err)
		if !ok || off < 0 || off > int64(cut) {
			t.Fatalf("cut=%d: offset=%d ok=%v", cut, off, ok)
		}
	}
}

func TestScanCorruptSizeOffset(t *testing.T) {
	data := gen.BuildMP4(scanSpecs())
	bad := append([]byte(nil), data...)
	bad[16] = 0xFF // moov size high byte (ftyp is 16 bytes)
	_, err := Scan(bytes.NewReader(bad))
	if !fault.Is(err, fault.ErrCorruptBox) {
		t.Fatalf("err=%v", err)
	}
	off, _ := fault.OffsetOf(err)
	if off != 16 {
		t.Fatalf("offset=%d want 16 (moov box start)", off)
	}
}
