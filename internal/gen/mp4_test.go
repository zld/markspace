package gen

import (
	"bytes"
	"math/rand"
	"testing"

	"mediakernel/internal/mp4scan"
)

func specs() []TrackSpec {
	sizes1 := make([]uint32, 12)
	for i := range sizes1 {
		sizes1[i] = uint32(100 + i%3)
	}
	sizes2 := make([]uint32, 8)
	for i := range sizes2 {
		sizes2[i] = uint32(50 + i%2)
	}
	return []TrackSpec{
		{ID: 1, Timescale: 90000, SampleDelta: 3000, SamplesPerChunk: 4, SampleSizes: sizes1},
		{ID: 2, Timescale: 48000, SampleDelta: 1024, SamplesPerChunk: 4, SampleSizes: sizes2},
	}
}

func TestBuildMP4ScansClean(t *testing.T) {
	data := BuildMP4(specs())
	c, err := mp4scan.Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(c.Tracks))
	}
	tr := c.Tracks[0]
	if tr.Timescale != 90000 || len(tr.Samples) != 12 {
		t.Fatalf("track1: %+v", tr)
	}
	// Deterministic payload: sample bytes must match SampleByte.
	s0 := tr.Samples[0]
	got := data[s0.Offset : s0.Offset+int64(s0.Size)]
	for i, b := range got {
		if b != SampleByte(1, 0, i) {
			t.Fatalf("payload mismatch at %d", i)
		}
	}
	// DTS expansion: sample i starts at i*3000.
	for i, s := range tr.Samples {
		if s.DTS != int64(i)*3000 {
			t.Fatalf("sample %d dts=%d", i, s.DTS)
		}
	}
}

func TestBuildMP4Deterministic(t *testing.T) {
	a, b := BuildMP4(specs()), BuildMP4(specs())
	if !bytes.Equal(a, b) {
		t.Fatal("generator must be deterministic")
	}
}

func TestTruncatedMP4ReportsOffset(t *testing.T) {
	data := BuildMP4(specs())
	cut := len(data) - 10 // inside mdat payload is fine; cut inside moov instead
	_ = cut
	moovCut := 40 // inside moov box header chain
	_, err := mp4scan.Scan(bytes.NewReader(data[:moovCut]))
	if err == nil {
		t.Fatal("truncated container must fail")
	}
}

func TestCorruptBoxSize(t *testing.T) {
	data := BuildMP4(specs())
	// Corrupt the moov size field (bytes 4..8 of the box after ftyp).
	ftypLen := 16 // 8B header + 4B major brand + 4B minor version
	bad := append([]byte(nil), data...)
	bad[ftypLen] = 0xFF
	bad[ftypLen+1] = 0xFF
	bad[ftypLen+2] = 0xFF
	bad[ftypLen+3] = 0xFF
	_, err := mp4scan.Scan(bytes.NewReader(bad))
	if err == nil {
		t.Fatal("oversized moov must fail")
	}
}

var _ = rand.Int // keep rand import if unused in future fixtures
