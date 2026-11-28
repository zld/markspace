package rangeread

import (
	"bytes"
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/fault"
	"mediakernel/internal/gen"
	"mediakernel/internal/index"
	"mediakernel/internal/mp4scan"
)

func fixture(t *testing.T) (*index.Index, []byte) {
	t.Helper()
	specs := []gen.TrackSpec{
		{ID: 1, Timescale: 1, SampleDelta: 1, SamplesPerChunk: 4,
			SampleSizes: []uint32{10, 10, 10, 10, 10, 10, 10, 10}}, // t=0..7 s
	}
	data := gen.BuildMP4(specs)
	c, err := mp4scan.Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Build(c)
	if err != nil {
		t.Fatal(err)
	}
	return ix, data
}

func sec(n int64) clock.Rational { return clock.Rational{Num: n, Den: 1} }

func TestHalfOpenBoundaries(t *testing.T) {
	ix, _ := fixture(t)
	hits, err := Read(ix, Request{TrackID: 1, Start: sec(2), End: sec(5)})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || len(hits[0]) != 3 {
		t.Fatalf("hits=%v", hits)
	}
	// [2,5) covers samples starting at t=2,3,4.
	for i, h := range hits[0] {
		if h.Entry.DTS != int64(i+2) {
			t.Fatalf("hit %d dts=%d", i, h.Entry.DTS)
		}
	}
	// Empty range rejected.
	if _, err := Read(ix, Request{TrackID: 1, Start: sec(3), End: sec(3)}); !fault.Is(err, fault.ErrEmptyRange) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Read(ix, Request{TrackID: 1, Start: sec(5), End: sec(2)}); !fault.Is(err, fault.ErrEmptyRange) {
		t.Fatalf("inverted: %v", err)
	}
	// Duplicate track request rejected.
	_, err = Read(ix,
		Request{TrackID: 1, Start: sec(0), End: sec(1)},
		Request{TrackID: 1, Start: sec(1), End: sec(2)})
	if !fault.Is(err, fault.ErrDuplicateTrack) {
		t.Fatalf("dup: %v", err)
	}
}

func TestReadDataMatchesGenerator(t *testing.T) {
	ix, data := fixture(t)
	hits, err := Read(ix, Request{TrackID: 1, Start: sec(0), End: sec(2)})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := ReadData(bytes.NewReader(data), hits[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 20 {
		t.Fatalf("blob=%d bytes", len(blob))
	}
	for i, b := range blob[:10] {
		if b != gen.SampleByte(1, 0, i) {
			t.Fatalf("byte %d = %d", i, b)
		}
	}
}
